// Package portforward manages DNAT rules and the NiuNexus nft table.
package portforward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vishvananda/netlink"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/netinfo"
	"niunexus/internal/nft"
	"niunexus/internal/sysctl"
)

// Request is the editable form of a port-forward rule.
type Request struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol"`
	WAN      string `json:"wan"`
	External string `json:"externalPort"`
	DestIP   string `json:"destIp"`
	DestPort string `json:"destPort"`
	Hairpin  bool   `json:"hairpin"`
}

// ToggleRequest flips the enabled flag.
type ToggleRequest struct {
	Enabled bool `json:"enabled"`
}

// RuleDetail adds live counters to a rule.
type RuleDetail struct {
	config.PortForward
	Counter nft.Counter `json:"counter"`
}

// Snapshot is the payload for the port-forward page.
type Snapshot struct {
	Rules    []RuleDetail     `json:"rules"`
	NAT      config.NATConfig `json:"nat"`
	Warnings []string         `json:"warnings,omitempty"`
}

// Controller applies rules through the shared changes manager.
type Controller struct {
	nft     *nft.Client
	sysctl  *sysctl.Forwarding
	store   *config.Store
	changes *changes.Manager
}

// New creates a controller.
func New(client *nft.Client, forwarding *sysctl.Forwarding, store *config.Store, manager *changes.Manager) *Controller {
	return &Controller{nft: client, sysctl: forwarding, store: store, changes: manager}
}

// List returns rules, NAT settings and counters.
func (c *Controller) List(ctx context.Context) (*Snapshot, error) {
	cfg := c.store.Config()
	counters, err := c.nft.Counters(ctx)
	warnings := []string{}
	if err != nil {
		warnings = append(warnings, "读取 nft 计数器失败（应用可能不是 root）: "+err.Error())
		counters = map[string]nft.Counter{}
	}
	rules := make([]RuleDetail, 0, len(cfg.PortForwards))
	for _, rule := range cfg.PortForwards {
		rules = append(rules, RuleDetail{
			PortForward: rule,
			Counter:     counters[nft.CounterName(rule.ID)],
		})
	}
	return &Snapshot{Rules: rules, NAT: cfg.NAT, Warnings: warnings}, nil
}

// Add creates one rule.
func (c *Controller) Add(ctx context.Context, req Request, actor string) (*changes.Result, error) {
	item := config.PortForward{
		ID:       "pf-" + randomHex(6),
		Name:     strings.TrimSpace(req.Name),
		Enabled:  req.Enabled,
		Protocol: strings.ToLower(strings.TrimSpace(req.Protocol)),
		WAN:      strings.TrimSpace(req.WAN),
		External: strings.TrimSpace(req.External),
		DestIP:   strings.TrimSpace(req.DestIP),
		DestPort: strings.TrimSpace(req.DestPort),
		Hairpin:  req.Hairpin,
	}
	cfg := c.store.Config()
	if err := config.ValidatePortForwards(append(append([]config.PortForward{}, cfg.PortForwards...), item)); err != nil {
		return nil, err
	}
	if err := validateWAN(item.WAN); err != nil {
		return nil, err
	}
	return c.runChange(ctx, "portforward.create", item.Name, actor, preview(item), func(cfg *config.Config) error {
		cfg.PortForwards = append(cfg.PortForwards, item)
		return nil
	})
}

// Update replaces an existing rule.
func (c *Controller) Update(ctx context.Context, id string, req Request, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	existing := findRule(cfg, id)
	if existing == nil {
		return nil, fmt.Errorf("端口转发不存在: %s", id)
	}
	item := config.PortForward{
		ID:       id,
		Name:     strings.TrimSpace(req.Name),
		Enabled:  req.Enabled,
		Protocol: strings.ToLower(strings.TrimSpace(req.Protocol)),
		WAN:      strings.TrimSpace(req.WAN),
		External: strings.TrimSpace(req.External),
		DestIP:   strings.TrimSpace(req.DestIP),
		DestPort: strings.TrimSpace(req.DestPort),
		Hairpin:  req.Hairpin,
	}
	next := make([]config.PortForward, 0, len(cfg.PortForwards))
	for _, rule := range cfg.PortForwards {
		if rule.ID == id {
			next = append(next, item)
		} else {
			next = append(next, rule)
		}
	}
	if err := config.ValidatePortForwards(next); err != nil {
		return nil, err
	}
	if err := validateWAN(item.WAN); err != nil {
		return nil, err
	}
	return c.runChange(ctx, "portforward.update", item.Name, actor, preview(item), func(cfg *config.Config) error {
		for i := range cfg.PortForwards {
			if cfg.PortForwards[i].ID == id {
				cfg.PortForwards[i] = item
				return nil
			}
		}
		return fmt.Errorf("端口转发不存在: %s", id)
	})
}

