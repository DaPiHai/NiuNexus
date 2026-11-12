// Package routing manages static routes and policy-routing rules through the
// iproute2 command line. Every apply is preceded by a persisted desired-state
// file so a rollback can remove exactly the entries NiuNexus owns.
package routing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vishvananda/netlink"

	"niunexus/internal/changes"
	"niunexus/internal/config"
)

// Runner executes one ip command.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the production runner.
type ExecRunner struct {
	Timeout time.Duration
}

// Run executes ip with LC_ALL=C and captures stderr in the error.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// RouteRequest adds a static route.
type RouteRequest struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Device      string `json:"device"`
	Metric      int    `json:"metric"`
}

// PolicyRequest adds a policy-routing rule.
type PolicyRequest struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	WAN      string `json:"wan"`
	Gateway  string `json:"gateway"`
	Table    int    `json:"table"`
	Priority int    `json:"priority"`
}

// RouteDetail marks whether a configured route is live.
type RouteDetail struct {
	config.StaticRoute
	Present bool `json:"present"`
}

// PolicyDetail marks whether a configured policy rule is live.
type PolicyDetail struct {
	config.PolicyRule
	Present bool `json:"present"`
}

// LiveRoute is a route read from the kernel.
type LiveRoute struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Table       int    `json:"table"`
	Metric      int    `json:"metric"`
}

// LiveRule is a policy rule read from the kernel.
type LiveRule struct {
	Priority int    `json:"priority"`
	Table    int    `json:"table"`
	Source   string `json:"source,omitempty"`
}

// Snapshot is the routes page payload.
type Snapshot struct {
	Routes     []RouteDetail  `json:"routes"`
	Rules      []PolicyDetail `json:"rules"`
	LiveRoutes []LiveRoute    `json:"liveRoutes"`
	LiveRules  []LiveRule     `json:"liveRules"`
	Warnings   []string       `json:"warnings,omitempty"`
}

type routeState struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device,omitempty"`
	Metric      int    `json:"metric,omitempty"`
}

type ruleState struct {
	Source   string `json:"source"`
	WAN      string `json:"wan"`
	Gateway  string `json:"gateway"`
	Table    int    `json:"table"`
	Priority int    `json:"priority"`
}

type state struct {
	Routes []routeState `json:"routes"`
	Rules  []ruleState  `json:"rules"`
}

// Controller owns static routes and policy rules.
type Controller struct {
	ip        string
	runner    Runner
	store     *config.Store
	changes   *changes.Manager
	statePath string
}

// New creates a controller.
func New(runner Runner, store *config.Store, manager *changes.Manager, varDir string) *Controller {
	if runner == nil {
		runner = ExecRunner{}
	}
	bin := "ip"
	if path, err := exec.LookPath("ip"); err == nil {
		bin = path
	} else if _, err := os.Stat("/usr/sbin/ip"); err == nil {
		bin = "/usr/sbin/ip"
	}
	return &Controller{
		ip:        bin,
		runner:    runner,
		store:     store,
		changes:   manager,
		statePath: filepath.Join(varDir, "routing-state.json"),
	}
}

