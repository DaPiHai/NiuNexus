// Package config owns the persistent desired-state document. Every write is
// validated and preceded by a backup so a bad edit cannot silently destroy the
// previous working configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// CurrentSchemaVersion is the only schema this build understands.
	CurrentSchemaVersion = 1
	defaultTimeout       = 120
	maxBackups           = 20
)

var (
	interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)
	uuidish       = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// Config is the complete desired state. Feature-specific sections are filled
// in by later phases; keeping them here avoids a schema migration for every
// feature.
type Config struct {
	SchemaVersion int               `json:"schemaVersion"`
	Settings      Settings          `json:"settings"`
	Interfaces    []InterfaceConfig `json:"interfaces,omitempty"`
	VirtualLinks  []VirtualLink     `json:"virtualLinks,omitempty"`
	StaticRoutes  []StaticRoute     `json:"staticRoutes,omitempty"`
	PolicyRules   []PolicyRule      `json:"policyRules,omitempty"`
	PortForwards  []PortForward     `json:"portForwards,omitempty"`
	NAT           NATConfig         `json:"nat,omitempty"`
	Proxy         ProxyConfig       `json:"proxy,omitempty"`
}

// Settings are low-risk application preferences.
type Settings struct {
	SafeMode            bool     `json:"safeMode"`
	ConfirmTimeoutSec   int      `json:"confirmTimeoutSec"`
	Language            string   `json:"language"`
	LogLevel            string   `json:"logLevel"`
	AutoApplyOnStart    bool     `json:"autoApplyOnStart"`
	ProtectedInterfaces []string `json:"protectedInterfaces,omitempty"`
}

// InterfaceConfig describes an explicit override for a managed interface.
type InterfaceConfig struct {
	Name              string   `json:"name"`
	Managed           bool     `json:"managed"`
	OriginalID        string   `json:"originalId,omitempty"`
	ManagedID         string   `json:"managedId,omitempty"`
	OriginalWasActive bool     `json:"originalWasActive,omitempty"`
	ConnectionType    string   `json:"connectionType,omitempty"`
	IPv4Mode          string   `json:"ipv4Mode,omitempty"`
	IPv4Addr          string   `json:"ipv4Address,omitempty"`
	IPv4GW            string   `json:"ipv4Gateway,omitempty"`
	DNS               []string `json:"dns,omitempty"`
	IPv6Mode          string   `json:"ipv6Mode,omitempty"`
	IPv6Addr          string   `json:"ipv6Address,omitempty"`
	IPv6GW            string   `json:"ipv6Gateway,omitempty"`
	IPv6DNS           []string `json:"ipv6Dns,omitempty"`
	MTU               int      `json:"mtu,omitempty"`
}

// VirtualLink is a NiuNexus-owned bridge, VLAN or bond.
type VirtualLink struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Parent    string   `json:"parent,omitempty"`
	MasterID  string   `json:"masterId,omitempty"`
	Members   []string `json:"members,omitempty"`
	MemberIDs []string `json:"memberIds,omitempty"`
	VLANID    int      `json:"vlanId,omitempty"`
	STP       bool     `json:"stp,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	IPv4Mode  string   `json:"ipv4Mode,omitempty"`
	IPv4Addr  string   `json:"ipv4Address,omitempty"`
	IPv4GW    string   `json:"ipv4Gateway,omitempty"`
	DNS       []string `json:"dns,omitempty"`
	IPv6Mode  string   `json:"ipv6Mode,omitempty"`
	IPv6Addr  string   `json:"ipv6Address,omitempty"`
	IPv6GW    string   `json:"ipv6Gateway,omitempty"`
	IPv6DNS   []string `json:"ipv6Dns,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
}

// StaticRoute is a route that NiuNexus owns and reapplies.
type StaticRoute struct {
	ID          string `json:"id"`
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device,omitempty"`
	Metric      int    `json:"metric,omitempty"`
	Table       int    `json:"table,omitempty"`
}

