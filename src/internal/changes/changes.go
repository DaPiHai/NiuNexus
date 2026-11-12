// Package changes implements the confirm-or-rollback watchdog used for every
// risky network mutation. An unconfirmed change is reverted automatically and
// is also recovered after a crash or restart.
package changes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"niunexus/internal/config"
	"niunexus/internal/snapshot"
)

// Status is the lifecycle state of a change.
type Status string

const (
	StatusApplied    Status = "applied"
	StatusPending    Status = "pending"
	StatusRolledBack Status = "rolled_back"
	StatusFailed     Status = "failed"
	StatusNone       Status = "none"
)

// Undo is the persistence-friendly description of how to revert a change
// after the core process has restarted.
type Undo struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Spec describes one mutation.
type Spec struct {
	Action   string
	Target   string
	Actor    string
	Preview  string
	Risky    bool
	Timeout  time.Duration
	Apply    func() error
	Rollback func() error
	Confirm  func() error
	UndoFn   func() (*Undo, error)
}

// Result is returned to the web layer.
type Result struct {
	ChangeID         string     `json:"changeId,omitempty"`
	Status           Status     `json:"status"`
	Action           string     `json:"action"`
	Target           string     `json:"target,omitempty"`
	Preview          string     `json:"preview,omitempty"`
	SnapshotID       string     `json:"snapshotId,omitempty"`
	RollbackDeadline *time.Time `json:"rollbackDeadline,omitempty"`
	Message          string     `json:"message,omitempty"`
}

// AuditFunc receives one line for the audit log.
type AuditFunc func(changeID, action, target, actor, result, message string)

type pendingChange struct {
	id         string
	action     string
	target     string
	actor      string
	snapshotID string
	deadline   time.Time
	undo       *Undo
	rollback   func() error
	confirm    func() error
	timer      *time.Timer
}

type pendingFile struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Actor      string    `json:"actor"`
	SnapshotID string    `json:"snapshotId"`
	Deadline   time.Time `json:"deadline"`
	Undo       *Undo     `json:"undo,omitempty"`
}

// Manager owns at most one pending risky change at a time.
type Manager struct {
	applyMu   sync.Mutex
	mu        sync.Mutex
	snapshots *snapshot.Manager
	store     *config.Store
	pending   string
	current   *pendingChange
	undoMu    sync.RWMutex
	undo      map[string]func(json.RawMessage) error
	audit     AuditFunc
}

// New creates a change manager.
func New(snapshots *snapshot.Manager, store *config.Store, varDir string) *Manager {
	return &Manager{
		snapshots: snapshots,
		store:     store,
		pending:   filepath.Join(varDir, "pending-change.json"),
		undo:      map[string]func(json.RawMessage) error{},
	}
}

// SetAudit installs the audit callback.
func (m *Manager) SetAudit(fn AuditFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = fn
}

// RegisterUndo installs a recovery handler for a persisted undo kind.
func (m *Manager) RegisterUndo(kind string, fn func(json.RawMessage) error) {
	m.undoMu.Lock()
	defer m.undoMu.Unlock()
	m.undo[kind] = fn
}