// List returns configured routes/rules plus live kernel state.
func (c *Controller) List(ctx context.Context) (*Snapshot, error) {
	cfg := c.store.Config()
	warnings := []string{}
	liveRoutes, err := c.liveRoutes(ctx)
	if err != nil {
		warnings = append(warnings, "读取路由表失败: "+err.Error())
	}
	liveRules, err := c.liveRules(ctx)
	if err != nil {
		warnings = append(warnings, "读取策略规则失败: "+err.Error())
	}

	routeSet := map[string]bool{}
	for _, route := range liveRoutes {
		routeSet[routeKey(routeState{
			Destination: route.Destination,
			Gateway:     route.Gateway,
			Device:      route.Device,
			Metric:      route.Metric,
		})] = true
	}
	ruleSet := map[string]bool{}
	for _, rule := range liveRules {
		ruleSet[fmt.Sprintf("%s|%d|%d", rule.Source, rule.Table, rule.Priority)] = true
	}

	routes := make([]RouteDetail, 0, len(cfg.StaticRoutes))
	for _, route := range cfg.StaticRoutes {
		routes = append(routes, RouteDetail{
			StaticRoute: route,
			Present: routeSet[routeKey(routeState{
				Destination: normalizeDestination(route.Destination),
				Gateway:     route.Gateway,
				Device:      route.Device,
				Metric:      route.Metric,
			})],
		})
	}
	rules := make([]PolicyDetail, 0, len(cfg.PolicyRules))
	for _, rule := range cfg.PolicyRules {
		rules = append(rules, PolicyDetail{
			PolicyRule: rule,
			Present:    ruleSet[fmt.Sprintf("%s|%d|%d", rule.Source, rule.Table, rule.Priority)],
		})
	}
	return &Snapshot{
		Routes:     routes,
		Rules:      rules,
		LiveRoutes: liveRoutes,
		LiveRules:  liveRules,
		Warnings:   warnings,
	}, nil
}

// AddRoute creates a static route.
func (c *Controller) AddRoute(ctx context.Context, req RouteRequest, actor string) (*changes.Result, error) {
	item := config.StaticRoute{
		ID:          "rt-" + randomHex(6),
		Destination: strings.TrimSpace(req.Destination),
		Gateway:     strings.TrimSpace(req.Gateway),
		Device:      strings.TrimSpace(req.Device),
		Metric:      req.Metric,
	}
	item.Destination = normalizeDestination(item.Destination)
	if item.Device == "" && item.Gateway != "" {
		device, err := resolveGatewayDevice(item.Gateway)
		if err != nil {
			return nil, err
		}
		item.Device = device
	}
	if item.Device != "" {
		if _, err := netlink.LinkByName(item.Device); err != nil {
			return nil, fmt.Errorf("出口网口不存在: %s", item.Device)
		}
	}
	cfg := c.store.Config()
	if err := config.ValidateStaticRoutes(append(append([]config.StaticRoute{}, cfg.StaticRoutes...), item)); err != nil {
		return nil, err
	}
	risky := isDefaultRoute(item.Destination)
	return c.runChange(ctx, "route.create", item.Destination, actor, routePreview(item), risky, func(cfg *config.Config) error {
		cfg.StaticRoutes = append(cfg.StaticRoutes, item)
		return nil
	})
}

// DeleteRoute removes a static route.
func (c *Controller) DeleteRoute(ctx context.Context, id, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	existing := findRoute(cfg, id)
	if existing == nil {
		return nil, fmt.Errorf("静态路由不存在: %s", id)
	}
	risky := isDefaultRoute(existing.Destination)
	return c.runChange(ctx, "route.delete", existing.Destination, actor, "删除静态路由 "+existing.Destination, risky, func(cfg *config.Config) error {
		out := cfg.StaticRoutes[:0]
		for _, route := range cfg.StaticRoutes {
			if route.ID != id {
				out = append(out, route)
			}
		}
		cfg.StaticRoutes = out
		return nil
	})
}

// AddPolicy creates a policy-routing rule and its dedicated table default route.
func (c *Controller) AddPolicy(ctx context.Context, req PolicyRequest, actor string) (*changes.Result, error) {
	item := config.PolicyRule{
		ID:       "pr-" + randomHex(6),
		Name:     strings.TrimSpace(req.Name),
		Source:   strings.TrimSpace(req.Source),
		WAN:      strings.TrimSpace(req.WAN),
		Gateway:  strings.TrimSpace(req.Gateway),
		Table:    req.Table,
		Priority: req.Priority,
	}
	if _, err := netlink.LinkByName(item.WAN); err != nil {
		return nil, fmt.Errorf("出口网口不存在: %s", item.WAN)
	}
	cfg := c.store.Config()
	if err := config.ValidatePolicyRules(append(append([]config.PolicyRule{}, cfg.PolicyRules...), item)); err != nil {
		return nil, err
	}
	return c.runChange(ctx, "policy.create", item.Name, actor,
		fmt.Sprintf("来源 %s 经 %s (%s) 查表 %d", item.Source, item.WAN, item.Gateway, item.Table),
		true,
		func(cfg *config.Config) error {
			cfg.PolicyRules = append(cfg.PolicyRules, item)
			return nil
		})
}