// PolicyRule sends matching source traffic to a dedicated routing table.
type PolicyRule struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Source   string `json:"source"`
	Table    int    `json:"table"`
	WAN      string `json:"wan"`
	Gateway  string `json:"gateway,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// PortForward is a DNAT rule with an optional hairpin masquerade.
type PortForward struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol"`
	WAN      string `json:"wan,omitempty"`
	External string `json:"externalPort"`
	DestIP   string `json:"destIp"`
	DestPort string `json:"destPort"`
	Hairpin  bool   `json:"hairpin,omitempty"`
}

// NATConfig controls masquerade for routed LAN traffic.
type NATConfig struct {
	Enabled    bool     `json:"enabled"`
	Interfaces []string `json:"interfaces,omitempty"`
}

// ProxyConfig is the non-secret part of the proxy configuration.
type ProxyConfig struct {
	Enabled         bool        `json:"enabled"`
	Mode            string      `json:"mode,omitempty"`
	Listen          string      `json:"listen,omitempty"`
	ListenPort      int         `json:"listenPort,omitempty"`
	TransparentPort int         `json:"transparentPort,omitempty"`
	RequireAuth     bool        `json:"requireAuth,omitempty"`
	Username        string      `json:"username,omitempty"`
	Transparent     Transparent `json:"transparent,omitempty"`
	Upstream        Upstream    `json:"upstream,omitempty"`
	Bypass          []string    `json:"bypass,omitempty"`
}

// Transparent describes which LAN traffic is redirected to the proxy.
type Transparent struct {
	Enabled     bool     `json:"enabled"`
	Interfaces  []string `json:"interfaces,omitempty"`
	SourceCIDRs []string `json:"sourceCidrs,omitempty"`
}

// Upstream describes an optional proxy chain.
type Upstream struct {
	Type     string `json:"type,omitempty"`
	Address  string `json:"address,omitempty"`
	Username string `json:"username,omitempty"`
}

// SettingsPatch uses pointers so clients can update individual fields.
type SettingsPatch struct {
	SafeMode            *bool     `json:"safeMode,omitempty"`
	ConfirmTimeoutSec   *int      `json:"confirmTimeoutSec,omitempty"`
	Language            *string   `json:"language,omitempty"`
	LogLevel            *string   `json:"logLevel,omitempty"`
	AutoApplyOnStart    *bool     `json:"autoApplyOnStart,omitempty"`
	ProtectedInterfaces *[]string `json:"protectedInterfaces,omitempty"`
}

type installOptions struct {
	SafeMode *bool  `json:"safeMode"`
	Language string `json:"language"`
}

// Store serializes reads and writes of the configuration document.
type Store struct {
	dir      string
	mu       sync.RWMutex
	cfg      *Config
	warnings []string
}

// Open loads config.json. A missing file yields defaults; a corrupt file
// yields defaults plus a warning and leaves the original file untouched.
func Open(dir string) *Store {
	store := &Store{dir: dir}
	cfg, warnings := load(dir)
	store.cfg = cfg
	store.warnings = warnings
	return store
}

// Config returns a deep copy so callers cannot mutate the store by accident.
func (s *Store) Config() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cfg)
}

// Warnings returns non-fatal load problems.
func (s *Store) Warnings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.warnings...)
}

// Update applies an arbitrary mutation under the store lock, validates the
// result, backs up the previous document and persists it atomically.
func (s *Store) Update(mutator func(*Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.cfg)
	if err := mutator(next); err != nil {
		return nil, err
	}
	if err := Validate(next); err != nil {
		return nil, err
	}
	if err := s.saveLocked(next); err != nil {
		return nil, err
	}
	s.cfg = next
	s.warnings = nil
	return clone(next), nil
}

