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

type virtualLinkUndo struct {
	Action    string              `json:"action"`
	MasterID  string              `json:"masterId,omitempty"`
	MemberIDs []string            `json:"memberIds,omitempty"`
	Link      *config.VirtualLink `json:"link,omitempty"`
}

// CreateVirtualLink creates a NiuNexus-owned bridge, VLAN or bond.
func (c *Controller) CreateVirtualLink(ctx context.Context, req VirtualLinkRequest, actor string) (*changes.Result, error) {
	linkType := strings.ToLower(strings.TrimSpace(req.Type))
	name := strings.TrimSpace(req.Name)
	parent := strings.TrimSpace(req.Parent)
	members := cleanMembers(req.Members)

	if linkType == "vlan" {
		if parent == "" {
			return nil, errors.New("VLAN 必须指定父接口")
		}
		if req.VLANID < 1 || req.VLANID > 4094 {
			return nil, errors.New("VLAN ID 必须在 1 到 4094 之间")
		}
		expected := fmt.Sprintf("%s.%d", parent, req.VLANID)
		if name == "" {
			name = expected
		}
		if name != expected {
			return nil, fmt.Errorf("VLAN 接口名必须是 %s", expected)
		}
	}
	if name == "" {
		return nil, errors.New("必须填写接口名")
	}

	ipv4Mode := req.IPv4Mode
	if ipv4Mode == "" {
		if linkType == "bridge" && len(members) == 0 {
			ipv4Mode = "disabled"
		} else {
			ipv4Mode = "auto"
		}
	}
	ipv6Mode := req.IPv6Mode
	if ipv6Mode == "" {
		ipv6Mode = "auto"
	}

	link := config.VirtualLink{
		ID:       newPrefixedID("vl-"),
		Type:     linkType,
		Name:     name,
		Parent:   parent,
		Members:  members,
		VLANID:   req.VLANID,
		STP:      req.STP,
		Mode:     req.Mode,
		IPv4Mode: ipv4Mode,
		IPv4Addr: req.IPv4Addr,
		IPv4GW:   req.IPv4GW,
		DNS:      req.DNS,
		IPv6Mode: ipv6Mode,
		IPv6Addr: req.IPv6Addr,
		IPv6GW:   req.IPv6GW,
		IPv6DNS:  req.IPv6DNS,
		MTU:      req.MTU,
	}
	if err := config.ValidateVirtualLink(link); err != nil {
		return nil, err
	}
	if _, err := netlink.LinkByName(name); err == nil {
		return nil, fmt.Errorf("接口 %s 已存在", name)
	}
	if linkType == "vlan" {
		if _, err := netlink.LinkByName(parent); err != nil {
			return nil, fmt.Errorf("父接口 %s 不存在", parent)
		}
	}
	for _, member := range members {
		if _, err := netlink.LinkByName(member); err != nil {
			return nil, fmt.Errorf("成员网口 %s 不存在", member)
		}
	}

	cfg := c.store.Config()
	for _, existing := range cfg.VirtualLinks {
		if existing.Name == name {
			return nil, fmt.Errorf("虚拟链路名称 %s 已被占用", name)
		}
		for _, existingMember := range existing.Members {
			for _, member := range members {
				if existingMember == member {
					return nil, fmt.Errorf("网口 %s 已经是虚拟链路 %s 的成员", member, existing.Name)
				}
			}
		}
	}
	profileName := "niunexus-" + name
	if _, err := c.nm.ByName(ctx, profileName); err == nil {
		return nil, fmt.Errorf("NetworkManager 中已存在连接 %s，请先清理", profileName)
	}

	protected := c.protectedSet()
	risky := false
	for _, member := range members {
		if protected[member] {
			risky = true
		}
	}
	preview := fmt.Sprintf("创建 %s %s（成员: %s）", linkType, name, strings.Join(members, ","))
	if len(members) == 0 {
		preview = fmt.Sprintf("创建 %s %s", linkType, name)
	}

	var masterUUID string
	var memberIDs []string

	apply := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		link.MasterID = ""
		link.MemberIDs = nil
		if err := c.createLinkProfiles(opCtx, &link, &masterUUID, &memberIDs); err != nil {
			return err
		}
		if _, err := c.store.Update(func(cfg *config.Config) error {
			upsertLink(cfg, link)
			return nil
		}); err != nil {
			return err
		}
		if err := c.nm.Up(opCtx, masterUUID); err != nil {
			return fmt.Errorf("激活 %s 失败: %w", name, err)
		}
		return c.verifyLink(opCtx, link)
	}

	rollback := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return c.deleteLinkProfiles(ctx, masterUUID, memberIDs)
	}

	undoFn := func() (*changes.Undo, error) {
		data, err := json.Marshal(virtualLinkUndo{
			Action:    "create",
			MasterID:  masterUUID,
			MemberIDs: memberIDs,
		})
		if err != nil {
			return nil, err
		}
		return &changes.Undo{Kind: "virtuallink", Data: data}, nil
	}

	return c.changes.Run(ctx, changes.Spec{
		Action:   "link.create",
		Target:   name,
		Actor:    actor,
		Preview:  preview,
		Risky:    risky,
		Timeout:  c.changeTimeout(),
		Apply:    apply,
		Rollback: rollback,
		UndoFn:   undoFn,
	})
}