// DeletePolicy removes a policy rule and its table default route.
func (c *Controller) DeletePolicy(ctx context.Context, id, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	existing := findPolicy(cfg, id)
	if existing == nil {
		return nil, fmt.Errorf("策略路由不存在: %s", id)
	}
	return c.runChange(ctx, "policy.delete", existing.Name, actor,
		"删除策略路由 "+existing.Source, true,
		func(cfg *config.Config) error {
			out := cfg.PolicyRules[:0]
			for _, rule := range cfg.PolicyRules {
				if rule.ID != id {
					out = append(out, rule)
				}
			}
			cfg.PolicyRules = out
			return nil
		})
}

// ApplyCurrent reapplies persisted routes and rules.
func (c *Controller) ApplyCurrent(ctx context.Context) error {
	return c.apply(ctx)
}

// Cleanup removes NiuNexus-owned routes and rules.
func (c *Controller) Cleanup(ctx context.Context) error {
	current, err := c.readState()
	if err != nil {
		return err
	}
	var problems []string
	for _, rule := range current.Rules {
		if err := c.ruleDel(ctx, rule); err != nil && !isMissing(err) {
			problems = append(problems, err.Error())
		}
		_ = c.routeDelDefaultTable(ctx, rule.Table)
	}
	for _, route := range current.Routes {
		if err := c.routeDel(ctx, route); err != nil && !isMissing(err) {
			problems = append(problems, err.Error())
		}
	}
	_ = os.Remove(c.statePath)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "；"))
	}
	return nil
}

// RegisterRecovery installs the crash-recovery handler.
func (c *Controller) RegisterRecovery() {
	c.changes.RegisterUndo("routing", func(json.RawMessage) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return c.ApplyCurrent(ctx)
	})
}

func (c *Controller) runChange(ctx context.Context, action, target, actor, preview string, risky bool, mutate func(*config.Config) error) (*changes.Result, error) {
	apply := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := c.store.Update(mutate); err != nil {
			return err
		}
		return c.apply(opCtx)
	}
	rollback := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return c.apply(opCtx)
	}
	undoFn := func() (*changes.Undo, error) {
		return &changes.Undo{Kind: "routing", Data: json.RawMessage(`{}`)}, nil
	}
	return c.changes.Run(ctx, changes.Spec{
		Action:   action,
		Target:   target,
		Actor:    actor,
		Preview:  preview,
		Risky:    risky,
		Timeout:  c.changeTimeout(),
		Apply:    apply,
		Rollback: rollback,
		UndoFn:   undoFn,
	})
}