// Reload re-reads config.json. It is used after a snapshot restore so the
// in-memory document matches what is now on disk.
func (s *Store) Reload() {
	cfg, warnings := load(s.dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.warnings = warnings
}

// UpdateSettings applies a patch, validates the result, backs up the previous
// document, and atomically replaces config.json.
func (s *Store) UpdateSettings(patch SettingsPatch) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := clone(s.cfg)
	if patch.SafeMode != nil {
		next.Settings.SafeMode = *patch.SafeMode
	}
	if patch.ConfirmTimeoutSec != nil {
		next.Settings.ConfirmTimeoutSec = *patch.ConfirmTimeoutSec
	}
	if patch.Language != nil {
		next.Settings.Language = *patch.Language
	}
	if patch.LogLevel != nil {
		next.Settings.LogLevel = *patch.LogLevel
	}
	if patch.AutoApplyOnStart != nil {
		next.Settings.AutoApplyOnStart = *patch.AutoApplyOnStart
	}
	if patch.ProtectedInterfaces != nil {
		next.Settings.ProtectedInterfaces = normalizeInterfaces(*patch.ProtectedInterfaces)
	}
	if err := Validate(next); err != nil {
		return nil, err
	}
	if err := s.saveLocked(next); err != nil {
		return nil, err
	}
	s.cfg = next
	s.warnings = nil
	return clone(next), nil
}

func load(dir string) (*Config, []string) {
	cfg := Defaults()
	applyInstallOptions(cfg, filepath.Join(dir, "install-options.json"))

	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, []string{fmt.Sprintf("无法读取配置文件: %v", err)}
	}

	loaded := Defaults()
	if err := json.Unmarshal(data, loaded); err != nil {
		return cfg, []string{fmt.Sprintf("配置文件 JSON 解析失败，当前使用默认值且未覆盖原文件: %v", err)}
	}
	if loaded.SchemaVersion > CurrentSchemaVersion {
		return cfg, []string{fmt.Sprintf("配置文件版本 %d 高于当前程序支持的 %d，已使用默认值", loaded.SchemaVersion, CurrentSchemaVersion)}
	}
	if loaded.SchemaVersion == 0 {
		loaded.SchemaVersion = CurrentSchemaVersion
	}
	if err := Validate(loaded); err != nil {
		return cfg, []string{fmt.Sprintf("配置文件校验失败，已使用默认值: %v", err)}
	}
	return loaded, nil
}

// Defaults returns the safe initial configuration.
func Defaults() *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		Settings: Settings{
			SafeMode:          true,
			ConfirmTimeoutSec: defaultTimeout,
			Language:          "zh-CN",
			LogLevel:          "info",
			AutoApplyOnStart:  true,
		},
	}
}

// Validate checks the invariants shared by load and save.
func Validate(cfg *Config) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("不支持的配置版本: %d", cfg.SchemaVersion)
	}
	if cfg.Settings.ConfirmTimeoutSec < 5 || cfg.Settings.ConfirmTimeoutSec > 3600 {
		return errors.New("确认超时必须在 5 到 3600 秒之间")
	}
	if cfg.Settings.Language != "zh-CN" && cfg.Settings.Language != "en" {
		return errors.New("界面语言只能是 zh-CN 或 en")
	}
	switch cfg.Settings.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("日志级别必须是 debug、info、warn 或 error")
	}
	if len(cfg.Settings.ProtectedInterfaces) > 32 {
		return errors.New("受保护网口不能超过 32 个")
	}
	seen := map[string]bool{}
	for _, name := range cfg.Settings.ProtectedInterfaces {
		if !interfaceName.MatchString(name) {
			return fmt.Errorf("非法网口名称: %q", name)
		}
		if seen[name] {
			return fmt.Errorf("受保护网口重复: %s", name)
		}
		seen[name] = true
	}
	if err := validateInterfaces(cfg.Interfaces); err != nil {
		return err
	}
	if err := validateVirtualLinks(cfg.VirtualLinks); err != nil {
		return err
	}
	if err := validatePortForwards(cfg.PortForwards); err != nil {
		return err
	}
	if err := validateNAT(cfg.NAT); err != nil {
		return err
	}
	if err := validateStaticRoutes(cfg.StaticRoutes); err != nil {
		return err
	}
	if err := validatePolicyRules(cfg.PolicyRules); err != nil {
		return err
	}
	if err := validateProxy(cfg.Proxy); err != nil {
		return err
	}
	return nil
}

