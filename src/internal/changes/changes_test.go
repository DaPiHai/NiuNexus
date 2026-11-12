package changes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"niunexus/internal/config"
	"niunexus/internal/snapshot"
)

type auditRecorder struct {
	mu      sync.Mutex
	entries []string
}

func (a *auditRecorder) fn(changeID, action, target, actor, result, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, action+":"+result)
}

func setup(t *testing.T) (*Manager, *config.Store, string, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := config.Open(configDir)
	snaps := snapshot.NewWithOptions(configDir, varDir, true)
	manager := New(snaps, store, varDir)
	return manager, store, configDir, varDir
}

func TestNonRiskyChangeCommits(t *testing.T) {
	manager, _, _, _ := setup(t)
	recorder := &auditRecorder{}
	manager.SetAudit(recorder.fn)

	applied := false
	result, err := manager.Run(context.Background(), Spec{
		Action: "noop",
		Target: "x",
		Actor:  "admin",
		Apply: func() error {
			applied = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied || result.Status != StatusApplied {
		t.Fatalf("unexpected result: %+v applied=%v", result, applied)
	}
	if len(recorder.entries) != 1 || recorder.entries[0] != "noop:success" {
		t.Fatalf("audit = %#v", recorder.entries)
	}
}

func TestFailedApplyRollsBack(t *testing.T) {
	manager, _, _, _ := setup(t)
	rolledBack := false
	_, err := manager.Run(context.Background(), Spec{
		Action: "fail",
		Actor:  "admin",
		Apply: func() error {
			return os.ErrInvalid
		},
		Rollback: func() error {
			rolledBack = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected apply error")
	}
	if !rolledBack {
		t.Fatal("rollback closure was not called")
	}
}

func TestRiskyChangeConfirm(t *testing.T) {
	manager, _, _, varDir := setup(t)
	undoCalled := false
	result, err := manager.Run(context.Background(), Spec{
		Action:  "interface.apply",
		Target:  "eth0",
		Actor:   "admin",
		Risky:   true,
		Timeout: time.Hour,
		Apply:   func() error { return nil },
		UndoFn: func() (*Undo, error) {
			return &Undo{Kind: "noop", Data: json.RawMessage(`{}`)}, nil
		},
		Confirm: func() error { return nil },
		Rollback: func() error {
			undoCalled = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusPending || result.ChangeID == "" || result.RollbackDeadline == nil {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(varDir, "pending-change.json")); err != nil {
		t.Fatalf("pending file missing: %v", err)
	}
	confirmed, err := manager.Confirm(result.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != StatusApplied {
		t.Fatalf("status = %s", confirmed.Status)
	}
	if undoCalled {
		t.Fatal("rollback must not run on confirm")
	}
	if _, err := os.Stat(filepath.Join(varDir, "pending-change.json")); !os.IsNotExist(err) {
		t.Fatalf("pending file still exists: %v", err)
	}
}

func TestRiskyChangeTimeoutRollsBack(t *testing.T) {
	manager, _, _, _ := setup(t)
	rolledBack := make(chan struct{}, 1)
	result, err := manager.Run(context.Background(), Spec{
		Action:  "interface.apply",
		Actor:   "admin",
		Risky:   true,
		Timeout: 80 * time.Millisecond,
		Apply:   func() error { return nil },
		UndoFn: func() (*Undo, error) {
			return &Undo{Kind: "noop", Data: json.RawMessage(`{}`)}, nil
		},
		Rollback: func() error {
			select {
			case rolledBack <- struct{}{}:
			default:
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-rolledBack:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic rollback did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if manager.Pending().Status == StatusNone {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pending change was not cleared")
	_ = result
}

func TestSecondRiskyChangeRejected(t *testing.T) {
	manager, _, _, _ := setup(t)
	first, err := manager.Run(context.Background(), Spec{
		Action:  "first",
		Actor:   "admin",
		Risky:   true,
		Timeout: time.Hour,
		Apply:   func() error { return nil },
		UndoFn: func() (*Undo, error) {
			return &Undo{Kind: "noop"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background(), Spec{
		Action: "second",
		Actor:  "admin",
		Risky:  true,
		Apply:  func() error { return nil },
		UndoFn: func() (*Undo, error) {
			return &Undo{Kind: "noop"}, nil
		},
	}); err == nil {
		t.Fatal("expected second risky change to be rejected")
	}
	if _, err := manager.Rollback(first.ChangeID, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRollsBackPendingChange(t *testing.T) {
	manager, store, configDir, varDir := setup(t)
	recovered := false
	manager.RegisterUndo("interface", func(data json.RawMessage) error {
		recovered = true
		return nil
	})
	_, err := manager.Run(context.Background(), Spec{
		Action:  "interface.apply",
		Actor:   "admin",
		Risky:   true,
		Timeout: time.Hour,
		Apply:   func() error { return nil },
		UndoFn: func() (*Undo, error) {
			return &Undo{Kind: "interface", Data: json.RawMessage(`{"ifname":"eth0"}`)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a new process: a fresh manager reads the same pending file.
	fresh := New(snapshot.NewWithOptions(configDir, varDir, true), store, varDir)
	fresh.RegisterUndo("interface", func(data json.RawMessage) error {
		recovered = true
		return nil
	})
	if err := fresh.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("undo handler was not invoked")
	}
	if _, err := os.Stat(filepath.Join(varDir, "pending-change.json")); !os.IsNotExist(err) {
		t.Fatalf("pending file should be removed: %v", err)
	}
}
