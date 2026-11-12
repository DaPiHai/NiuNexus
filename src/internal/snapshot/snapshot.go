// Package snapshot captures the state needed to undo a network change.
//
// Phase 1 restores config.json, the dedicated nft table and forwarding
// sysctls. Interface and route state is captured for diagnostics; those
// controllers add their own transactional restore in Phase 2/3.
package snapshot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"niunexus/internal/config"
	"niunexus/internal/version"
)

const (
	maxSnapshots = 10
	commandLimit = 5 * time.Second
)

// Meta is the small index document stored next to each snapshot.
type Meta struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	Reason         string    `json:"reason"`
	Actor          string    `json:"actor"`
	Version        string    `json:"version"`
	HasConfig      bool      `json:"hasConfig"`
	HasSecrets     bool      `json:"hasSecrets"`
	HasNft         bool      `json:"hasNft"`
	HasSystemState bool      `json:"hasSystemState"`
}

// Report describes what a restore actually changed.
type Report struct {
	ID              string   `json:"id"`
	ConfigRestored  bool     `json:"configRestored"`
	SecretsRestored bool     `json:"secretsRestored"`
	NftRestored     bool     `json:"nftRestored"`
	SysctlRestored  bool     `json:"sysctlRestored"`
	Warnings        []string `json:"warnings,omitempty"`
}

type systemState struct {
	CapturedAt  time.Time       `json:"capturedAt"`
	IPAddr      json.RawMessage `json:"ipAddr,omitempty"`
	IPRoute     json.RawMessage `json:"ipRoute,omitempty"`
	IPRule      json.RawMessage `json:"ipRule,omitempty"`
	IPv4Forward string          `json:"ipv4Forward,omitempty"`
	IPv6Forward string          `json:"ipv6Forward,omitempty"`
	Errors      []string        `json:"errors,omitempty"`
}

// Manager creates and restores snapshots under the config directory.
type Manager struct {
	configDir string
	varDir    string
	root      string
	skip      bool
	mu        sync.Mutex
}

// New creates a manager that captures host state where possible.
func New(configDir, varDir string) *Manager {
	return NewWithOptions(configDir, varDir, false)
}

// NewWithOptions allows tests and unprivileged diagnostics to skip host
// commands while still exercising the config restore path.
func NewWithOptions(configDir, varDir string, skipSystem bool) *Manager {
	return &Manager{
		configDir: configDir,
		varDir:    varDir,
		root:      filepath.Join(configDir, "backups", "snapshots"),
		skip:      skipSystem,
	}
}

// Root returns the snapshot directory.
func (m *Manager) Root() string { return m.root }

// Create captures a snapshot and returns its metadata.
func (m *Manager) Create(reason, actor string) (*Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return nil, fmt.Errorf("创建快照目录失败: %w", err)
	}
	id := time.Now().UTC().Format("20060102T150405Z") + "-" + randomHex(3)
	dir := filepath.Join(m.root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建快照失败: %w", err)
	}
	meta := &Meta{
		ID:        id,
		CreatedAt: time.Now(),
		Reason:    strings.TrimSpace(reason),
		Actor:     strings.TrimSpace(actor),
		Version:   version.Version,
	}

	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()

	if data, err := os.ReadFile(filepath.Join(m.configDir, "config.json")); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
			return nil, fmt.Errorf("保存配置快照失败: %w", err)
		}
		meta.HasConfig = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if data, err := os.ReadFile(filepath.Join(m.configDir, "secrets.json")); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "secrets.json"), data, 0o600); err != nil {
			return nil, fmt.Errorf("保存密钥快照失败: %w", err)
		}
		meta.HasSecrets = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取密钥失败: %w", err)
	}

	if !m.skip {
		if data, err := m.captureNft(); err == nil && len(data) > 0 {
			if err := os.WriteFile(filepath.Join(dir, "nft.nft"), data, 0o600); err != nil {
				return nil, fmt.Errorf("保存 nft 快照失败: %w", err)
			}
			meta.HasNft = true
		}
		if state, err := m.captureSystemState(); err == nil {
			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o600); err != nil {
				return nil, fmt.Errorf("保存系统状态快照失败: %w", err)
			}
			meta.HasSystemState = true
		}
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		return nil, fmt.Errorf("保存快照索引失败: %w", err)
	}
	cleanup = false
	_ = m.pruneLocked()
	return meta, nil
}

// List returns snapshots newest first.
func (m *Manager) List() ([]Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

// Latest returns the newest snapshot metadata.
func (m *Manager) Latest() (*Meta, error) {
	list, err := m.List()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errors.New("没有可用的快照")
	}
	return &list[0], nil
}

// RestoreLast restores the newest snapshot.
func (m *Manager) RestoreLast() (*Report, error) {
	meta, err := m.Latest()
	if err != nil {
		return nil, err
	}
	return m.Restore(meta.ID)
}

