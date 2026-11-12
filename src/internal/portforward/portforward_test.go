package portforward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/nft"
	"niunexus/internal/snapshot"
	"niunexus/internal/sysctl"
)

type fakeNFT struct {
	exists bool
	stdin  []byte
	json   string
	err    error
}

func (f *fakeNFT) Run(_ context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "list" && args[1] == "table" {
		if !f.exists {
			return nil, errors.New("No such file or directory")
		}
		return []byte(f.json), nil
	}
	if len(args) >= 1 && args[0] == "-f" {
		if f.err != nil {
			return nil, f.err
		}
		f.stdin = append([]byte(nil), stdin...)
		return nil, nil
	}
	if len(args) >= 1 && args[0] == "delete" {
		return nil, nil
	}
	if len(args) >= 1 && args[0] == "-j" {
		return []byte(f.json), nil
	}
	return nil, nil
}

func setup(t *testing.T) (*Controller, *config.Store, *fakeNFT, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	proc := filepath.Join(root, "ip_forward")
	if err := os.WriteFile(proc, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := config.Open(configDir)
	snaps := snapshot.NewWithOptions(configDir, varDir, true)
	manager := changes.New(snaps, store, varDir)
	fake := &fakeNFT{}
	controller := New(nft.New(fake), sysctl.NewWithPaths(varDir, proc), store, manager)
	return controller, store, fake, proc
}

func sampleRequest() Request {
	return Request{
		Name:     "web",
		Enabled:  true,
		Protocol: "tcp",
		WAN:      "eth0",
		External: "8080",
		DestIP:   "192.168.1.10",
		DestPort: "80",
	}
}

func TestAddListToggleDelete(t *testing.T) {
	controller, store, fake, _ := setup(t)
	result, err := controller.Add(context.Background(), sampleRequest(), "tester")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if result.Status != changes.StatusApplied {
		t.Fatalf("status = %s", result.Status)
	}
	rules := store.Config().PortForwards
	if len(rules) != 1 || !rules[0].Enabled {
		t.Fatalf("rules = %+v", rules)
	}
	if !strings.Contains(string(fake.stdin), "dnat ip to 192.168.1.10:80") {
		t.Fatalf("nft stdin = %s", fake.stdin)
	}

	fake.exists = true
	fake.json = `{"nftables":[{"counter":{"name":"` + nft.CounterName(rules[0].ID) + `","packets":7,"bytes":700}}]}`
	snapshot, err := controller.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rules) != 1 || snapshot.Rules[0].Counter.Packets != 7 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	if _, err := controller.Toggle(context.Background(), rules[0].ID, false, "tester"); err != nil {
		t.Fatal(err)
	}
	if store.Config().PortForwards[0].Enabled {
		t.Fatal("rule should be disabled")
	}

	if _, err := controller.Delete(context.Background(), rules[0].ID, "tester"); err != nil {
		t.Fatal(err)
	}
	if len(store.Config().PortForwards) != 0 {
		t.Fatal("rule should be deleted")
	}
}

func TestOverlapRejected(t *testing.T) {
	controller, store, _, _ := setup(t)
	if _, err := controller.Add(context.Background(), sampleRequest(), "tester"); err != nil {
		t.Fatal(err)
	}
	second := sampleRequest()
	second.Name = "second"
	second.External = "8080-8081"
	second.DestPort = "80-81"
	if _, err := controller.Add(context.Background(), second, "tester"); err == nil {
		t.Fatal("expected overlap error")
	}
	if len(store.Config().PortForwards) != 1 {
		t.Fatalf("rules = %d", len(store.Config().PortForwards))
	}
}

func TestNATForwardingToggle(t *testing.T) {
	controller, store, _, proc := setup(t)
	if _, err := controller.UpdateNAT(context.Background(), config.NATConfig{
		Enabled:    true,
		Interfaces: []string{"eth0"},
	}, "tester"); err != nil {
		t.Fatal(err)
	}
	value, _ := os.ReadFile(proc)
	if strings.TrimSpace(string(value)) != "1" {
		t.Fatalf("forwarding = %q", value)
	}
	if !store.Config().NAT.Enabled {
		t.Fatal("NAT should be enabled")
	}
	if _, err := controller.UpdateNAT(context.Background(), config.NATConfig{
		Enabled:    false,
		Interfaces: []string{"eth0"},
	}, "tester"); err != nil {
		t.Fatal(err)
	}
	value, _ = os.ReadFile(proc)
	if strings.TrimSpace(string(value)) != "0" {
		t.Fatalf("forwarding not restored: %q", value)
	}
}

func TestApplyFailureRollsBackConfig(t *testing.T) {
	controller, store, fake, _ := setup(t)
	fake.err = errors.New("nft denied")
	if _, err := controller.Add(context.Background(), sampleRequest(), "tester"); err == nil {
		t.Fatal("expected apply failure")
	}
	if len(store.Config().PortForwards) != 0 {
		t.Fatal("failed add must not persist")
	}
}