func (c *Controller) apply(ctx context.Context) error {
	previous, err := c.readState()
	if err != nil {
		return err
	}
	next := desiredState(c.store.Config())
	if err := c.writeState(next); err != nil {
		return err
	}

	for _, route := range previous.Routes {
		if !containsRoute(next.Routes, route) {
			if err := c.routeDel(ctx, route); err != nil && !isMissing(err) {
				return err
			}
		}
	}
	for _, rule := range previous.Rules {
		if !containsRule(next.Rules, rule) {
			if err := c.ruleDel(ctx, rule); err != nil && !isMissing(err) {
				return err
			}
			_ = c.routeDelDefaultTable(ctx, rule.Table)
		}
	}
	for _, route := range next.Routes {
		if err := c.routeReplace(ctx, route); err != nil {
			return err
		}
	}
	for _, rule := range next.Rules {
		_ = c.ruleDel(ctx, rule)
		if err := c.ruleAdd(ctx, rule); err != nil {
			return err
		}
		if err := c.routeReplaceDefaultTable(ctx, rule); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) liveRoutes(ctx context.Context) ([]LiveRoute, error) {
	out, err := c.runner.Run(ctx, c.ip, "-4", "-j", "route", "show", "table", "all")
	if err != nil {
		return nil, err
	}
	var items []struct {
		Dst      string          `json:"dst"`
		Gateway  string          `json:"gateway"`
		Dev      string          `json:"dev"`
		Protocol string          `json:"protocol"`
		Table    json.RawMessage `json:"table"`
		Metric   int             `json:"metric"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, err
	}
	routes := make([]LiveRoute, 0, len(items))
	for _, item := range items {
		routes = append(routes, LiveRoute{
			Destination: normalizeDestination(item.Dst),
			Gateway:     item.Gateway,
			Device:      item.Dev,
			Protocol:    item.Protocol,
			Table:       parseTable(item.Table),
			Metric:      item.Metric,
		})
	}
	return routes, nil
}

func (c *Controller) liveRules(ctx context.Context) ([]LiveRule, error) {
	out, err := c.runner.Run(ctx, c.ip, "-4", "-j", "rule", "show")
	if err != nil {
		return nil, err
	}
	var items []struct {
		Priority int             `json:"priority"`
		Src      string          `json:"src"`
		Table    json.RawMessage `json:"table"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, err
	}
	rules := make([]LiveRule, 0, len(items))
	for _, item := range items {
		source := item.Src
		if source == "all" || source == "" {
			source = "0.0.0.0/0"
		}
		rules = append(rules, LiveRule{Priority: item.Priority, Table: parseTable(item.Table), Source: source})
	}
	return rules, nil
}

func (c *Controller) routeReplace(ctx context.Context, route routeState) error {
	args := []string{"-4", "route", "replace"}
	args = append(args, destinationArg(route.Destination)...)
	if route.Gateway != "" {
		args = append(args, "via", route.Gateway)
	}
	if route.Device != "" {
		args = append(args, "dev", route.Device)
	}
	if route.Metric > 0 {
		args = append(args, "metric", strconv.Itoa(route.Metric))
	}
	_, err := c.runner.Run(ctx, c.ip, args...)
	return err
}

func (c *Controller) routeDel(ctx context.Context, route routeState) error {
	args := []string{"-4", "route", "del"}
	args = append(args, destinationArg(route.Destination)...)
	if route.Gateway != "" {
		args = append(args, "via", route.Gateway)
	}
	if route.Device != "" {
		args = append(args, "dev", route.Device)
	}
	if route.Metric > 0 {
		args = append(args, "metric", strconv.Itoa(route.Metric))
	}
	_, err := c.runner.Run(ctx, c.ip, args...)
	return err
}

func (c *Controller) routeReplaceDefaultTable(ctx context.Context, rule ruleState) error {
	_, err := c.runner.Run(ctx, c.ip, "-4", "route", "replace", "default",
		"via", rule.Gateway, "dev", rule.WAN, "table", strconv.Itoa(rule.Table))
	return err
}

func (c *Controller) routeDelDefaultTable(ctx context.Context, table int) error {
	_, err := c.runner.Run(ctx, c.ip, "-4", "route", "del", "default", "table", strconv.Itoa(table))
	return err
}

func (c *Controller) ruleAdd(ctx context.Context, rule ruleState) error {
	_, err := c.runner.Run(ctx, c.ip, "-4", "rule", "add",
		"from", rule.Source, "table", strconv.Itoa(rule.Table), "priority", strconv.Itoa(rule.Priority))
	return err
}

func (c *Controller) ruleDel(ctx context.Context, rule ruleState) error {
	_, err := c.runner.Run(ctx, c.ip, "-4", "rule", "del",
		"from", rule.Source, "table", strconv.Itoa(rule.Table), "priority", strconv.Itoa(rule.Priority))
	return err
}

func (c *Controller) readState() (state, error) {
	var current state
	data, err := os.ReadFile(c.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return current, nil
		}
		return current, err
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return state{}, err
	}
	return current, nil
}

func (c *Controller) writeState(current state) error {
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.statePath)
}