// DeleteVirtualLink removes a NiuNexus-owned link and its slave profiles.
func (c *Controller) DeleteVirtualLink(ctx context.Context, id, actor string) (*changes.Result, error) {
	cfg := c.store.Config()
	link := findLinkByID(cfg, id)
	if link == nil {
		return nil, fmt.Errorf("虚拟链路不存在: %s", id)
	}
	protected := c.protectedSet()
	risky := protected[link.Name]
	for _, member := range link.Members {
		if protected[member] {
			risky = true
		}
	}

	apply := func() error {
		opCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if link.MasterID != "" {
			_ = c.nm.Down(opCtx, link.MasterID)
		}
		var problems []string
		for _, memberID := range link.MemberIDs {
			if err := c.nm.Delete(opCtx, memberID); err != nil {
				problems = append(problems, "删除成员连接失败: "+err.Error())
			}
		}
		if link.MasterID != "" {
			if err := c.nm.Delete(opCtx, link.MasterID); err != nil {
				problems = append(problems, "删除主连接失败: "+err.Error())
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "；"))
		}
		if _, err := c.store.Update(func(cfg *config.Config) error {
			removeLink(cfg, link.ID)
			return nil
		}); err != nil {
			return err
		}
		return c.verifyLinkGone(opCtx, link.Name)
	}

	rollback := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return c.recreateLink(ctx, *link)
	}

	undoFn := func() (*changes.Undo, error) {
		data, err := json.Marshal(virtualLinkUndo{Action: "delete", Link: link})
		if err != nil {
			return nil, err
		}
		return &changes.Undo{Kind: "virtuallink", Data: data}, nil
	}

	return c.changes.Run(ctx, changes.Spec{
		Action:   "link.delete",
		Target:   link.Name,
		Actor:    actor,
		Preview:  fmt.Sprintf("删除 %s %s 及其成员连接", link.Type, link.Name),
		Risky:    risky,
		Timeout:  c.changeTimeout(),
		Apply:    apply,
		Rollback: rollback,
		UndoFn:   undoFn,
	})
}