// Delete removes a rule.
func (c *Controller) Delete(ctx context.Context, id, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	existing := findRule(cfg, id)
	if existing == nil {
		return nil, fmt.Errorf("端口转发不存在: %s", id)
	}
	return c.runChange(ctx, "portforward.delete", existing.Name, actor, "删除端口转发 "+existing.Name, func(cfg *config.Config) error {
		out := cfg.PortForwards[:0]
		for _, rule := range cfg.PortForwards {
			if rule.ID != id {
				out = append(out, rule)
			}
		}
		cfg.PortForwards = out
		return nil
	})
}

// Toggle enables or disables a rule.
func (c *Controller) Toggle(ctx context.Context, id string, enabled bool, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	existing := findRule(cfg, id)
	if existing == nil {
		return nil, fmt.Errorf("端口转发不存在: %s", id)
	}
	state := "禁用"
	if enabled {
		state = "启用"
	}
	return c.runChange(ctx, "portforward.toggle", existing.Name, actor, state+"端口转发 "+existing.Name, func(cfg *config.Config) error {
		for i := range cfg.PortForwards {
			if cfg.PortForwards[i].ID == id {
				cfg.PortForwards[i].Enabled = enabled
				return nil
			}
		}
		return fmt.Errorf("端口转发不存在: %s", id)
	})
}

// UpdateNAT replaces the masquerade configuration.
func (c *Controller) UpdateNAT(ctx context.Context, nat config.NATConfig, actor string) (*changes.Result, error) {
	if err := config.ValidateNAT(nat); err != nil {
		return nil, err
	}
	for _, wan := range nat.Interfaces {
		if err := validateWAN(wan); err != nil {
			return nil, err
		}
	}
	preview := "NAT 已禁用"
	if nat.Enabled {
		preview = "NAT 启用，WAN: " + strings.Join(nat.Interfaces, ", ")
	}
	return c.runChange(ctx, "nat.update", "nat", actor, preview, func(cfg *config.Config) error {
		cfg.NAT = nat
		return nil
	})
}

// ApplyCurrent reapplies the persisted table and forwarding sysctl.
func (c *Controller) ApplyCurrent(ctx context.Context) error {
	return c.apply(ctx)
}

// Cleanup removes the table and restores the saved forwarding value.
func (c *Controller) Cleanup(ctx context.Context) error {
	var problems []string
	if err := c.nft.Delete(ctx); err != nil {
		problems = append(problems, err.Error())
	}
	if err := c.sysctl.Restore(); err != nil {
		problems = append(problems, "恢复 IPv4 转发失败: "+err.Error())
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "；"))
	}
	return nil
}

// RegisterRecovery installs the crash-recovery handler.
func (c *Controller) RegisterRecovery() {
	c.changes.RegisterUndo("nft", func(json.RawMessage) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return c.ApplyCurrent(ctx)
	})
}

func (c *Controller) runChange(ctx context.Context, action, target, actor, previewText string, mutate func(*config.Config) error) (*changes.Result, error) {
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
		return &changes.Undo{Kind: "nft", Data: json.RawMessage(`{}`)}, nil
	}
	return c.changes.Run(ctx, changes.Spec{
		Action:   action,
		Target:   target,
		Actor:    actor,
		Preview:  previewText,
		Risky:    false,
		Apply:    apply,
		Rollback: rollback,
		UndoFn:   undoFn,
	})
}

func (c *Controller) apply(ctx context.Context) error {
	cfg := c.store.Config()
	if err := c.sysctl.Ensure(config.NeedsForwarding(cfg)); err != nil {
		return fmt.Errorf("设置 IPv4 转发失败: %w", err)
	}
	return c.nft.Apply(ctx, cfg, c.renderOptions())
}

func (c *Controller) renderOptions() nft.RenderOptions {
	return nft.RenderOptions{LocalAddresses: netinfo.LocalIPv4Addresses()}
}

func findRule(cfg *config.Config, id string) *config.PortForward {
	for i := range cfg.PortForwards {
		if cfg.PortForwards[i].ID == id {
			rule := cfg.PortForwards[i]
			return &rule
		}
	}
	return nil
}

func validateWAN(wan string) error {
	if wan == "" {
		return nil
	}
	if _, err := netlink.LinkByName(wan); err != nil {
		return fmt.Errorf("WAN 网口不存在: %s", wan)
	}
	return nil
}

func preview(rule config.PortForward) string {
	wan := rule.WAN
	if wan == "" {
		wan = "任意"
	}
	return fmt.Sprintf("%s: %s %s/%s → %s:%s%s", rule.Name, wan, rule.Protocol, rule.External, rule.DestIP, rule.DestPort, map[bool]string{true: " (hairpin)"}[rule.Hairpin])
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