func desiredState(cfg *config.Config) state {
	current := state{
		Routes: make([]routeState, 0, len(cfg.StaticRoutes)),
		Rules:  make([]ruleState, 0, len(cfg.PolicyRules)),
	}
	for _, route := range cfg.StaticRoutes {
		current.Routes = append(current.Routes, routeState{
			Destination: normalizeDestination(route.Destination),
			Gateway:     route.Gateway,
			Device:      route.Device,
			Metric:      route.Metric,
		})
	}
	for _, rule := range cfg.PolicyRules {
		current.Rules = append(current.Rules, ruleState{
			Source:   rule.Source,
			WAN:      rule.WAN,
			Gateway:  rule.Gateway,
			Table:    rule.Table,
			Priority: rule.Priority,
		})
	}
	sort.Slice(current.Routes, func(i, j int) bool {
		return routeKey(current.Routes[i]) < routeKey(current.Routes[j])
	})
	sort.Slice(current.Rules, func(i, j int) bool {
		if current.Rules[i].Table != current.Rules[j].Table {
			return current.Rules[i].Table < current.Rules[j].Table
		}
		return current.Rules[i].Priority < current.Rules[j].Priority
	})
	return current
}

func routeKey(route routeState) string {
	return fmt.Sprintf("%s|%s|%s|%d", route.Destination, route.Gateway, route.Device, route.Metric)
}

func containsRoute(routes []routeState, want routeState) bool {
	key := routeKey(want)
	for _, route := range routes {
		if routeKey(route) == key {
			return true
		}
	}
	return false
}

func containsRule(rules []ruleState, want ruleState) bool {
	key := fmt.Sprintf("%s|%d|%d", want.Source, want.Table, want.Priority)
	for _, rule := range rules {
		if fmt.Sprintf("%s|%d|%d", rule.Source, rule.Table, rule.Priority) == key {
			return true
		}
	}
	return false
}

func normalizeDestination(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "default" || value == "0.0.0.0/0" {
		return "default"
	}
	return value
}

func destinationArg(destination string) []string {
	if destination == "default" {
		return []string{"default"}
	}
	return []string{destination}
}

func parseTable(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 254
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		if number == 0 {
			return 254
		}
		return number
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		switch name {
		case "", "main":
			return 254
		case "local":
			return 255
		default:
			if n, err := strconv.Atoi(name); err == nil {
				return n
			}
		}
	}
	return 254
}

func resolveGatewayDevice(gateway string) (string, error) {
	ip := net.ParseIP(gateway)
	if ip == nil {
		return "", fmt.Errorf("网关地址无效: %s", gateway)
	}
	routes, err := netlink.RouteGet(ip)
	if err != nil || len(routes) == 0 {
		return "", fmt.Errorf("无法确定网关 %s 所在网口", gateway)
	}
	link, err := netlink.LinkByIndex(routes[0].LinkIndex)
	if err != nil {
		return "", fmt.Errorf("无法解析网关网口: %w", err)
	}
	return link.Attrs().Name, nil
}

func isDefaultRoute(destination string) bool {
	return normalizeDestination(destination) == "default"
}

func (c *Controller) changeTimeout() time.Duration {
	seconds := c.store.Config().Settings.ConfirmTimeoutSec
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}

func findRoute(cfg *config.Config, id string) *config.StaticRoute {
	for i := range cfg.StaticRoutes {
		if cfg.StaticRoutes[i].ID == id {
			route := cfg.StaticRoutes[i]
			return &route
		}
	}
	return nil
}

func findPolicy(cfg *config.Config, id string) *config.PolicyRule {
	for i := range cfg.PolicyRules {
		if cfg.PolicyRules[i].ID == id {
			rule := cfg.PolicyRules[i]
			return &rule
		}
	}
	return nil
}

func routePreview(route config.StaticRoute) string {
	gateway := route.Gateway
	if gateway == "" {
		gateway = "直连"
	}
	return fmt.Sprintf("%s via %s dev %s metric %d", route.Destination, gateway, route.Device, route.Metric)
}

func isMissing(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "No such process") ||
		strings.Contains(message, "No such file or directory") ||
		strings.Contains(message, "Cannot find device") ||
		strings.Contains(message, "not found")
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
