// Package netctl applies interface and virtual-link changes through
// NetworkManager and verifies them against the kernel. It never edits a
// system-owned profile in place: physical interfaces are cloned to a profile
// named niunexus-<ifname>, and virtual links are entirely NiuNexus-owned.
package netctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vishvananda/netlink"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/netinfo"
	"niunexus/internal/nm"
	"niunexus/internal/rpc"
)

// InterfaceRequest is the editable subset of an interface.
type InterfaceRequest struct {
	IPv4Mode string   `json:"ipv4Mode"`
	IPv4Addr string   `json:"ipv4Address"`
	IPv4GW   string   `json:"ipv4Gateway"`
	DNS      []string `json:"dns"`
	IPv6Mode string   `json:"ipv6Mode"`
	IPv6Addr string   `json:"ipv6Address"`
	IPv6GW   string   `json:"ipv6Gateway"`
	IPv6DNS  []string `json:"ipv6Dns"`
	MTU      int      `json:"mtu"`
}

// VirtualLinkRequest creates one bridge, VLAN or bond.
type VirtualLinkRequest struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Parent   string   `json:"parent"`
	Members  []string `json:"members"`
	VLANID   int      `json:"vlanId"`
	STP      bool     `json:"stp"`
	Mode     string   `json:"mode"`
	IPv4Mode string   `json:"ipv4Mode"`
	IPv4Addr string   `json:"ipv4Address"`
	IPv4GW   string   `json:"ipv4Gateway"`
	DNS      []string `json:"dns"`
	IPv6Mode string   `json:"ipv6Mode"`
	IPv6Addr string   `json:"ipv6Address"`
	IPv6GW   string   `json:"ipv6Gateway"`
	IPv6DNS  []string `json:"ipv6Dns"`
	MTU      int      `json:"mtu"`
}

// InterfaceDetail adds management state to the read-only interface snapshot.
type InterfaceDetail struct {
	rpc.Interface
	Managed          *config.InterfaceConfig `json:"managed,omitempty"`
	Protected        bool                    `json:"protected"`
	DefaultRoute     bool                    `json:"defaultRoute"`
	ActiveConnection *nm.Connection          `json:"activeConnection,omitempty"`
}

// VirtualLinkDetail adds live presence to a configured link.
type VirtualLinkDetail struct {
	config.VirtualLink
	Present   bool `json:"present"`
	Active    bool `json:"active"`
	Protected bool `json:"protected"`
}

// NMClient is the subset of NetworkManager operations the controller needs.
// *nm.Client implements it; tests use a stateful fake.
type NMClient interface {
	List(context.Context, bool) ([]nm.Connection, error)
	ActiveForDevice(context.Context, string) (*nm.Connection, error)
	ByName(context.Context, string) (*nm.Connection, error)
	Clone(context.Context, string, string) (string, error)
	Modify(context.Context, string, ...string) error
	Up(context.Context, string) error
	Down(context.Context, string) error
	Delete(context.Context, string) error
	AddEthernet(context.Context, string, string) (string, error)
	AddBridge(context.Context, string, string) (string, error)
	AddVlan(context.Context, string, string, string, int) (string, error)
	AddBond(context.Context, string, string) (string, error)
	AddSlave(context.Context, string, string, string) (string, error)
}

// Controller applies network changes.
type Controller struct {
	nm            NMClient
	store         *config.Store
	changes       *changes.Manager
	verifyTimeout time.Duration
}

// New creates a controller. verifyTimeout defaults to 15 seconds.
func New(client NMClient, store *config.Store, manager *changes.Manager) *Controller {
	return &Controller{
		nm:            client,
		store:         store,
		changes:       manager,
		verifyTimeout: 15 * time.Second,
	}
}

// SetVerifyTimeout is used by tests to shorten the verification window.
func (c *Controller) SetVerifyTimeout(timeout time.Duration) {
	if timeout > 0 {
		c.verifyTimeout = timeout
	}
}