// Run applies a change and either commits it or leaves it pending for the
// caller to confirm.
func (m *Manager) Run(ctx context.Context, spec Spec) (*Result, error) {
	if spec.Apply == nil {
		return nil, errors.New("变更缺少 Apply 函数")
	}
	if spec.Risky && spec.UndoFn == nil {
		return nil, errors.New("高风险变更必须提供可持久化的 Undo")
	}
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	if m.current != nil {
		current := m.current
		m.mu.Unlock()
		return nil, fmt.Errorf("已有待确认的变更（%s，截止 %s），请先确认或回滚", current.action, current.deadline.Format(time.RFC3339))
	}
	m.mu.Unlock()

	snap, err := m.snapshots.Create(spec.Action, spec.Actor)
	if err != nil {
		return nil, fmt.Errorf("创建变更快照失败: %w", err)
	}

	if err := spec.Apply(); err != nil {
		m.rollbackAfterFailure(spec, snap.ID)
		m.emit("", spec.Action, spec.Target, spec.Actor, "failed", err.Error())
		return nil, fmt.Errorf("应用变更失败，已尝试回滚: %w", err)
	}

	if !spec.Risky {
		m.emit("", spec.Action, spec.Target, spec.Actor, "success", spec.Preview)
		return &Result{
			Status:     StatusApplied,
			Action:     spec.Action,
			Target:     spec.Target,
			Preview:    spec.Preview,
			SnapshotID: snap.ID,
		}, nil
	}

	var undo *Undo
	if spec.UndoFn != nil {
		undo, err = spec.UndoFn()
		if err != nil {
			m.rollbackAfterFailure(spec, snap.ID)
			return nil, fmt.Errorf("生成回滚信息失败: %w", err)
		}
	}
	changeID := newID()
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	deadline := time.Now().Add(timeout)
	entry := &pendingChange{
		id:         changeID,
		action:     spec.Action,
		target:     spec.Target,
		actor:      spec.Actor,
		snapshotID: snap.ID,
		deadline:   deadline,
		undo:       undo,
		rollback:   spec.Rollback,
		confirm:    spec.Confirm,
	}

	m.mu.Lock()
	m.current = entry
	m.writePendingLocked(entry)
	m.mu.Unlock()

	entry.timer = time.AfterFunc(timeout, func() {
		if _, err := m.Rollback(changeID, "确认超时"); err != nil {
			log.Printf("automatic rollback %s failed: %v", changeID, err)
		}
	})

	m.emit(changeID, spec.Action, spec.Target, spec.Actor, "pending", fmt.Sprintf("等待确认，截止 %s", deadline.Format(time.RFC3339)))
	return &Result{
		ChangeID:         changeID,
		Status:           StatusPending,
		Action:           spec.Action,
		Target:           spec.Target,
		Preview:          spec.Preview,
		SnapshotID:       snap.ID,
		RollbackDeadline: &deadline,
		Message:          "变更已应用，请在截止时间前确认，否则自动回滚",
	}, nil
}

// Confirm keeps a pending change.
func (m *Manager) Confirm(id string) (*Result, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	entry := m.current
	if entry == nil || entry.id != id {
		m.mu.Unlock()
		return nil, errors.New("没有匹配的待确认变更")
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	m.mu.Unlock()

	if entry.confirm != nil {
		if err := entry.confirm(); err != nil {
			m.rollbackInternal(entry, "确认时执行提交动作失败")
			return nil, fmt.Errorf("确认变更失败，已回滚: %w", err)
		}
	}

	m.mu.Lock()
	if m.current == entry {
		m.current = nil
		m.removePendingLocked()
	}
	m.mu.Unlock()

	m.emit(entry.id, entry.action, entry.target, entry.actor, "success", "已确认并保留变更")
	return &Result{
		ChangeID:   entry.id,
		Status:     StatusApplied,
		Action:     entry.action,
		Target:     entry.target,
		SnapshotID: entry.snapshotID,
		Message:    "变更已确认",
	}, nil
}

// Rollback reverts a pending change immediately.
func (m *Manager) Rollback(id, reason string) (*Result, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	entry := m.current
	if entry == nil || entry.id != id {
		m.mu.Unlock()
		return nil, errors.New("没有匹配的待确认变更")
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	m.mu.Unlock()
	return m.rollbackInternal(entry, reason), nil
}

// Pending returns the current pending change, if any.
func (m *Manager) Pending() *Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.current
	if entry == nil {
		return &Result{Status: StatusNone}
	}
	deadline := entry.deadline
	return &Result{
		ChangeID:         entry.id,
		Status:           StatusPending,
		Action:           entry.action,
		Target:           entry.target,
		SnapshotID:       entry.snapshotID,
		RollbackDeadline: &deadline,
		Message:          "等待确认",
	}
}

// Recover rolls back an unconfirmed change left by a previous process.
func (m *Manager) Recover(ctx context.Context) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	data, err := os.ReadFile(m.pending)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var pending pendingFile
	if err := json.Unmarshal(data, &pending); err != nil {
		_ = os.Remove(m.pending)
		return fmt.Errorf("待恢复变更文件格式错误: %w", err)
	}
	// Restore the document before running the controller undo: a recreated
	// virtual link must be able to save its fresh UUIDs without being
	// overwritten by the snapshot.
	if _, err := m.snapshots.Restore(pending.SnapshotID); err != nil {
		log.Printf("recovery: restore snapshot %s failed: %v", pending.SnapshotID, err)
	} else {
		m.store.Reload()
	}
	if pending.Undo != nil {
		m.undoMu.RLock()
		handler := m.undo[pending.Undo.Kind]
		m.undoMu.RUnlock()
		if handler == nil {
			log.Printf("recovery: no undo handler for kind %q", pending.Undo.Kind)
		} else if err := handler(pending.Undo.Data); err != nil {
			log.Printf("recovery: undo %s failed: %v", pending.Undo.Kind, err)
		}
	}
	_ = os.Remove(m.pending)
	m.emit(pending.ID, pending.Action, pending.Target, pending.Actor, "rolled_back", "启动时恢复未确认变更")
	return nil
}

