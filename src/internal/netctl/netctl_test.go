package netctl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"niunexus/internal/changes"
	"niunexus/internal/config"
	"niunexus/internal/nm"
	"niunexus/internal/snapshot"
)

type fakeNM struct {
	profiles map[string]nm.Connection // by name
	calls    []string
	next     int
	cloneErr error
}

func newFakeNM() *fakeNM {
	return &fakeNM{profiles: map[string]nm.Connection{
		"lo": {
			UUID:   "orig-lo",
			Name:   "lo",
			Type:   "loopback",
			Device: "lo",
			Active: true,
		},
		"eth0": {
			UUID:   "orig-eth0",
			Name:   "Wired connection 1",
			Type:   "802-3-ethernet",
			Device: "eth0",
			Active: true,
		},
	}}
}

func (f *fakeNM) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeNM) List(_ context.Context, activeOnly bool) ([]nm.Connection, error) {
	out := make([]nm.Connection, 0, len(f.profiles))
	for _, profile := range f.profiles {
		if activeOnly && !profile.Active {
			continue
		}
		out = append(out, profile)
	}
	return out, nil
}

func (f *fakeNM) ActiveForDevice(_ context.Context, ifname string) (*nm.Connection, error) {
	for _, profile := range f.profiles {
		if profile.Device == ifname && profile.Active {
			copyProfile := profile
			return &copyProfile, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", nm.ErrNoActiveConnection, ifname)
}

func (f *fakeNM) ByName(_ context.Context, name string) (*nm.Connection, error) {
	profile, ok := f.profiles[name]
	if !ok {
		return nil, errors.New("not found")
	}
	copyProfile := profile
	return &copyProfile, nil
}

func (f *fakeNM) Clone(_ context.Context, uuid, newName string) (string, error) {
	if f.cloneErr != nil {
		return "", f.cloneErr
	}
	var source nm.Connection
	found := false
	for _, profile := range f.profiles {
		if profile.UUID == uuid {
			source = profile
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("source missing")
	}
	f.next++
	clone := nm.Connection{
		UUID:   fmt.Sprintf("clone-%d", f.next),
		Name:   newName,
		Type:   source.Type,
		Active: false,
	}
	f.profiles[newName] = clone
	f.record("clone %s -> %s", uuid, newName)
	return clone.UUID, nil
}

func (f *fakeNM) Modify(_ context.Context, uuid string, props ...string) error {
	f.record("modify %s %s", uuid, strings.Join(props, " "))
	return nil
}

func (f *fakeNM) Up(_ context.Context, uuid string) error {
	for name, profile := range f.profiles {
		if profile.UUID == uuid {
			profile.Active = true
			profile.Device = name
			f.profiles[name] = profile
			f.record("up %s", uuid)
			return nil
		}
	}
	return errors.New("profile missing")
}

func (f *fakeNM) Down(_ context.Context, uuid string) error {
	for name, profile := range f.profiles {
		if profile.UUID == uuid {
			profile.Active = false
			profile.Device = ""
			f.profiles[name] = profile
			f.record("down %s", uuid)
			return nil
		}
	}
	return errors.New("profile missing")
}

func (f *fakeNM) Delete(_ context.Context, uuid string) error {
	for name, profile := range f.profiles {
		if profile.UUID == uuid {
			delete(f.profiles, name)
			f.record("delete %s", uuid)
			return nil
		}
	}
	return errors.New("profile missing")
}

func (f *fakeNM) add(name, ifname, kind string) (string, error) {
	f.next++
	uuid := fmt.Sprintf("new-%d", f.next)
	f.profiles[name] = nm.Connection{UUID: uuid, Name: name, Type: kind}
	f.record("add %s %s %s", kind, name, ifname)
	return uuid, nil
}

func (f *fakeNM) AddEthernet(_ context.Context, name, ifname string) (string, error) {
	return f.add(name, ifname, "802-3-ethernet")
}

func (f *fakeNM) AddBridge(_ context.Context, name, ifname string) (string, error) {
	return f.add(name, ifname, "bridge")
}

func (f *fakeNM) AddVlan(_ context.Context, name, ifname, parent string, id int) (string, error) {
	return f.add(name, ifname, "vlan")
}

func (f *fakeNM) AddBond(_ context.Context, name, ifname string) (string, error) {
	return f.add(name, ifname, "bond")
}

func (f *fakeNM) AddSlave(_ context.Context, name, ifname, masterUUID string) (string, error) {
	return f.add(name, ifname, "802-3-ethernet")
}

func testController(t *testing.T) (*Controller, *fakeNM, *config.Store) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "etc")
	varDir := filepath.Join(root, "var")
	store := config.Open(configDir)
	snaps := snapshot.NewWithOptions(configDir, varDir, true)
	manager := changes.New(snaps, store, varDir)
	fake := newFakeNM()
	controller := New(fake, store, manager)
	controller.SetVerifyTimeout(2 * time.Second)
	return controller, fake, store
}

func TestApplyInterfaceSuccessAndRevert(t *testing.T) {
	controller, fake, store := testController(t)
	result, err := controller.ApplyInterface(context.Background(), "lo", InterfaceRequest{
		IPv4Mode: "disabled",
		IPv6Mode: "ignore",
	}, "tester")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Status != changes.StatusApplied {
		t.Fatalf("status = %s", result.Status)
	}
	cfg := store.Config()
	if len(cfg.Interfaces) != 1 {
		t.Fatalf("interfaces = %d", len(cfg.Interfaces))
	}
	item := cfg.Interfaces[0]
	if item.ManagedID == "" || item.OriginalID != "orig-lo" || !item.Managed {
		t.Fatalf("unexpected managed entry: %+v", item)
	}
	joined := strings.Join(fake.calls, "\n")
	for _, want := range []string{"clone orig-lo -> niunexus-lo", "up " + item.ManagedID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in calls:\n%s", want, joined)
		}
	}

	revert, err := controller.RevertInterface(context.Background(), "lo", "tester")
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if revert.Status != changes.StatusApplied {
		t.Fatalf("revert status = %s", revert.Status)
	}
	if len(store.Config().Interfaces) != 0 {
		t.Fatal("managed interface entry was not removed")
	}
	if _, ok := fake.profiles["niunexus-lo"]; ok {
		t.Fatal("managed profile still exists")
	}
}