// NeedsForwarding reports whether any enabled feature requires IPv4
// forwarding.
func NeedsForwarding(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.NAT.Enabled && len(cfg.NAT.Interfaces) > 0 {
		return true
	}
	if cfg.Proxy.Enabled && cfg.Proxy.Transparent.Enabled && len(cfg.Proxy.Transparent.SourceCIDRs) > 0 {
		return true
	}
	return false
}

// ValidatePortForwards validates a complete list, including overlap checks.
func ValidatePortForwards(items []PortForward) error {
	return validatePortForwards(items)
}

// ValidatePortForward validates one rule.
func ValidatePortForward(item PortForward) error {
	return validatePortForwards([]PortForward{item})
}

// ValidateNAT validates the masquerade configuration.
func ValidateNAT(item NATConfig) error {
	return validateNAT(item)
}

// ValidateStaticRoutes validates a complete route list.
func ValidateStaticRoutes(items []StaticRoute) error {
	return validateStaticRoutes(items)
}

// ValidatePolicyRules validates a complete policy-rule list.
func ValidatePolicyRules(items []PolicyRule) error {
	return validatePolicyRules(items)
}

// ValidateProxy validates the proxy configuration.
func ValidateProxy(item ProxyConfig) error {
	return validateProxy(item)
}

// ValidateInterface validates one interface override.
func ValidateInterface(item InterfaceConfig) error {
	return validateInterfaces([]InterfaceConfig{item})
}

// ValidateVirtualLink validates one virtual link.
func ValidateVirtualLink(item VirtualLink) error {
	return validateVirtualLinks([]VirtualLink{item})
}

func validateInterfaces(items []InterfaceConfig) error {
	seen := map[string]bool{}
	for _, item := range items {
		if !interfaceName.MatchString(item.Name) {
			return fmt.Errorf("非法网口名称: %q", item.Name)
		}
		if seen[item.Name] {
			return fmt.Errorf("网口配置重复: %s", item.Name)
		}
		seen[item.Name] = true
		if !oneOf(item.IPv4Mode, "", "auto", "manual", "disabled") {
			return fmt.Errorf("网口 %s 的 IPv4 模式无效: %q", item.Name, item.IPv4Mode)
		}
		if !oneOf(item.IPv6Mode, "", "auto", "manual", "disabled", "ignore") {
			return fmt.Errorf("网口 %s 的 IPv6 模式无效: %q", item.Name, item.IPv6Mode)
		}
		if item.IPv4Addr != "" {
			if err := validateCIDR(item.IPv4Addr); err != nil {
				return fmt.Errorf("网口 %s 的 IPv4 地址无效: %w", item.Name, err)
			}
		}
		if item.IPv4GW != "" && net.ParseIP(item.IPv4GW) == nil {
			return fmt.Errorf("网口 %s 的 IPv4 网关无效: %s", item.Name, item.IPv4GW)
		}
		if err := validateIPList(item.DNS); err != nil {
			return fmt.Errorf("网口 %s 的 DNS 无效: %w", item.Name, err)
		}
		if item.IPv6Addr != "" {
			if err := validateCIDR(item.IPv6Addr); err != nil {
				return fmt.Errorf("网口 %s 的 IPv6 地址无效: %w", item.Name, err)
			}
		}
		if item.IPv6GW != "" && net.ParseIP(item.IPv6GW) == nil {
			return fmt.Errorf("网口 %s 的 IPv6 网关无效: %s", item.Name, item.IPv6GW)
		}
		if err := validateIPList(item.IPv6DNS); err != nil {
			return fmt.Errorf("网口 %s 的 IPv6 DNS 无效: %w", item.Name, err)
		}
		if item.MTU != 0 && (item.MTU < 576 || item.MTU > 9000) {
			return fmt.Errorf("网口 %s 的 MTU 必须在 576 到 9000 之间", item.Name)
		}
		for _, id := range []string{item.OriginalID, item.ManagedID} {
			if id != "" && !uuidish.MatchString(id) {
				return fmt.Errorf("网口 %s 的连接 UUID 无效: %q", item.Name, id)
			}
		}
	}
	return nil
}