// Shutdown rolls back a pending change before the process exits.
func (m *Manager) Shutdown(ctx context.Context) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	entry := m.current
	if entry != nil && entry.timer != nil {
		entry.timer.Stop()
	}
	m.mu.Unlock()
	if entry != nil {
		m.rollbackInternal(entry, "应用停止")
	}
}

func (m *Manager) rollbackAfterFailure(spec Spec, snapshotID string) {
	// Restore the document before the controller undo so a recreated virtual
	// link can save fresh UUIDs without being overwritten by the snapshot.
	if _, err := m.snapshots.Restore(snapshotID); err != nil {
		log.Printf("restore after failed apply: %v", err)
	} else {
		m.store.Reload()
	}
	if spec.Rollback != nil {
		if err := spec.Rollback(); err != nil {
			log.Printf("rollback after failed apply: %v", err)
		}
	}
}

// rollbackInternal assumes applyMu is held.
func (m *Manager) rollbackInternal(entry *pendingChange, reason string) *Result {
	var warnings []string
	if _, err := m.snapshots.Restore(entry.snapshotID); err != nil {
		warnings = append(warnings, "快照恢复失败: "+err.Error())
	} else {
		m.store.Reload()
	}
	if entry.rollback != nil {
		if err := entry.rollback(); err != nil {
			warnings = append(warnings, "控制器回滚失败: "+err.Error())
		}
	}

	m.mu.Lock()
	if m.current == entry {
		m.current = nil
		m.removePendingLocked()
	}
	m.mu.Unlock()

	message := reason
	if len(warnings) > 0 {
		message += "；" + join(warnings)
	}
	status := StatusRolledBack
	if len(warnings) > 0 {
		status = StatusFailed
	}
	m.emit(entry.id, entry.action, entry.target, entry.actor, string(status), message)
	return &Result{
		ChangeID:   entry.id,
		Status:     status,
		Action:     entry.action,
		Target:     entry.target,
		SnapshotID: entry.snapshotID,
		Message:    message,
	}
}

func (m *Manager) writePendingLocked(entry *pendingChange) {
	payload := pendingFile{
		ID:         entry.id,
		Action:     entry.action,
		Target:     entry.target,
		Actor:      entry.actor,
		SnapshotID: entry.snapshotID,
		Deadline:   entry.deadline,
		Undo:       entry.undo,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Printf("encode pending change: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.pending), 0o750); err != nil {
		log.Printf("create pending dir: %v", err)
		return
	}
	tmp := m.pending + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("write pending change: %v", err)
		return
	}
	if err := os.Rename(tmp, m.pending); err != nil {
		log.Printf("replace pending change: %v", err)
	}
}

func (m *Manager) removePendingLocked() {
	_ = os.Remove(m.pending)
}

func (m *Manager) emit(changeID, action, target, actor, result, message string) {
	m.mu.Lock()
	fn := m.audit
	m.mu.Unlock()
	if fn != nil {
		fn(changeID, action, target, actor, result, message)
	}
}

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("chg-%d", time.Now().UnixNano())
	}
	return "chg-" + hex.EncodeToString(buf)
}

func join(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += "；"
		}
		out += value
	}
	return out
}