func TestApplyInterfaceCloneFailureRollsBack(t *testing.T) {
	controller, fake, store := testController(t)
	fake.cloneErr = errors.New("insufficient privileges")
	if _, err := controller.ApplyInterface(context.Background(), "lo", InterfaceRequest{
		IPv4Mode: "disabled",
		IPv6Mode: "ignore",
	}, "tester"); err == nil {
		t.Fatal("expected apply failure")
	}
	if len(store.Config().Interfaces) != 0 {
		t.Fatal("failed apply must not persist an interface entry")
	}
}

func TestBuildIPProperties(t *testing.T) {
	props, err := buildIPProperties("manual", "192.168.1.10/24", "192.168.1.1", []string{"1.1.1.1"}, "disabled", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(props, " ")
	for _, want := range []string{"ipv4.method manual", "ipv4.addresses 192.168.1.10/24", "ipv4.gateway 192.168.1.1", "ipv4.dns 1.1.1.1", "ipv6.method disabled"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if _, err := buildIPProperties("manual", "", "", nil, "auto", "", "", nil); err == nil {
		t.Fatal("expected missing address error")
	}
	if _, err := buildIPProperties("bogus", "", "", nil, "auto", "", "", nil); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestCreateLinkProfiles(t *testing.T) {
	controller, fake, _ := testController(t)
	link := config.VirtualLink{
		ID:       "vl-test",
		Type:     "bridge",
		Name:     "br-test",
		Members:  []string{"eth0"},
		STP:      true,
		IPv4Mode: "disabled",
		IPv6Mode: "ignore",
	}
	var master string
	var members []string
	if err := controller.createLinkProfiles(context.Background(), &link, &master, &members); err != nil {
		t.Fatalf("create profiles: %v", err)
	}
	if master == "" || len(members) != 1 {
		t.Fatalf("master=%q members=%v", master, members)
	}
	if link.MasterID != master || len(link.MemberIDs) != 1 {
		t.Fatalf("link not updated: %+v", link)
	}
	joined := strings.Join(fake.calls, "\n")
	for _, want := range []string{"add bridge niunexus-br-test br-test", "bridge.stp yes", "ipv4.method disabled"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in calls:\n%s", want, joined)
		}
	}
}

func TestDeleteVirtualLink(t *testing.T) {
	controller, fake, store := testController(t)
	link := config.VirtualLink{
		ID:        "vl-test",
		Type:      "bridge",
		Name:      "br-test",
		MasterID:  "master-1",
		MemberIDs: []string{"member-1"},
		IPv4Mode:  "disabled",
		IPv6Mode:  "ignore",
	}
	if _, err := store.Update(func(cfg *config.Config) error {
		cfg.VirtualLinks = append(cfg.VirtualLinks, link)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fake.profiles["niunexus-br-test"] = nm.Connection{UUID: "master-1", Name: "niunexus-br-test", Type: "bridge", Device: "br-test", Active: true}
	fake.profiles["niunexus-br-test-port-eth0"] = nm.Connection{UUID: "member-1", Name: "niunexus-br-test-port-eth0", Type: "802-3-ethernet"}

	result, err := controller.DeleteVirtualLink(context.Background(), "vl-test", "tester")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Status != changes.StatusApplied {
		t.Fatalf("status = %s", result.Status)
	}
	if len(store.Config().VirtualLinks) != 0 {
		t.Fatal("link config was not removed")
	}
	if _, ok := fake.profiles["niunexus-br-test"]; ok {
		t.Fatal("master profile still exists")
	}
}