// ProtectedInterfaces returns the configured protected interfaces plus, in
// safe mode, every interface that currently has a global IPv4 address or the
// default route.
func (c *Controller) ProtectedInterfaces() []string {
	cfg := c.store.Config()
	set := map[string]bool{}
	for _, name := range cfg.Settings.ProtectedInterfaces {
		set[name] = true
	}
	if routes, err := netinfo.Routes(); err == nil {
		for _, route := range routes {
			if route.Family == "ipv4" && route.Destination == "default" && route.Device != "" {
				set[route.Device] = true
			}
		}
	}
	if cfg.Settings.SafeMode {
		if ifaces, err := netinfo.Interfaces(); err == nil {
			for _, iface := range ifaces {
				if hasGlobalIPv4(iface.IPv4) {
					set[iface.Name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c *Controller) protectedSet() map[string]bool {
	set := map[string]bool{}
	for _, name := range c.ProtectedInterfaces() {
		set[name] = true
	}
	return set
}

func (c *Controller) isProtected(ifname string) bool {
	return c.protectedSet()[ifname]
}

// ListInterfaces returns live interfaces enriched with managed state.
func (c *Controller) ListInterfaces(ctx context.Context) ([]InterfaceDetail, []string, error) {
	ifaces, err := netinfo.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	defaultDevices := map[string]bool{}
	if routes, err := netinfo.Routes(); err == nil {
		for _, route := range routes {
			if route.Family == "ipv4" && route.Destination == "default" && route.Device != "" {
				defaultDevices[route.Device] = true
			}
		}
	}
	var warnings []string
	active := map[string]nm.Connection{}
	if list, err := c.nm.List(ctx, true); err != nil {
		warnings = append(warnings, "读取 NetworkManager 活动连接失败: "+err.Error())
	} else {
		for _, conn := range list {
			if conn.Device != "" {
				active[conn.Device] = conn
			}
		}
	}
	cfg := c.store.Config()
	managed := map[string]config.InterfaceConfig{}
	for _, item := range cfg.Interfaces {
		managed[item.Name] = item
	}
	protected := c.protectedSet()

	out := make([]InterfaceDetail, 0, len(ifaces))
	for _, iface := range ifaces {
		detail := InterfaceDetail{
			Interface:    iface,
			Protected:    protected[iface.Name],
			DefaultRoute: defaultDevices[iface.Name],
		}
		if item, ok := managed[iface.Name]; ok {
			copyItem := item
			detail.Managed = &copyItem
		}
		if conn, ok := active[iface.Name]; ok {
			copyConn := conn
			detail.ActiveConnection = &copyConn
		}
		out = append(out, detail)
	}
	return out, warnings, nil
}

// ListVirtualLinks returns configured links with live presence information.
func (c *Controller) ListVirtualLinks(ctx context.Context) ([]VirtualLinkDetail, []string, error) {
	cfg := c.store.Config()
	var warnings []string
	conns, err := c.nm.List(ctx, false)
	if err != nil {
		warnings = append(warnings, "读取 NetworkManager 连接失败: "+err.Error())
	}
	byUUID := map[string]nm.Connection{}
	for _, conn := range conns {
		byUUID[conn.UUID] = conn
	}
	protected := c.protectedSet()
	out := make([]VirtualLinkDetail, 0, len(cfg.VirtualLinks))
	for _, link := range cfg.VirtualLinks {
		detail := VirtualLinkDetail{VirtualLink: link}
		if conn, ok := byUUID[link.MasterID]; ok {
			detail.Present = true
			detail.Active = conn.Device == link.Name
		}
		detail.Protected = protected[link.Name]
		for _, member := range link.Members {
			if protected[member] {
				detail.Protected = true
			}
		}
		out = append(out, detail)
	}
	return out, warnings, nil
}

// buildIPProperties renders nmcli property/value pairs.
func buildIPProperties(v4Mode, v4Addr, v4GW string, dns []string, v6Mode, v6Addr, v6GW string, v6DNS []string) ([]string, error) {
	props := make([]string, 0, 24)
	switch v4Mode {
	case "auto":
		props = append(props,
			"ipv4.method", "auto",
			"ipv4.addresses", "",
			"ipv4.gateway", "",
			"ipv4.dns", "",
			"ipv4.ignore-auto-dns", "no",
		)
	case "manual":
		if strings.TrimSpace(v4Addr) == "" {
			return nil, errors.New("静态 IPv4 必须填写地址")
		}
		ignore := "no"
		if len(dns) > 0 {
			ignore = "yes"
		}
		props = append(props,
			"ipv4.method", "manual",
			"ipv4.addresses", v4Addr,
			"ipv4.gateway", v4GW,
			"ipv4.dns", strings.Join(dns, " "),
			"ipv4.ignore-auto-dns", ignore,
		)
	case "disabled":
		props = append(props, "ipv4.method", "disabled")
	default:
		return nil, fmt.Errorf("不支持的 IPv4 模式: %q", v4Mode)
	}

	switch v6Mode {
	case "auto":
		props = append(props,
			"ipv6.method", "auto",
			"ipv6.addresses", "",
			"ipv6.gateway", "",
			"ipv6.dns", "",
		)
	case "manual":
		if strings.TrimSpace(v6Addr) == "" {
			return nil, errors.New("静态 IPv6 必须填写地址")
		}
		props = append(props,
			"ipv6.method", "manual",
			"ipv6.addresses", v6Addr,
			"ipv6.gateway", v6GW,
			"ipv6.dns", strings.Join(v6DNS, " "),
		)
	case "disabled", "ignore":
		props = append(props, "ipv6.method", v6Mode)
	default:
		return nil, fmt.Errorf("不支持的 IPv6 模式: %q", v6Mode)
	}
	return props, nil
}

func (c *Controller) verifyInterface(ctx context.Context, name, v4Mode, v4Addr, v6Mode, v6Addr string) error {
	deadline := time.Now().Add(c.verifyTimeout)
	for {
		link, err := linkByName(name)
		if err == nil && linkUp(link) {
			ok := true
			if v4Mode == "manual" && !addressPresent(link, v4Addr, netlink.FAMILY_V4) {
				ok = false
			}
			if v4Mode == "auto" && !linkHasGlobalIPv4(link) {
				ok = false
			}
			if v6Mode == "manual" && !addressPresent(link, v6Addr, netlink.FAMILY_V6) {
				ok = false
			}
			if ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("网口 %s 未在 %s 内达到预期状态", name, c.verifyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (c *Controller) verifyLinkUp(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if link, err := linkByName(name); err == nil && linkUp(link) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("网口 %s 未在预期时间内启用", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (c *Controller) verifyLinkGone(ctx context.Context, name string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := linkByName(name); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("虚拟链路 %s 仍然存在", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func linkByName(name string) (netlink.Link, error) {
	return netlink.LinkByName(name)
}

func linkUp(link netlink.Link) bool {
	return link.Attrs().Flags&net.FlagUp != 0
}

func addressPresent(link netlink.Link, cidr string, family int) bool {
	ip, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return false
	}
	wantOnes, _ := network.Mask.Size()
	addrs, err := netlink.AddrList(link, family)
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if addr.IPNet == nil || !addr.IP.Equal(ip) {
			continue
		}
		ones, _ := addr.IPNet.Mask.Size()
		if ones == wantOnes {
			return true
		}
	}
	return false
}

func linkHasGlobalIPv4(link netlink.Link) bool {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ip := addr.IP
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return true
	}
	return false
}

func hasGlobalIPv4(addresses []string) bool {
	for _, value := range addresses {
		ip, _, err := net.ParseCIDR(value)
		if err == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			return true
		}
	}
	return false
}

func boolString(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func intString(value int) string {
	return strconv.Itoa(value)
}

func newPrefixedID(prefix string) string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(buf)
}

func joinErrors(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "；"))
}

func upsertInterface(cfg *config.Config, item config.InterfaceConfig) {
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == item.Name {
			cfg.Interfaces[i] = item
			return
		}
	}
	cfg.Interfaces = append(cfg.Interfaces, item)
}

func removeInterface(cfg *config.Config, name string) {
	out := cfg.Interfaces[:0]
	for _, item := range cfg.Interfaces {
		if item.Name != name {
			out = append(out, item)
		}
	}
	cfg.Interfaces = out
}

func findInterface(cfg *config.Config, name string) *config.InterfaceConfig {
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == name {
			item := cfg.Interfaces[i]
			return &item
		}
	}
	return nil
}

func findLinkByID(cfg *config.Config, id string) *config.VirtualLink {
	for i := range cfg.VirtualLinks {
		if cfg.VirtualLinks[i].ID == id {
			link := cfg.VirtualLinks[i]
			return &link
		}
	}
	return nil
}

func findLinkByName(cfg *config.Config, name string) *config.VirtualLink {
	for i := range cfg.VirtualLinks {
		if cfg.VirtualLinks[i].Name == name {
			link := cfg.VirtualLinks[i]
			return &link
		}
	}
	return nil
}

func upsertLink(cfg *config.Config, link config.VirtualLink) {
	for i := range cfg.VirtualLinks {
		if cfg.VirtualLinks[i].ID == link.ID {
			cfg.VirtualLinks[i] = link
			return
		}
	}
	cfg.VirtualLinks = append(cfg.VirtualLinks, link)
}

func removeLink(cfg *config.Config, id string) {
	out := cfg.VirtualLinks[:0]
	for _, link := range cfg.VirtualLinks {
		if link.ID != id {
			out = append(out, link)
		}
	}
	cfg.VirtualLinks = out
}
