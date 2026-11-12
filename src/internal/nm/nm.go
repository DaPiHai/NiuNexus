// Package nm is a small, typed wrapper around the nmcli command line. All
// arguments are passed as argv elements (never through a shell) and every
// command runs with a context deadline.
package nm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNoActiveConnection means the interface exists but no profile is active.
var ErrNoActiveConnection = errors.New("no active NetworkManager connection")

// Runner executes one command and returns stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the production runner.
type ExecRunner struct {
	Timeout time.Duration
}

// Run executes a command with LC_ALL=C and captures stderr in the error.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Connection is one NetworkManager profile.
type Connection struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Device string `json:"device,omitempty"`
	Active bool   `json:"active"`
}

// Client wraps nmcli.
type Client struct {
	Bin    string
	Runner Runner
}

// New creates a client using the default runner.
func New(runner Runner) *Client {
	if runner == nil {
		runner = ExecRunner{}
	}
	bin := "nmcli"
	if path, err := exec.LookPath("nmcli"); err == nil {
		bin = path
	} else if _, err := os.Stat("/usr/bin/nmcli"); err == nil {
		bin = "/usr/bin/nmcli"
	}
	return &Client{Bin: bin, Runner: runner}
}

// List returns connection profiles. activeOnly filters to active profiles.
func (c *Client) List(ctx context.Context, activeOnly bool) ([]Connection, error) {
	args := []string{"-t", "-f", "UUID,NAME,TYPE,DEVICE", "connection", "show"}
	if activeOnly {
		args = append(args, "--active")
	}
	out, err := c.Runner.Run(ctx, c.Bin, args...)
	if err != nil {
		return nil, err
	}
	return parseConnections(string(out)), nil
}

// ActiveForDevice returns the active profile attached to ifname.
func (c *Client) ActiveForDevice(ctx context.Context, ifname string) (*Connection, error) {
	list, err := c.List(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Device == ifname {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("%w: 网口 %s", ErrNoActiveConnection, ifname)
}

// ByName returns a profile with the exact name.
func (c *Client) ByName(ctx context.Context, name string) (*Connection, error) {
	list, err := c.List(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("连接不存在: %s", name)
}

// Clone copies an existing profile under a new name and returns the new UUID.
func (c *Client) Clone(ctx context.Context, uuid, newName string) (string, error) {
	if strings.TrimSpace(uuid) == "" || strings.TrimSpace(newName) == "" {
		return "", fmt.Errorf("clone 需要源 UUID 和新名称")
	}
	if _, err := c.Runner.Run(ctx, c.Bin, "connection", "clone", uuid, newName); err != nil {
		return "", err
	}
	conn, err := c.ByName(ctx, newName)
	if err != nil {
		return "", fmt.Errorf("克隆后找不到连接 %s: %w", newName, err)
	}
	return conn.UUID, nil
}

// Modify applies alternating property/value pairs.
func (c *Client) Modify(ctx context.Context, uuid string, props ...string) error {
	if strings.TrimSpace(uuid) == "" {
		return fmt.Errorf("modify 需要连接 UUID")
	}
	if len(props)%2 != 0 {
		return fmt.Errorf("modify 属性必须是成对的 property/value")
	}
	args := []string{"connection", "modify", uuid}
	args = append(args, props...)
	_, err := c.Runner.Run(ctx, c.Bin, args...)
	return err
}

// Up activates a profile.
func (c *Client) Up(ctx context.Context, uuid string) error {
	_, err := c.Runner.Run(ctx, c.Bin, "connection", "up", uuid)
	return err
}

// Down deactivates a profile.
func (c *Client) Down(ctx context.Context, uuid string) error {
	_, err := c.Runner.Run(ctx, c.Bin, "connection", "down", uuid)
	return err
}

// Delete removes a profile.
func (c *Client) Delete(ctx context.Context, uuid string) error {
	_, err := c.Runner.Run(ctx, c.Bin, "connection", "delete", uuid)
	return err
}

// AddEthernet creates a new ethernet profile.
func (c *Client) AddEthernet(ctx context.Context, name, ifname string) (string, error) {
	if err := c.add(ctx, "ethernet", name, ifname); err != nil {
		return "", err
	}
	return c.uuidByName(ctx, name)
}

// AddBridge creates a bridge profile.
func (c *Client) AddBridge(ctx context.Context, name, ifname string) (string, error) {
	if err := c.add(ctx, "bridge", name, ifname); err != nil {
		return "", err
	}
	return c.uuidByName(ctx, name)
}

// AddVlan creates a VLAN profile.
func (c *Client) AddVlan(ctx context.Context, name, ifname, parent string, vlanID int) (string, error) {
	if parent == "" || vlanID < 1 || vlanID > 4094 {
		return "", fmt.Errorf("VLAN 参数无效")
	}
	args := []string{"connection", "add", "type", "vlan", "con-name", name, "ifname", ifname, "dev", parent, "id", fmt.Sprintf("%d", vlanID)}
	if _, err := c.Runner.Run(ctx, c.Bin, args...); err != nil {
		return "", err
	}
	return c.uuidByName(ctx, name)
}

// AddBond creates a bond profile.
func (c *Client) AddBond(ctx context.Context, name, ifname string) (string, error) {
	if err := c.add(ctx, "bond", name, ifname); err != nil {
		return "", err
	}
	return c.uuidByName(ctx, name)
}

// AddSlave attaches an ethernet profile to a bridge/bond master.
func (c *Client) AddSlave(ctx context.Context, name, ifname, masterUUID string) (string, error) {
	if masterUUID == "" {
		return "", fmt.Errorf("slave 需要 master UUID")
	}
	args := []string{"connection", "add", "type", "ethernet", "con-name", name, "ifname", ifname, "master", masterUUID}
	if _, err := c.Runner.Run(ctx, c.Bin, args...); err != nil {
		return "", err
	}
	return c.uuidByName(ctx, name)
}

func (c *Client) add(ctx context.Context, kind, name, ifname string) error {
	args := []string{"connection", "add", "type", kind, "con-name", name, "ifname", ifname}
	_, err := c.Runner.Run(ctx, c.Bin, args...)
	return err
}

func (c *Client) uuidByName(ctx context.Context, name string) (string, error) {
	conn, err := c.ByName(ctx, name)
	if err != nil {
		return "", err
	}
	return conn.UUID, nil
}

func parseConnections(output string) []Connection {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	out := make([]Connection, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := splitTerse(line)
		if len(fields) < 3 {
			continue
		}
		conn := Connection{
			UUID: fields[0],
			Name: fields[1],
			Type: fields[2],
		}
		if len(fields) > 3 {
			conn.Device = fields[3]
		}
		conn.Active = conn.Device != ""
		out = append(out, conn)
	}
	return out
}

func splitTerse(line string) []string {
	fields := make([]string, 0, 4)
	var b strings.Builder
	escaped := false
	for _, r := range line {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case ':':
			fields = append(fields, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteByte('\\')
	}
	fields = append(fields, b.String())
	return fields
}

// MTUProperty returns the type-specific MTU property name.
func MTUProperty(connectionType string) string {
	switch connectionType {
	case "802-3-ethernet", "bridge", "vlan", "bond":
		// NetworkManager exposes the link MTU through the ethernet setting
		// even for virtual link master connections.
		return "802-3-ethernet.mtu"
	case "802-11-wireless":
		return "802-11-wireless.mtu"
	default:
		return ""
	}
}

// InterfaceName returns a safe NiuNexus-owned profile name.
func InterfaceName(prefix, ifname string) string {
	return prefix + ifname
}
