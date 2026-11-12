// Package nft renders and applies the dedicated inet niunexus table. It never
// flushes the system ruleset and never touches another table.
package nft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"niunexus/internal/config"
)

const (
	tableName   = "inet niunexus"
	markForward = "0x4e58"
	markHairpin = "0x4e59"
)

// Runner executes one nft command. stdin is used for nft -f -.
type Runner interface {
	Run(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error)
}

// ExecRunner is the production runner.
type ExecRunner struct {
	Timeout time.Duration
}

// Run executes nft with LC_ALL=C and captures stderr in the error.
func (r ExecRunner) Run(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Counter is one named nft counter.
type Counter struct {
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// Client applies and reads the NiuNexus table.
type Client struct {
	Bin    string
	Runner Runner
}

// New creates a client using the default runner.
func New(runner Runner) *Client {
	if runner == nil {
		runner = ExecRunner{}
	}
	bin := "nft"
	if path, err := exec.LookPath("nft"); err == nil {
		bin = path
	} else if _, err := os.Stat("/usr/sbin/nft"); err == nil {
		bin = "/usr/sbin/nft"
	}
	return &Client{Bin: bin, Runner: runner}
}

// RenderOptions supplies host-specific exclusions that must not be redirected.
type RenderOptions struct {
	LocalAddresses []string
}

// Apply renders the configuration and replaces the NiuNexus table.
func (c *Client) Apply(ctx context.Context, cfg *config.Config, opts ...RenderOptions) error {
	script, active := Render(cfg, opts...)
	if !active {
		exists, err := c.TableExists(ctx)
		if err != nil {
			// nft is unavailable (for example an unprivileged development
			// run). There are no nft features to enforce; leave the host alone.
			return nil
		}
		if !exists {
			return nil
		}
		return c.Delete(ctx)
	}
	exists, err := c.TableExists(ctx)
	if err != nil {
		return err
	}
	full := script
	if exists {
		full = "delete table " + tableName + "\n" + script
	}
	if _, err := c.Runner.Run(ctx, c.Bin, []byte(full), "-f", "-"); err != nil {
		return fmt.Errorf("应用 nft 规则失败: %w", err)
	}
	return nil
}

// Delete removes the NiuNexus table if it exists.
func (c *Client) Delete(ctx context.Context) error {
	if _, err := c.Runner.Run(ctx, c.Bin, nil, "delete", "table", tableName); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("清理 nft 表失败: %w", err)
	}
	return nil
}

// TableExists checks for the NiuNexus table.
func (c *Client) TableExists(ctx context.Context) (bool, error) {
	if _, err := c.Runner.Run(ctx, c.Bin, nil, "list", "table", tableName); err != nil {
		if isMissingTable(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 nft 表失败: %w", err)
	}
	return true, nil
}

// Counters reads named counters from the NiuNexus table.
func (c *Client) Counters(ctx context.Context) (map[string]Counter, error) {
	out, err := c.Runner.Run(ctx, c.Bin, nil, "-j", "list", "table", tableName)
	if err != nil {
		if isMissingTable(err) {
			return map[string]Counter{}, nil
		}
		return nil, err
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("解析 nft 计数器失败: %w", err)
	}
	counters := map[string]Counter{}
	for _, element := range doc.Nftables {
		raw, ok := element["counter"]
		if !ok {
			continue
		}
		var item struct {
			Name    string `json:"name"`
			Packets uint64 `json:"packets"`
			Bytes   uint64 `json:"bytes"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		if item.Name != "" {
			counters[item.Name] = Counter{Packets: item.Packets, Bytes: item.Bytes}
		}
	}
	return counters, nil
}

// CounterName converts a rule ID into a safe nft counter identifier.
func CounterName(id string) string {
	var b strings.Builder
	b.WriteString("pf_")
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Render produces the table definition. The boolean is false when there is
// nothing to install.
func Render(cfg *config.Config, opts ...RenderOptions) (string, bool) {
	if cfg == nil {
		return "", false
	}
	var options RenderOptions
	if len(opts) > 0 {
		options = opts[0]
	}
	enabled := make([]config.PortForward, 0, len(cfg.PortForwards))
	for _, rule := range cfg.PortForwards {
		if rule.Enabled {
			enabled = append(enabled, rule)
		}
	}
	natActive := cfg.NAT.Enabled && len(cfg.NAT.Interfaces) > 0
	proxyTransparent := cfg.Proxy.Enabled && cfg.Proxy.Transparent.Enabled &&
		cfg.Proxy.TransparentPort > 0 && len(cfg.Proxy.Transparent.SourceCIDRs) > 0
	if len(cfg.PortForwards) == 0 && !natActive && !proxyTransparent {
		return "", false
	}

	var b strings.Builder
	b.WriteString("table " + tableName + " {\n")
	for _, rule := range cfg.PortForwards {
		fmt.Fprintf(&b, "  counter %s {\n    comment %q\n  }\n", CounterName(rule.ID), "pf:"+rule.ID)
	}

	b.WriteString("  chain prerouting {\n")
	b.WriteString("    type nat hook prerouting priority dstnat; policy accept;\n")
	for _, rule := range enabled {
		renderForwardRule(&b, rule)
	}
	if proxyTransparent {
		renderProxyRedirects(&b, cfg, options)
	}
	b.WriteString("  }\n")

	b.WriteString("  chain postrouting {\n")
	b.WriteString("    type nat hook postrouting priority srcnat; policy accept;\n")
	if natActive {
		for _, wan := range cfg.NAT.Interfaces {
			fmt.Fprintf(&b, "    oifname %q masquerade\n", wan)
		}
	}
	if anyHairpin(enabled) {
		fmt.Fprintf(&b, "    ct mark %s masquerade\n", markHairpin)
	}
	b.WriteString("  }\n")

	b.WriteString("  chain forward {\n")
	b.WriteString("    type filter hook forward priority filter; policy accept;\n")
	b.WriteString("    ct state established,related accept\n")
	if len(enabled) > 0 {
		fmt.Fprintf(&b, "    ct mark %s accept\n", markForward)
		fmt.Fprintf(&b, "    ct mark %s accept\n", markHairpin)
	}
	if natActive {
		for _, wan := range cfg.NAT.Interfaces {
			fmt.Fprintf(&b, "    oifname %q ct state new,established,related accept\n", wan)
		}
	}
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String(), true
}

func renderForwardRule(b *strings.Builder, rule config.PortForward) {
	extStart, extEnd, err := portRange(rule.External)
	if err != nil {
		return
	}
	dstStart, dstEnd, err := portRange(rule.DestPort)
	if err != nil {
		return
	}
	protocol := strings.ToLower(rule.Protocol)
	protocols := []string{protocol}
	if protocol == "both" {
		protocols = []string{"tcp", "udp"}
	}
	mark := markForward
	if rule.Hairpin {
		mark = markHairpin
	}
	counter := CounterName(rule.ID)
	for _, proto := range protocols {
		prefix := ""
		if rule.WAN != "" {
			prefix = fmt.Sprintf("iifname %q ", rule.WAN)
		}
		fmt.Fprintf(b, "    %s%s dport %s ct mark set %s counter name %s dnat ip to %s:%s\n",
			prefix,
			proto,
			formatRange(extStart, extEnd),
			mark,
			counter,
			rule.DestIP,
			formatRange(dstStart, dstEnd),
		)
	}
}

func renderProxyRedirects(b *strings.Builder, cfg *config.Config, options RenderOptions) {
	exclusions := []string{"127.0.0.0/8", "169.254.0.0/16"}
	for _, address := range options.LocalAddresses {
		exclusions = append(exclusions, address)
	}
	for _, value := range cfg.Proxy.Bypass {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if ip, _, err := net.ParseCIDR(value); err == nil {
			if ip.To4() != nil {
				exclusions = append(exclusions, value)
			}
		} else if ip := net.ParseIP(value); ip != nil && ip.To4() != nil {
			exclusions = append(exclusions, value)
		}
	}
	exclusions = dedupeStrings(exclusions)
	ports := map[int]bool{cfg.Proxy.TransparentPort: true}
	if cfg.Proxy.ListenPort > 0 {
		ports[cfg.Proxy.ListenPort] = true
	}
	portValues := make([]int, 0, len(ports))
	for port := range ports {
		portValues = append(portValues, port)
	}
	sort.Ints(portValues)
	interfaces := cfg.Proxy.Transparent.Interfaces
	if len(interfaces) == 0 {
		interfaces = []string{""}
	}
	for _, iface := range interfaces {
		for _, source := range cfg.Proxy.Transparent.SourceCIDRs {
			clauses := make([]string, 0, 6+len(exclusions)+len(portValues))
			if iface != "" {
				clauses = append(clauses, fmt.Sprintf("iifname %q", iface))
			}
			clauses = append(clauses, "ip saddr "+source)
			for _, port := range portValues {
				clauses = append(clauses, fmt.Sprintf("tcp dport != %d", port))
			}
			for _, exclusion := range exclusions {
				clauses = append(clauses, "ip daddr != "+exclusion)
			}
			clauses = append(clauses, fmt.Sprintf("redirect to :%d", cfg.Proxy.TransparentPort))
			fmt.Fprintf(b, "    %s\n", strings.Join(clauses, " "))
		}
	}
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func anyHairpin(rules []config.PortForward) bool {
	for _, rule := range rules {
		if rule.Hairpin {
			return true
		}
	}
	return false
}

func formatRange(start, end int) string {
	if start == end {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func portRange(value string) (int, int, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "-")
	if len(parts) == 0 || len(parts) > 2 {
		return 0, 0, errors.New("invalid port")
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, err
		}
	}
	if start < 1 || end > 65535 || start > end {
		return 0, 0, errors.New("invalid port range")
	}
	return start, end, nil
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "No such file or directory") ||
		strings.Contains(message, "does not exist") ||
		strings.Contains(message, "not found")
}
