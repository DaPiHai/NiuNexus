package netctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vishvananda/netlink"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/nm"
)

type interfaceUndo struct {
	Ifname            string `json:"ifname"`
	ManagedID         string `json:"managedId"`
	OriginalID        string `json:"originalId"`
	OriginalWasActive bool   `json:"originalWasActive"`
}

// ApplyInterface clones or reuses a NiuNexus-owned profile, applies the
// requested address settings and activates it. A protected interface is left
// pending until the administrator confirms.
func (c *Controller) ApplyInterface(ctx context.Context, ifname string, req InterfaceRequest, actor string) (*changes.Result, error) {
	if strings.TrimSpace(req.IPv4Mode) == "" {
		return nil, errors.New("必须选择 IPv4 模式")
	}
	if strings.TrimSpace(req.IPv6Mode) == "" {
		req.IPv6Mode = "auto"
	}
	item := config.InterfaceConfig{
		Name:     ifname,
		Managed:  true,
		IPv4Mode: req.IPv4Mode,
		IPv4Addr: req.IPv4Addr,
		IPv4GW:   req.IPv4GW,
		DNS:      req.DNS,
		IPv6Mode: req.IPv6Mode,
		IPv6Addr: req.IPv6Addr,
		IPv6GW:   req.IPv6GW,
		IPv6DNS:  req.IPv6DNS,
		MTU:      req.MTU,
	}
	if err := config.ValidateInterface(item); err != nil {
		return nil, err
	}
	if _, err := netlink.LinkByName(ifname); err != nil {
		return nil, fmt.Errorf("网口 %s 不存在", ifname)
	}

	cfg := c.store.Config()
	for _, link := range cfg.VirtualLinks {
		if link.Name == ifname {
			return nil, fmt.Errorf("虚拟链路 %s 的地址请在创建时配置；修改请删除后重建", ifname)
		}
		for _, member := range link.Members {
			if member == ifname {
				return nil, fmt.Errorf("网口 %s 是虚拟链路 %s 的成员，请先在网桥/VLAN 页面删除该链路", ifname, link.Name)
			}
		}
	}

	existing := findInterface(cfg, ifname)
	active, err := c.nm.ActiveForDevice(ctx, ifname)
	if err != nil && !errors.Is(err, nm.ErrNoActiveConnection) {
		return nil, err
	}
	var activeConn *nm.Connection
	if err == nil {
		activeConn = active
	}

	risky := c.isProtected(ifname)
	preview := fmt.Sprintf("%s: IPv4=%s %s 网关=%s DNS=%s MTU=%d", ifname, req.IPv4Mode, req.IPv4Addr, req.IPv4GW, strings.Join(req.DNS, ","), req.MTU)

	var managedUUID, originalID, connectionType string
	var originalWasActive bool

	apply := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		managedExists := false
		if existing != nil && existing.ManagedID != "" {
			if _, err := c.connectionByUUID(opCtx, existing.ManagedID); err == nil {
				managedExists = true
			}
		}

		switch {
		case managedExists:
			managedUUID = existing.ManagedID
			originalID = existing.OriginalID
			originalWasActive = existing.OriginalWasActive
			connectionType = existing.ConnectionType
			if connectionType == "" {
				conn, err := c.connectionByUUID(opCtx, managedUUID)
				if err != nil {
					return fmt.Errorf("读取管理连接失败: %w", err)
				}
				connectionType = conn.Type
			}
		case activeConn != nil:
			connectionType = activeConn.Type
			if strings.HasPrefix(activeConn.Name, "niunexus-") {
				return fmt.Errorf("检测到未跟踪的 NiuNexus 连接 %q，请先恢复配置或手动清理", activeConn.Name)
			}
			originalID = activeConn.UUID
			originalWasActive = true
			profileName := "niunexus-" + ifname
			if stale, err := c.nm.ByName(opCtx, profileName); err == nil {
				if !strings.HasPrefix(stale.Name, "niunexus-") {
					return fmt.Errorf("连接名 %q 已被非 NiuNexus 配置占用", profileName)
				}
				if err := c.nm.Delete(opCtx, stale.UUID); err != nil {
					return fmt.Errorf("清理旧的管理连接失败: %w", err)
				}
			}
			managedUUID, err = c.nm.Clone(opCtx, activeConn.UUID, profileName)
			if err != nil {
				return fmt.Errorf("克隆原连接失败: %w", err)
			}
		default:
			connectionType = "802-3-ethernet"
			profileName := "niunexus-" + ifname
			if existing != nil {
				originalID = existing.OriginalID
				originalWasActive = existing.OriginalWasActive
			}
			if stale, err := c.nm.ByName(opCtx, profileName); err == nil {
				if !strings.HasPrefix(stale.Name, "niunexus-") {
					return fmt.Errorf("连接名 %q 已被非 NiuNexus 配置占用", profileName)
				}
				if err := c.nm.Delete(opCtx, stale.UUID); err != nil {
					return fmt.Errorf("清理旧的管理连接失败: %w", err)
				}
			}
			managedUUID, err = c.nm.AddEthernet(opCtx, profileName, ifname)
			if err != nil {
				return fmt.Errorf("创建管理连接失败: %w", err)
			}
		}

		props := []string{
			"connection.autoconnect", "yes",
			"connection.autoconnect-priority", "200",
		}
		ipProps, err := buildIPProperties(req.IPv4Mode, req.IPv4Addr, req.IPv4GW, req.DNS, req.IPv6Mode, req.IPv6Addr, req.IPv6GW, req.IPv6DNS)
		if err != nil {
			return err
		}
		props = append(props, ipProps...)
		if req.MTU > 0 {
			property := nm.MTUProperty(connectionType)
			if property == "" {
				return fmt.Errorf("连接类型 %s 不支持设置 MTU", connectionType)
			}
			props = append(props, property, intString(req.MTU))
		}
		if err := c.nm.Modify(opCtx, managedUUID, props...); err != nil {
			return fmt.Errorf("修改管理连接失败: %w", err)
		}

		item.OriginalID = originalID
		item.ManagedID = managedUUID
		item.OriginalWasActive = originalWasActive
		item.ConnectionType = connectionType
		if _, err := c.store.Update(func(cfg *config.Config) error {
			upsertInterface(cfg, item)
			return nil
		}); err != nil {
			return err
		}

		if err := c.nm.Up(opCtx, managedUUID); err != nil {
			return fmt.Errorf("激活管理连接失败: %w", err)
		}
		return c.verifyInterface(opCtx, ifname, req.IPv4Mode, req.IPv4Addr, req.IPv6Mode, req.IPv6Addr)
	}

	rollback := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var problems []string
		if managedUUID != "" {
			_ = c.nm.Down(ctx, managedUUID)
			if err := c.nm.Delete(ctx, managedUUID); err != nil {
				problems = append(problems, "删除管理连接失败: "+err.Error())
			}
		}
		if originalID != "" && originalWasActive {
			if err := c.nm.Up(ctx, originalID); err != nil {
				problems = append(problems, "恢复原连接失败: "+err.Error())
			}
		}
		return joinErrors(problems)
	}

	undoFn := func() (*changes.Undo, error) {
		data, err := json.Marshal(interfaceUndo{
			Ifname:            ifname,
			ManagedID:         managedUUID,
			OriginalID:        originalID,
			OriginalWasActive: originalWasActive,
		})
		if err != nil {
			return nil, err
		}
		return &changes.Undo{Kind: "interface", Data: data}, nil
	}

	return c.changes.Run(ctx, changes.Spec{
		Action:   "interface.apply",
		Target:   ifname,
		Actor:    actor,
		Preview:  preview,
		Risky:    risky,
		Timeout:  c.changeTimeout(),
		Apply:    apply,
		Rollback: rollback,
		UndoFn:   undoFn,
	})
}

