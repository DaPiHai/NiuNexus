package routing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/snapshot"
)

type fakeRunner struct {
	calls      [][]string
	jsonRoutes string
	jsonRules  string
	failOn     string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-j route show") {
		return []byte(f.jsonRoutes), nil
	}
	if strings.Contains(joined, "-j rule show") {
		return []byte(f.jsonRules), nil
	}
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		return nil, errors.New("command failed")
	}
	return nil, nil
}

func setup(t *testing.T) (*Controller, *config.Store, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	store := config.Open(configDir)
	snaps := snapshot.NewWithOptions(configDir, varDir, true)
	manager := changes.New(snaps, store, varDir)
	fake := &fakeRunner{}
	controller := New(fake, store, manager, varDir)
	return controller, store, fake
}

func TestAddAndDeleteStaticRoute(t *testing.T) {
	controller, store, fake := setup(t)
	result, err := controller.AddRoute(context.Background(), RouteRequest{
		Destination: "10.0.0.0/24",
		Gateway:     "172.21.240.1",
		Device:      "eth0",
		Metric:      50,
	}, "tester")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if result.Status != changes.StatusApplied {
		t.Fatalf("status = %s", result.Status)
	}
	if len(store.Config().StaticRoutes) != 1 {
		t.Fatalf("routes = %+v", store.Config().StaticRoutes)
	}
	joined := commands(fake)
	if !strings.Contains(joined, "route replace 10.0.0.0/24 via 172.21.240.1 dev eth0 metric 50") {
		t.Fatalf("missing replace:\n%s", joined)
	}
	if _, err := controller.DeleteRoute(context.Background(), store.Config().StaticRoutes[0].ID, "tester"); err != nil {
		t.Fatal(err)
	}
	if len(store.Config().StaticRoutes) != 0 {
		t.Fatal("route not removed")
	}
	if !strings.Contains(commands(fake), "route del 10.0.0.0/24 via 172.21.240.1 dev eth0 metric 50") {
		t.Fatalf("missing delete:\n%s", commands(fake))
	}
}

func TestPolicyCreateConfirmAndDelete(t *testing.T) {
	controller, store, fake := setup(t)
	result, err := controller.AddPolicy(context.Background(), PolicyRequest{
		Name:     "LAN via WAN",
		Source:   "192.168.50.0/24",
		WAN:      "eth0",
		Gateway:  "172.21.240.1",
		Table:    100,
		Priority: 1000,
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != changes.StatusPending || result.ChangeID == "" {
		t.Fatalf("expected pending, got %+v", result)
	}
	if _, err := controller.changes.Confirm(result.ChangeID); err != nil {
		t.Fatal(err)
	}
	if len(store.Config().PolicyRules) != 1 {
		t.Fatalf("rules = %+v", store.Config().PolicyRules)
	}
	joined := commands(fake)
	for _, want := range []string{
		"rule add from 192.168.50.0/24 table 100 priority 1000",
		"route replace default via 172.21.240.1 dev eth0 table 100",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}

	ruleID := store.Config().PolicyRules[0].ID
	deleted, err := controller.DeletePolicy(context.Background(), ruleID, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != changes.StatusPending {
		t.Fatalf("expected pending delete, got %+v", deleted)
	}
	if _, err := controller.changes.Confirm(deleted.ChangeID); err != nil {
		t.Fatal(err)
	}
	if len(store.Config().PolicyRules) != 0 {
		t.Fatal("policy rule not removed")
	}
	joined = commands(fake)
	if !strings.Contains(joined, "rule del from 192.168.50.0/24 table 100 priority 1000") {
		t.Fatalf("missing rule delete:\n%s", joined)
	}
	if !strings.Contains(joined, "route del default table 100") {
		t.Fatalf("missing table route delete:\n%s", joined)
	}
}

func TestApplyFailureRollsBackRoute(t *testing.T) {
	controller, store, fake := setup(t)
	fake.failOn = "route replace"
	if _, err := controller.AddRoute(context.Background(), RouteRequest{
		Destination: "10.1.0.0/24",
		Gateway:     "172.21.240.1",
		Device:      "eth0",
		Metric:      10,
	}, "tester"); err == nil {
		t.Fatal("expected failure")
	}
	if len(store.Config().StaticRoutes) != 0 {
		t.Fatal("failed add must not persist")
	}
}

func TestListLiveState(t *testing.T) {
	controller, store, fake := setup(t)
	if _, err := store.Update(func(cfg *config.Config) error {
		cfg.StaticRoutes = []config.StaticRoute{{
			ID:          "rt-1",
			Destination: "10.0.0.0/24",
			Gateway:     "172.21.240.1",
			Device:      "eth0",
			Metric:      50,
		}}
		cfg.PolicyRules = []config.PolicyRule{{
			ID:       "pr-1",
			Source:   "192.168.50.0/24",
			WAN:      "eth0",
			Gateway:  "172.21.240.1",
			Table:    100,
			Priority: 1000,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fake.jsonRoutes = `[{"dst":"10.0.0.0/24","gateway":"172.21.240.1","dev":"eth0","metric":50,"table":254}]`
	fake.jsonRules = `[{"priority":1000,"src":"192.168.50.0/24","table":100}]`
	snapshot, err := controller.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.LiveRoutes) != 1 || len(snapshot.LiveRules) != 1 {
		t.Fatalf("live = %+v %+v", snapshot.LiveRoutes, snapshot.LiveRules)
	}
	if !snapshot.Routes[0].Present || !snapshot.Rules[0].Present {
		t.Fatalf("presence flags = %+v %+v", snapshot.Routes, snapshot.Rules)
	}
}

func commands(f *fakeRunner) string {
	var b strings.Builder
	for _, call := range f.calls {
		b.WriteString(strings.Join(call, " "))
		b.WriteByte('\n')
	}
	return b.String()
}
