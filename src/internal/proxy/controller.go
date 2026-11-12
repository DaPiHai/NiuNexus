package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/netinfo"
	"niunexus/internal/nft"
	"niunexus/internal/secrets"
	"niunexus/internal/sysctl"
)

// Request updates the proxy configuration. Password fields are optional; an
// empty value keeps the stored secret.
type Request struct {
	config.ProxyConfig
	ProxyPassword         string `json:"proxyPassword,omitempty"`
	UpstreamPassword      string `json:"upstreamPassword,omitempty"`
	ClearProxyPassword    bool   `json:"clearProxyPassword,omitempty"`
	ClearUpstreamPassword bool   `json:"clearUpstreamPassword,omitempty"`
}

// Snapshot is the proxy page payload.
type Snapshot struct {
	Config              config.ProxyConfig `json:"config"`
	Status              Status             `json:"status"`
	HasProxyPassword    bool               `json:"hasProxyPassword"`
	HasUpstreamPassword bool               `json:"hasUpstreamPassword"`
	Warnings            []string           `json:"warnings,omitempty"`
}

// Controller coordinates the engine, secrets, nft redirects and sysctl.
type Controller struct {
	engine  *Engine
	store   *config.Store
	changes *changes.Manager
	secrets *secrets.Store
	nft     *nft.Client
	sysctl  *sysctl.Forwarding
}

// NewController creates a proxy controller.
func NewController(engine *Engine, store *config.Store, manager *changes.Manager, secretStore *secrets.Store, nftClient *nft.Client, forwarding *sysctl.Forwarding) *Controller {
	return &Controller{
		engine:  engine,
		store:   store,
		changes: manager,
		secrets: secretStore,
		nft:     nftClient,
		sysctl:  forwarding,
	}
}

// Status returns the current configuration and runtime counters.
func (c *Controller) Status(ctx context.Context) (*Snapshot, error) {
	_ = ctx
	cfg := c.store.Config().Proxy
	stored, err := c.secrets.Load()
	warnings := []string{}
	if err != nil {
		warnings = append(warnings, "读取代理密钥失败: "+err.Error())
	}
	if cfg.Enabled && !cfg.RequireAuth && (cfg.Listen == "" || cfg.Listen == "0.0.0.0" || cfg.Listen == "::") {
		warnings = append(warnings, "代理未启用认证且监听所有地址，请确认局域网访问风险")
	}
	return &Snapshot{
		Config:              cfg,
		Status:              c.engine.Status(),
		HasProxyPassword:    stored.ProxyPassword != "",
		HasUpstreamPassword: stored.UpstreamPassword != "",
		Warnings:            warnings,
	}, nil
}

// Update validates, persists and applies a new proxy configuration.
func (c *Controller) Update(ctx context.Context, req Request, actor string) (*changes.Result, error) {
	next := req.ProxyConfig
	if err := config.ValidateProxy(next); err != nil {
		return nil, err
	}
	current, err := c.secrets.Load()
	if err != nil {
		return nil, err
	}
	if req.ClearProxyPassword {
		current.ProxyPassword = ""
	} else if req.ProxyPassword != "" {
		current.ProxyPassword = req.ProxyPassword
	}
	if req.ClearUpstreamPassword {
		current.UpstreamPassword = ""
	} else if req.UpstreamPassword != "" {
		current.UpstreamPassword = req.UpstreamPassword
	}
	if !next.RequireAuth {
		current.ProxyPassword = ""
	}
	if next.Upstream.Type == "" || next.Upstream.Type == "none" {
		current.UpstreamPassword = ""
	}
	if next.RequireAuth && current.ProxyPassword == "" {
		return nil, errors.New("启用代理认证时必须设置密码")
	}
	old := c.store.Config().Proxy
	risky := next.Transparent.Enabled || old.Transparent.Enabled

	apply := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := c.store.Update(func(cfg *config.Config) error {
			cfg.Proxy = next
			return nil
		}); err != nil {
			return err
		}
		if err := c.secrets.Save(current); err != nil {
			return err
		}
		return c.applyFromStore(opCtx)
	}
	rollback := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return c.applyFromStore(opCtx)
	}
	undoFn := func() (*changes.Undo, error) {
		return &changes.Undo{Kind: "proxy", Data: json.RawMessage(`{}`)}, nil
	}
	return c.changes.Run(ctx, changes.Spec{
		Action:   "proxy.update",
		Target:   "proxy",
		Actor:    actor,
		Preview:  proxyPreview(next),
		Risky:    risky,
		Timeout:  c.changeTimeout(),
		Apply:    apply,
		Rollback: rollback,
		UndoFn:   undoFn,
	})
}

// Restart reapplies the current configuration.
func (c *Controller) Restart(ctx context.Context) error {
	return c.applyFromStore(ctx)
}

// ApplyCurrent reapplies the persisted configuration during startup/recovery.
func (c *Controller) ApplyCurrent(ctx context.Context) error {
	return c.applyFromStore(ctx)
}

// Cleanup stops listeners. The nft table is owned by the port-forward
// controller's cleanup.
func (c *Controller) Cleanup(ctx context.Context) error {
	_ = ctx
	c.engine.Stop()
	return nil
}

// RegisterRecovery installs the crash-recovery handler.
func (c *Controller) RegisterRecovery() {
	c.changes.RegisterUndo("proxy", func(json.RawMessage) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return c.ApplyCurrent(ctx)
	})
}

func (c *Controller) applyFromStore(ctx context.Context) error {
	cfg := c.store.Config()
	stored, err := c.secrets.Load()
	if err != nil {
		return err
	}
	if cfg.Proxy.Enabled {
		if err := c.engine.Start(cfg.Proxy, Secret{
			ProxyPassword:    stored.ProxyPassword,
			UpstreamPassword: stored.UpstreamPassword,
		}); err != nil {
			return err
		}
	} else {
		c.engine.Stop()
	}
	if err := c.sysctl.Ensure(config.NeedsForwarding(cfg)); err != nil {
		return fmt.Errorf("设置 IPv4 转发失败: %w", err)
	}
	return c.nft.Apply(ctx, cfg, nft.RenderOptions{LocalAddresses: netinfo.LocalIPv4Addresses()})
}

func (c *Controller) changeTimeout() time.Duration {
	seconds := c.store.Config().Settings.ConfirmTimeoutSec
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}

func proxyPreview(cfg config.ProxyConfig) string {
	if !cfg.Enabled {
		return "停用代理"
	}
	parts := []string{fmt.Sprintf("正向代理 %s:%d", defaultListen(cfg.Listen), cfg.ListenPort)}
	if cfg.Transparent.Enabled {
		parts = append(parts, fmt.Sprintf("透明代理 :%d（来源 %s）", cfg.TransparentPort, strings.Join(cfg.Transparent.SourceCIDRs, ",")))
	}
	if cfg.Upstream.Type != "" && cfg.Upstream.Type != "none" {
		parts = append(parts, fmt.Sprintf("上游 %s %s", cfg.Upstream.Type, cfg.Upstream.Address))
	}
	return strings.Join(parts, "；")
}

func defaultListen(value string) string {
	if strings.TrimSpace(value) == "" {
		return "0.0.0.0"
	}
	return value
}