func validateVirtualLinks(items []VirtualLink) error {
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("虚拟链路缺少 ID")
		}
		if ids[item.ID] {
			return fmt.Errorf("虚拟链路 ID 重复: %s", item.ID)
		}
		ids[item.ID] = true
		if !interfaceName.MatchString(item.Name) {
			return fmt.Errorf("虚拟链路名称无效: %q", item.Name)
		}
		if names[item.Name] {
			return fmt.Errorf("虚拟链路名称重复: %s", item.Name)
		}
		names[item.Name] = true
		switch item.Type {
		case "bridge":
			// A bridge may be created empty and have ports attached later.
		case "bond":
			if len(item.Members) == 0 {
				return fmt.Errorf("Bond %s 至少需要一个成员网口", item.Name)
			}
			if !oneOf(item.Mode, "", "balance-rr", "active-backup", "balance-xor", "broadcast", "802.3ad", "balance-tlb", "balance-alb") {
				return fmt.Errorf("Bond 模式无效: %q", item.Mode)
			}
		case "vlan":
			if !interfaceName.MatchString(item.Parent) {
				return fmt.Errorf("VLAN %s 的父接口无效: %q", item.Name, item.Parent)
			}
			if item.VLANID < 1 || item.VLANID > 4094 {
				return fmt.Errorf("VLAN %s 的 ID 必须在 1 到 4094 之间", item.Name)
			}
			if len(item.Members) != 0 {
				return fmt.Errorf("VLAN %s 不能配置成员网口", item.Name)
			}
		default:
			return fmt.Errorf("不支持的虚拟链路类型: %q", item.Type)
		}
		memberSeen := map[string]bool{}
		for _, member := range item.Members {
			if !interfaceName.MatchString(member) {
				return fmt.Errorf("虚拟链路 %s 的成员网口无效: %q", item.Name, member)
			}
			if member == item.Name {
				return fmt.Errorf("虚拟链路 %s 不能把自己作为成员", item.Name)
			}
			if memberSeen[member] {
				return fmt.Errorf("虚拟链路 %s 的成员重复: %s", item.Name, member)
			}
			memberSeen[member] = true
		}
		if err := validateLinkIP(item); err != nil {
			return fmt.Errorf("虚拟链路 %s: %w", item.Name, err)
		}
		if item.MasterID != "" && !uuidish.MatchString(item.MasterID) {
			return fmt.Errorf("虚拟链路 %s 的 master UUID 无效", item.Name)
		}
		for _, id := range item.MemberIDs {
			if !uuidish.MatchString(id) {
				return fmt.Errorf("虚拟链路 %s 的成员 UUID 无效: %q", item.Name, id)
			}
		}
	}
	return nil
}

func validateLinkIP(item VirtualLink) error {
	if !oneOf(item.IPv4Mode, "", "auto", "manual", "disabled") {
		return fmt.Errorf("IPv4 模式无效: %q", item.IPv4Mode)
	}
	if !oneOf(item.IPv6Mode, "", "auto", "manual", "disabled", "ignore") {
		return fmt.Errorf("IPv6 模式无效: %q", item.IPv6Mode)
	}
	if item.IPv4Addr != "" {
		if err := validateCIDR(item.IPv4Addr); err != nil {
			return fmt.Errorf("IPv4 地址无效: %w", err)
		}
	}
	if item.IPv4GW != "" && net.ParseIP(item.IPv4GW) == nil {
		return fmt.Errorf("IPv4 网关无效: %s", item.IPv4GW)
	}
	if err := validateIPList(item.DNS); err != nil {
		return fmt.Errorf("DNS 无效: %w", err)
	}
	if item.IPv6Addr != "" {
		if err := validateCIDR(item.IPv6Addr); err != nil {
			return fmt.Errorf("IPv6 地址无效: %w", err)
		}
	}
	if item.IPv6GW != "" && net.ParseIP(item.IPv6GW) == nil {
		return fmt.Errorf("IPv6 网关无效: %s", item.IPv6GW)
	}
	if err := validateIPList(item.IPv6DNS); err != nil {
		return fmt.Errorf("IPv6 DNS 无效: %w", err)
	}
	if item.MTU != 0 && (item.MTU < 576 || item.MTU > 9000) {
		return errors.New("MTU 必须在 576 到 9000 之间")
	}
	return nil
}

