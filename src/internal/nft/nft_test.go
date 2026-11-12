package nft

import (
	"context"
	"errors"
	"strings"
	"testing"

	"niunexus/internal/config"
)

type fakeRunner struct {
	exists bool
	calls  [][]string
	stdin  []byte
	err    error
	json   string
}

func (f *fakeRunner) Run(_ context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	if len(args) >= 2 && args[0] == "list" && args[1] == "table" {
		if !f.exists {
			return nil, errors.New("nft list: No such file or directory")
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

func sampleConfig() *config.Config {
	cfg := config.Defaults()
	cfg.PortForwards = []config.PortForward{{
		ID:       "pf-rule1",
		Name:     "Web",
		Enabled:  true,
		Protocol: "tcp",
		WAN:      "eth0",
		External: "8080",
		DestIP:   "192.168.1.10",
		DestPort: "80",
	}}
	return cfg
}

func TestRenderPortForward(t *testing.T) {
	script, active := Render(sampleConfig())
	if !active {
		t.Fatal("expected active table")
	}
	for _, want := range []string{
		"table inet niunexus",
		"counter pf_pf_rule1",
		`iifname "eth0" tcp dport 8080 ct mark set 0x4e58 counter name pf_pf_rule1 dnat ip to 192.168.1.10:80`,
		"ct mark 0x4e58 accept",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
	if strings.Contains(script, "masquerade") {
		t.Fatalf("unexpected masquerade:\n%s", script)
	}
}

func TestRenderRangesBothAndHairpin(t *testing.T) {
	cfg := sampleConfig()
	cfg.PortForwards[0].Protocol = "both"
	cfg.PortForwards[0].External = "8000-8010"
	cfg.PortForwards[0].DestPort = "9000-9010"
	cfg.PortForwards[0].Hairpin = true
	cfg.NAT = config.NATConfig{Enabled: true, Interfaces: []string{"eth0"}}
	script, _ := Render(cfg)
	for _, want := range []string{
		"tcp dport 8000-8010 ct mark set 0x4e59",
		"udp dport 8000-8010 ct mark set 0x4e59",
		"dnat ip to 192.168.1.10:9000-9010",
		`oifname "eth0" masquerade`,
		"ct mark 0x4e59 masquerade",
		`oifname "eth0" ct state new,established,related accept`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	script, active := Render(config.Defaults())
	if active || script != "" {
		t.Fatalf("expected empty render, got active=%v script=%q", active, script)
	}
}

func TestCounterName(t *testing.T) {
	if got := CounterName("pf-abc-123"); got != "pf_pf_abc_123" {
		t.Fatalf("counter = %q", got)
	}
	if got := CounterName("a:b c"); got != "pf_a_b_c" {
		t.Fatalf("counter = %q", got)
	}
}

func TestApplyCreatesTable(t *testing.T) {
	runner := &fakeRunner{exists: false}
	client := New(runner)
	if err := client.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runner.stdin), "table inet niunexus") {
		t.Fatalf("stdin = %s", runner.stdin)
	}
	if strings.Contains(string(runner.stdin), "delete table") {
		t.Fatalf("unexpected delete on a missing table:\n%s", runner.stdin)
	}
}

func TestApplyReplacesTable(t *testing.T) {
	runner := &fakeRunner{exists: true}
	client := New(runner)
	if err := client.Apply(context.Background(), sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(runner.stdin), "delete table inet niunexus") {
		t.Fatalf("stdin = %s", runner.stdin)
	}
}

func TestApplyDeletesWhenEmpty(t *testing.T) {
	runner := &fakeRunner{exists: true}
	client := New(runner)
	if err := client.Apply(context.Background(), config.Defaults()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "delete table inet niunexus") {
			found = true
		}
	}
	if !found {
		t.Fatalf("delete call missing: %#v", runner.calls)
	}
}

func TestCounters(t *testing.T) {
	runner := &fakeRunner{exists: true, json: `{"nftables":[{"counter":{"name":"pf_pf_rule1","packets":12,"bytes":3456}},{"counter":{"name":"other","packets":1,"bytes":2}}]}`}
	client := New(runner)
	counters, err := client.Counters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counters["pf_pf_rule1"].Packets != 12 || counters["pf_pf_rule1"].Bytes != 3456 {
		t.Fatalf("counters = %#v", counters)
	}
}