// Restore applies a snapshot. It is expected to run while the core is stopped;
// the CLI enforces that ordering.
func (m *Manager) Restore(id string) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("缺少快照 ID")
	}
	dir := filepath.Join(m.root, id)
	meta, err := readMeta(dir)
	if err != nil {
		return nil, err
	}
	report := &Report{ID: meta.ID}

	if meta.HasConfig {
		data, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			return nil, fmt.Errorf("读取快照配置失败: %w", err)
		}
		if err := config.Restore(m.configDir, data); err != nil {
			return nil, err
		}
		report.ConfigRestored = true
	} else {
		// The snapshot was taken before any config existed. Remove a document
		// created by the failed change so the app returns to defaults.
		if err := os.Remove(filepath.Join(m.configDir, "config.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			report.Warnings = append(report.Warnings, "移除失败变更生成的配置失败: "+err.Error())
		}
	}

	secretsPath := filepath.Join(m.configDir, "secrets.json")
	if meta.HasSecrets {
		data, err := os.ReadFile(filepath.Join(dir, "secrets.json"))
		if err != nil {
			report.Warnings = append(report.Warnings, "读取密钥快照失败: "+err.Error())
		} else if err := os.MkdirAll(m.configDir, 0o700); err != nil {
			report.Warnings = append(report.Warnings, "创建配置目录失败: "+err.Error())
		} else if err := os.WriteFile(secretsPath, data, 0o600); err != nil {
			report.Warnings = append(report.Warnings, "恢复密钥失败: "+err.Error())
		} else {
			report.SecretsRestored = true
		}
	} else if err := os.Remove(secretsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		report.Warnings = append(report.Warnings, "移除失败变更生成的密钥失败: "+err.Error())
	}

	if meta.HasSystemState {
		state, err := readSystemState(dir)
		if err == nil {
			if m.restoreSysctl(state) {
				report.SysctlRestored = true
			}
		} else {
			report.Warnings = append(report.Warnings, "读取系统状态快照失败: "+err.Error())
		}
	}

	if meta.HasNft {
		data, err := os.ReadFile(filepath.Join(dir, "nft.nft"))
		if err != nil {
			report.Warnings = append(report.Warnings, "读取 nft 快照失败: "+err.Error())
		} else if err := m.restoreNft(data); err != nil {
			report.Warnings = append(report.Warnings, "恢复 nft 表失败: "+err.Error())
		} else {
			report.NftRestored = true
		}
	} else if !m.skip {
		// A snapshot with no NiuNexus table should leave no table behind.
		if err := m.deleteNftTable(); err != nil {
			report.Warnings = append(report.Warnings, "清理 nft 表失败: "+err.Error())
		} else {
			report.NftRestored = true
		}
	}

	report.Warnings = append(report.Warnings,
		"接口地址、路由与 NetworkManager 连接的恢复将在 Phase 2 控制器中接入；本快照已保存这些状态用于诊断。")
	return report, nil
}

func (m *Manager) listLocked() ([]Meta, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Meta{}, nil
		}
		return nil, err
	}
	out := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := readMeta(filepath.Join(m.root, entry.Name()))
		if err != nil {
			continue
		}
		out = append(out, *meta)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (m *Manager) pruneLocked() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	type item struct {
		name string
		meta Meta
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := readMeta(filepath.Join(m.root, entry.Name()))
		if err != nil {
			continue
		}
		items = append(items, item{name: entry.Name(), meta: *meta})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].meta.CreatedAt.Before(items[j].meta.CreatedAt)
	})
	for len(items) > maxSnapshots {
		path := filepath.Join(m.root, items[0].name)
		if info, err := os.Lstat(path); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			_ = os.RemoveAll(path)
		}
		items = items[1:]
	}
	return nil
}

func (m *Manager) captureNft() ([]byte, error) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, nft, "list", "table", "inet", "niunexus")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if !bytes.Contains(data, []byte("table inet niunexus")) {
		return nil, errors.New("nft 表不存在")
	}
	return data, nil
}

func (m *Manager) captureSystemState() (*systemState, error) {
	state := &systemState{CapturedAt: time.Now()}
	state.IPv4Forward = readTrimmed("/proc/sys/net/ipv4/ip_forward")
	state.IPv6Forward = readTrimmed("/proc/sys/net/ipv6/conf/all/forwarding")

	commands := []struct {
		name string
		args []string
		dst  *json.RawMessage
	}{
		{"ip", []string{"-j", "addr", "show"}, &state.IPAddr},
		{"ip", []string{"-j", "route", "show", "table", "all"}, &state.IPRoute},
		{"ip", []string{"-j", "rule", "show"}, &state.IPRule},
	}
	for _, item := range commands {
		data, err := run(item.name, item.args...)
		if err != nil {
			state.Errors = append(state.Errors, err.Error())
			continue
		}
		if json.Valid(data) {
			*item.dst = json.RawMessage(data)
		} else {
			state.Errors = append(state.Errors, fmt.Sprintf("%s 输出不是 JSON", item.name))
		}
	}
	return state, nil
}

func (m *Manager) restoreNft(data []byte) error {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return errors.New("系统缺少 nft")
	}
	if err := m.deleteNftTable(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, nft, "-f", "-")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (m *Manager) deleteNftTable() error {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return errors.New("系统缺少 nft")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, nft, "delete", "table", "inet", "niunexus")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := stderr.String()
		if strings.Contains(message, "No such file or directory") || strings.Contains(message, "does not exist") {
			return nil
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(message))
	}
	return nil
}

func (m *Manager) restoreSysctl(state *systemState) bool {
	restored := false
	pairs := []struct {
		path  string
		value string
	}{
		{"/proc/sys/net/ipv4/ip_forward", state.IPv4Forward},
		{"/proc/sys/net/ipv6/conf/all/forwarding", state.IPv6Forward},
	}
	for _, pair := range pairs {
		if pair.value != "0" && pair.value != "1" {
			continue
		}
		if err := os.WriteFile(pair.path, []byte(pair.value+"\n"), 0o644); err == nil {
			restored = true
		}
	}
	return restored
}

func readMeta(dir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.ID == "" {
		return nil, errors.New("快照索引缺少 ID")
	}
	return &meta, nil
}

func readSystemState(dir string) (*systemState, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var state systemState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func run(name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("缺少命令 %s", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	}
	return hex.EncodeToString(buf)
}