func validateCIDR(value string) error {
	if _, _, err := net.ParseCIDR(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("必须是 CIDR 格式（例如 192.168.1.10/24）")
	}
	return nil
}

func validateIPList(values []string) error {
	for _, value := range values {
		if net.ParseIP(strings.TrimSpace(value)) == nil {
			return fmt.Errorf("无效 IP: %q", value)
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s *Store) saveLocked(next *Config) error {
	payload, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeDocument(s.dir, payload)
}

// Restore validates and atomically writes a document captured by a snapshot.
// The current document is backed up first, so a restore is itself reversible.
func Restore(dir string, data []byte) error {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("快照配置解析失败: %w", err)
	}
	if err := Validate(&cfg); err != nil {
		return fmt.Errorf("快照配置校验失败: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return writeDocument(dir, data)
}

func writeDocument(dir string, payload []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	if err := backupExisting(dir); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("写入临时配置文件失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("同步临时配置文件失败: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "config.json")); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

func backupExisting(dir string) error {
	src := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取旧配置失败: %w", err)
	}
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("创建备份目录失败: %w", err)
	}
	name := fmt.Sprintf("config-%s.json", time.Now().Format("20060102-150405.000"))
	if err := os.WriteFile(filepath.Join(backupDir, name), data, 0o600); err != nil {
		return fmt.Errorf("写入配置备份失败: %w", err)
	}
	return pruneBackups(backupDir, maxBackups)
}

func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "config-") && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return nil
}

func applyInstallOptions(cfg *Config, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var options installOptions
	if err := json.Unmarshal(data, &options); err != nil {
		return
	}
	if options.SafeMode != nil {
		cfg.Settings.SafeMode = *options.SafeMode
	}
	if options.Language == "zh-CN" || options.Language == "en" {
		cfg.Settings.Language = options.Language
	}
}

var domainName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func validateProxy(item ProxyConfig) error {
	if !item.Enabled {
		return nil
	}
	if item.Listen != "" && net.ParseIP(item.Listen) == nil {
		return fmt.Errorf("代理监听地址无效: %q", item.Listen)
	}
	if item.ListenPort < 1 || item.ListenPort > 65535 {
		return errors.New("代理监听端口必须在 1-65535 之间")
	}
	if item.RequireAuth && strings.TrimSpace(item.Username) == "" {
		return errors.New("启用代理认证时必须填写用户名")
	}
	if item.Transparent.Enabled {
		if item.TransparentPort < 1 || item.TransparentPort > 65535 {
			return errors.New("透明代理端口必须在 1-65535 之间")
		}
		if item.TransparentPort == item.ListenPort {
			return errors.New("透明代理端口不能与正向代理端口相同")
		}
		if len(item.Transparent.SourceCIDRs) == 0 {
			return errors.New("透明代理至少需要一个来源网段")
		}
		for _, value := range item.Transparent.SourceCIDRs {
			ip, _, err := net.ParseCIDR(strings.TrimSpace(value))
			if err != nil || ip.To4() == nil {
				return fmt.Errorf("透明代理来源网段必须是 IPv4 CIDR: %q", value)
			}
		}
		for _, iface := range item.Transparent.Interfaces {
			if !interfaceName.MatchString(iface) {
				return fmt.Errorf("透明代理网口无效: %q", iface)
			}
		}
	}
	switch item.Upstream.Type {
	case "", "none":
	case "http", "socks5":
		if err := validateHostPort(item.Upstream.Address); err != nil {
			return fmt.Errorf("上游代理地址无效: %w", err)
		}
	default:
		return fmt.Errorf("不支持的上游代理类型: %q", item.Upstream.Type)
	}
	if len(item.Bypass) > 100 {
		return errors.New("Bypass 条目不能超过 100 个")
	}
	for _, value := range item.Bypass {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			if _, _, err := net.ParseCIDR(value); err != nil {
				return fmt.Errorf("Bypass CIDR 无效: %q", value)
			}
			continue
		}
		if net.ParseIP(value) != nil {
			continue
		}
		if !domainName.MatchString(value) {
			return fmt.Errorf("Bypass 域名无效: %q", value)
		}
	}
	return nil
}