func (c *Controller) createLinkProfiles(ctx context.Context, link *config.VirtualLink, masterUUID *string, memberIDs *[]string) error {
	profileName := "niunexus-" + link.Name
	if stale, err := c.nm.ByName(ctx, profileName); err == nil {
		if !strings.HasPrefix(stale.Name, "niunexus-") {
			return fmt.Errorf("连接名 %q 已被非 NiuNexus 配置占用", profileName)
		}
		_ = c.nm.Delete(ctx, stale.UUID)
	}

	var master string
	var err error
	switch link.Type {
	case "bridge":
		master, err = c.nm.AddBridge(ctx, profileName, link.Name)
	case "vlan":
		master, err = c.nm.AddVlan(ctx, profileName, link.Name, link.Parent, link.VLANID)
	case "bond":
		master, err = c.nm.AddBond(ctx, profileName, link.Name)
	default:
		return fmt.Errorf("不支持的虚拟链路类型: %s", link.Type)
	}
	if err != nil {
		return fmt.Errorf("创建 %s 主连接失败: %w", link.Type, err)
	}
	*masterUUID = master
	link.MasterID = master

	props := []string{
		"connection.autoconnect", "yes",
		"connection.autoconnect-priority", "200",
	}
	switch link.Type {
	case "bridge":
		props = append(props, "bridge.stp", boolString(link.STP))
	case "bond":
		mode := link.Mode
		if mode == "" {
			mode = "active-backup"
		}
		props = append(props, "bond.options", "mode="+mode+",miimon=100")
	}
	ipProps, err := buildIPProperties(link.IPv4Mode, link.IPv4Addr, link.IPv4GW, link.DNS, link.IPv6Mode, link.IPv6Addr, link.IPv6GW, link.IPv6DNS)
	if err != nil {
		return err
	}
	props = append(props, ipProps...)
	if link.MTU > 0 {
		property := nm.MTUProperty(link.Type)
		if property == "" {
			return fmt.Errorf("链路类型 %s 不支持设置 MTU", link.Type)
		}
		props = append(props, property, intString(link.MTU))
	}
	if err := c.nm.Modify(ctx, master, props...); err != nil {
		return fmt.Errorf("配置 %s 主连接失败: %w", link.Type, err)
	}

	for _, member := range link.Members {
		slaveName := fmt.Sprintf("niunexus-%s-port-%s", link.Name, member)
		if stale, err := c.nm.ByName(ctx, slaveName); err == nil {
			if !strings.HasPrefix(stale.Name, "niunexus-") {
				return fmt.Errorf("连接名 %q 已被非 NiuNexus 配置占用", slaveName)
			}
			_ = c.nm.Delete(ctx, stale.UUID)
		}
		slaveID, err := c.nm.AddSlave(ctx, slaveName, member, master)
		if err != nil {
			return fmt.Errorf("创建成员连接 %s 失败: %w", member, err)
		}
		*memberIDs = append(*memberIDs, slaveID)
		if err := c.nm.Modify(ctx, slaveID,
			"connection.autoconnect", "yes",
			"connection.autoconnect-priority", "200",
		); err != nil {
			return fmt.Errorf("配置成员连接 %s 失败: %w", member, err)
		}
	}
	link.MemberIDs = append([]string(nil), (*memberIDs)...)
	return nil
}

func (c *Controller) deleteLinkProfiles(ctx context.Context, masterID string, memberIDs []string) error {
	var problems []string
	for _, memberID := range memberIDs {
		if err := c.nm.Delete(ctx, memberID); err != nil {
			problems = append(problems, "删除成员连接失败: "+err.Error())
		}
	}
	if masterID != "" {
		_ = c.nm.Down(ctx, masterID)
		if err := c.nm.Delete(ctx, masterID); err != nil {
			problems = append(problems, "删除主连接失败: "+err.Error())
		}
	}
	return joinErrors(problems)
}

func (c *Controller) verifyLink(ctx context.Context, link config.VirtualLink) error {
	return c.verifyInterface(ctx, link.Name, link.IPv4Mode, link.IPv4Addr, link.IPv6Mode, link.IPv6Addr)
}

func (c *Controller) recreateLink(ctx context.Context, link config.VirtualLink) error {
	link.MasterID = ""
	link.MemberIDs = nil
	var master string
	var memberIDs []string
	if err := c.createLinkProfiles(ctx, &link, &master, &memberIDs); err != nil {
		_ = c.deleteLinkProfiles(ctx, master, memberIDs)
		return err
	}
	if _, err := c.store.Update(func(cfg *config.Config) error {
		upsertLink(cfg, link)
		return nil
	}); err != nil {
		_ = c.deleteLinkProfiles(ctx, master, memberIDs)
		return err
	}
	if err := c.nm.Up(ctx, master); err != nil {
		_ = c.deleteLinkProfiles(ctx, master, memberIDs)
		return err
	}
	if err := c.verifyLink(ctx, link); err != nil {
		_ = c.deleteLinkProfiles(ctx, master, memberIDs)
		return err
	}
	return nil
}

func (c *Controller) undoVirtualLink(data json.RawMessage) error {
	var undo virtualLinkUndo
	if err := json.Unmarshal(data, &undo); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	switch undo.Action {
	case "create":
		return c.deleteLinkProfiles(ctx, undo.MasterID, undo.MemberIDs)
	case "delete":
		if undo.Link == nil {
			return errors.New("删除回滚缺少链路配置")
		}
		return c.recreateLink(ctx, *undo.Link)
	default:
		return fmt.Errorf("未知的虚拟链路回滚动作: %q", undo.Action)
	}
}

func cleanMembers(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// RegisterRecovery installs crash-recovery handlers for interface and link
// changes. Call once during core startup.
func (c *Controller) RegisterRecovery() {
	c.changes.RegisterUndo("interface", c.undoInterface)
	c.changes.RegisterUndo("virtuallink", c.undoVirtualLink)
}
