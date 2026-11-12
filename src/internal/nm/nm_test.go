package nm

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	out   string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.out), nil
}

func TestParseConnectionsAndEscaping(t *testing.T) {
	out := "uuid-1:Wired connection 1:802-3-ethernet:eth0\nuuid-2:Office\\:LAN:802-3-ethernet:\n"
	list := parseConnections(out)
	if len(list) != 2 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].UUID != "uuid-1" || list[0].Device != "eth0" || !list[0].Active {
		t.Fatalf("unexpected first: %+v", list[0])
	}
	if list[1].Name != "Office:LAN" || list[1].Active {
		t.Fatalf("unexpected second: %+v", list[1])
	}
}

func TestActiveForDevice(t *testing.T) {
	runner := &fakeRunner{out: "u1:Wired:802-3-ethernet:eth0\nu2:Other:802-3-ethernet:eth1\n"}
	client := New(runner)
	conn, err := client.ActiveForDevice(context.Background(), "eth1")
	if err != nil {
		t.Fatal(err)
	}
	if conn.UUID != "u2" {
		t.Fatalf("uuid = %s", conn.UUID)
	}
	if _, err := client.ActiveForDevice(context.Background(), "eth9"); err == nil {
		t.Fatal("expected error for missing device")
	}
}

func TestCloneFindsNewUUID(t *testing.T) {
	runner := &fakeRunner{out: "old:Original:802-3-ethernet:eth0\nnew:niunexus-eth0:802-3-ethernet:\n"}
	client := New(runner)
	uuid, err := client.Clone(context.Background(), "old", "niunexus-eth0")
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "new" {
		t.Fatalf("uuid = %s", uuid)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	if got := runner.calls[0][1:]; !reflect.DeepEqual(got, []string{"connection", "clone", "old", "niunexus-eth0"}) {
		t.Fatalf("unexpected clone args: %#v", got)
	}
}

func TestModifyRequiresPairs(t *testing.T) {
	client := New(&fakeRunner{})
	if err := client.Modify(context.Background(), "u", "ipv4.method"); err == nil {
		t.Fatal("expected pair validation error")
	}
}

func TestAddVlanArgs(t *testing.T) {
	runner := &fakeRunner{out: "v:niunexus-eth0.100:vlan:eth0.100\n"}
	client := New(runner)
	uuid, err := client.AddVlan(context.Background(), "niunexus-eth0.100", "eth0.100", "eth0", 100)
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "v" {
		t.Fatalf("uuid = %s", uuid)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	joined := strings.Join(runner.calls[0], " ")
	for _, want := range []string{"type vlan", "con-name niunexus-eth0.100", "ifname eth0.100", "dev eth0", "id 100"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestMTUProperty(t *testing.T) {
	for _, kind := range []string{"802-3-ethernet", "bridge", "vlan", "bond"} {
		if got := MTUProperty(kind); got != "802-3-ethernet.mtu" {
			t.Fatalf("%s mtu = %s", kind, got)
		}
	}
	if got := MTUProperty("802-11-wireless"); got != "802-11-wireless.mtu" {
		t.Fatalf("wifi mtu = %s", got)
	}
	if got := MTUProperty("unknown"); got != "" {
		t.Fatalf("unknown mtu = %s", got)
	}
}