func validateHostPort(value string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return errors.New("必须是 host:port 格式")
	}
	if host == "" {
		return errors.New("缺少主机")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("端口必须在 1-65535 之间")
	}
	return nil
}

func validateStaticRoutes(items []StaticRoute) error {
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("静态路由缺少 ID")
		}
		if ids[item.ID] {
			return fmt.Errorf("静态路由 ID 重复: %s", item.ID)
		}
		ids[item.ID] = true
		destination := strings.TrimSpace(item.Destination)
		if destination == "" {
			return errors.New("静态路由缺少目标网段")
		}
		if destination != "default" {
			if err := validateCIDR(destination); err != nil {
				return fmt.Errorf("静态路由目标网段无效: %w", err)
			}
		}
		if item.Gateway == "" && item.Device == "" {
			return errors.New("静态路由必须指定网关或出口网口")
		}
		if item.Gateway != "" && net.ParseIP(item.Gateway) == nil {
			return fmt.Errorf("静态路由网关无效: %s", item.Gateway)
		}
		if item.Device != "" && !interfaceName.MatchString(item.Device) {
			return fmt.Errorf("静态路由出口网口无效: %q", item.Device)
		}
		if item.Metric < 0 || item.Metric > 2147483647 {
			return errors.New("静态路由 metric 无效")
		}
		if item.Table != 0 && item.Table != 254 {
			return errors.New("静态路由首版只支持主路由表")
		}
		key := fmt.Sprintf("%s|%s|%s|%d", destination, item.Gateway, item.Device, item.Metric)
		if keys[key] {
			return fmt.Errorf("静态路由重复: %s", destination)
		}
		keys[key] = true
	}
	return nil
}

func validatePolicyRules(items []PolicyRule) error {
	ids := map[string]bool{}
	tables := map[int]bool{}
	priorities := map[int]bool{}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("策略路由缺少 ID")
		}
		if ids[item.ID] {
			return fmt.Errorf("策略路由 ID 重复: %s", item.ID)
		}
		ids[item.ID] = true
		if err := validateCIDR(item.Source); err != nil {
			return fmt.Errorf("策略路由来源网段无效: %w", err)
		}
		if item.Table < 100 || item.Table > 199 {
			return errors.New("策略路由表号必须在 100 到 199 之间")
		}
		if tables[item.Table] {
			return fmt.Errorf("策略路由表号重复: %d", item.Table)
		}
		tables[item.Table] = true
		if !interfaceName.MatchString(item.WAN) {
			return fmt.Errorf("策略路由出口网口无效: %q", item.WAN)
		}
		if net.ParseIP(item.Gateway) == nil {
			return fmt.Errorf("策略路由网关无效: %q", item.Gateway)
		}
		if item.Priority < 1 || item.Priority > 32766 {
			return errors.New("策略路由优先级必须在 1 到 32766 之间")
		}
		if priorities[item.Priority] {
			return fmt.Errorf("策略路由优先级重复: %d", item.Priority)
		}
		priorities[item.Priority] = true
	}
	return nil
}