// RevertInterface removes the managed profile and reactivates the original
// profile captured when the interface was first managed.
func (c *Controller) RevertInterface(ctx context.Context, ifname, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	existing := findInterface(cfg, ifname)
	if existing == nil || existing.ManagedID == "" {
		return nil, fmt.Errorf("网口 %s 没有被 NiuNexus 管理", ifname)
	}

	apply := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if existing.OriginalID != "" {
			if err := c.nm.Up(opCtx, existing.OriginalID); err != nil {
				return fmt.Errorf("启用原连接失败: %w", err)
			}
			if err := c.verifyLinkUp(opCtx, ifname, 10*time.Second); err != nil {
				return err
			}
		}
		if err := c.nm.Delete(opCtx, existing.ManagedID); err != nil {
			return fmt.Errorf("删除管理连接失败: %w", err)
		}
		_, err := c.store.Update(func(cfg *config.Config) error {
			removeInterface(cfg, ifname)
			return nil
		})
		return err
	}
	rollback := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.nm.Up(ctx, existing.ManagedID); err != nil {
			return fmt.Errorf("恢复管理连接失败: %w", err)
		}
		return nil
	}

	return c.changes.Run(ctx, changes.Spec{
		Action:   "interface.revert",
		Target:   ifname,
		Actor:    actor,
		Preview:  fmt.Sprintf("恢复 %s 的原 NetworkManager 配置并删除 NiuNexus 管理连接", ifname),
		Risky:    false,
		Apply:    apply,
		Rollback: rollback,
	})
}

func (c *Controller) connectionByUUID(ctx context.Context, uuid string) (*nm.Connection, error) {
	list, err := c.nm.List(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].UUID == uuid {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("连接 %s 不存在", uuid)
}

func (c *Controller) changeTimeout() time.Duration {
	seconds := c.store.Config().Settings.ConfirmTimeoutSec
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}

func (c *Controller) undoInterface(data json.RawMessage) error {
	var undo interfaceUndo
	if err := json.Unmarshal(data, &undo); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var problems []string
	if undo.ManagedID != "" {
		_ = c.nm.Down(ctx, undo.ManagedID)
		if err := c.nm.Delete(ctx, undo.ManagedID); err != nil {
			problems = append(problems, "删除管理连接失败: "+err.Error())
		}
	}
	if undo.OriginalID != "" && undo.OriginalWasActive {
		if err := c.nm.Up(ctx, undo.OriginalID); err != nil {
			problems = append(problems, "恢复原连接失败: "+err.Error())
		}
	}
	return joinErrors(problems)
}