func validatePortForwards(items []PortForward) error {
	ids := map[string]bool{}
	names := map[string]bool{}
	type interval struct {
		start int
		end   int
		proto string
		wan   string
		name  string
	}
	intervals := make([]interval, 0, len(items)*2)
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("端口转发缺少 ID")
		}
		if ids[item.ID] {
			return fmt.Errorf("端口转发 ID 重复: %s", item.ID)
		}
		ids[item.ID] = true
		if item.Name != "" {
			if names[item.Name] {
				return fmt.Errorf("端口转发名称重复: %s", item.Name)
			}
			names[item.Name] = true
		}
		protocol := strings.ToLower(strings.TrimSpace(item.Protocol))
		if protocol != "tcp" && protocol != "udp" && protocol != "both" {
			return fmt.Errorf("端口转发协议必须是 tcp、udp 或 both: %q", item.Protocol)
		}
		if item.WAN != "" && !interfaceName.MatchString(item.WAN) {
			return fmt.Errorf("端口转发 WAN 网口无效: %q", item.WAN)
		}
		if net.ParseIP(item.DestIP) == nil {
			return fmt.Errorf("端口转发 %s 的内部 IP 无效: %q", item.Name, item.DestIP)
		}
		if net.ParseIP(item.DestIP).To4() == nil {
			return fmt.Errorf("端口转发 %s 当前仅支持 IPv4 内部地址", item.Name)
		}
		extStart, extEnd, err := parsePortRange(item.External)
		if err != nil {
			return fmt.Errorf("端口转发 %s 的外部端口: %w", item.Name, err)
		}
		dstStart, dstEnd, err := parsePortRange(item.DestPort)
		if err != nil {
			return fmt.Errorf("端口转发 %s 的内部端口: %w", item.Name, err)
		}
		if extEnd-extStart != dstEnd-dstStart {
			return fmt.Errorf("端口转发 %s 的外部端口范围与内部端口范围长度必须一致", item.Name)
		}
		protocols := []string{protocol}
		if protocol == "both" {
			protocols = []string{"tcp", "udp"}
		}
		for _, proto := range protocols {
			for _, existing := range intervals {
				if existing.wan != item.WAN || existing.proto != proto {
					continue
				}
				if rangesOverlap(extStart, extEnd, existing.start, existing.end) {
					return fmt.Errorf("端口转发 %s 与 %s 在 %s/%s 上端口重叠", item.Name, existing.name, item.WAN, proto)
				}
			}
			intervals = append(intervals, interval{
				start: extStart,
				end:   extEnd,
				proto: proto,
				wan:   item.WAN,
				name:  item.Name,
			})
		}
	}
	return nil
}

func validateNAT(item NATConfig) error {
	if item.Enabled && len(item.Interfaces) == 0 {
		return errors.New("启用 NAT 需要至少一个 WAN 接口")
	}
	seen := map[string]bool{}
	for _, name := range item.Interfaces {
		if !interfaceName.MatchString(name) {
			return fmt.Errorf("NAT WAN 网口无效: %q", name)
		}
		if seen[name] {
			return fmt.Errorf("NAT WAN 网口重复: %s", name)
		}
		seen[name] = true
	}
	return nil
}

func parsePortRange(value string) (int, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, errors.New("端口不能为空")
	}
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("端口格式无效: %q", value)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("端口格式无效: %q", value)
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("端口格式无效: %q", value)
		}
	}
	if start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("端口必须在 1-65535 且起始不大于结束: %q", value)
	}
	return start, end, nil
}

func rangesOverlap(aStart, aEnd, bStart, bEnd int) bool {
	return aStart <= bEnd && bStart <= aEnd
}

func normalizeInterfaces(values []string) []string {
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
	sort.Strings(out)
	return out
}

func clone(cfg *Config) *Config {
	if cfg == nil {
		return Defaults()
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return Defaults()
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		return Defaults()
	}
	return &out
}
