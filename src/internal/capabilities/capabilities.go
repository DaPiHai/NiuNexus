package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"niunexus/internal/rpc"
)

// commonPaths mirrors the PATH a root lifecycle script normally gets. The
// interactive shell on fnOS does not always include /usr/sbin.
var commonPaths = []string{
	"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
}

// Detect collects host capabilities. It never mutates system state.
func Detect() rpc.Capabilities {
	caps := rpc.Capabilities{
		Kernel:         readTrimmed("/proc/sys/kernel/osrelease"),
		OSVersion:      detectOSVersion(),
		IPForward:      readBoolFile("/proc/sys/net/ipv4/ip_forward"),
		IPv6Forward:    readBoolFile("/proc/sys/net/ipv6/conf/all/forwarding"),
		NetworkManager: probeVersion("nmcli", "--version"),
		Root:           os.Geteuid() == 0,
	}
	if caps.Kernel == "" {
		caps.Kernel = "unknown"
	}

	caps.Tools = []rpc.Tool{
		probe("nft", "--version"),
		probe("ip", "-V"),
		probe("nmcli", "--version"),
		probe("dnsmasq", "--version"),
		probe("ethtool", "--version"),
		probe("conntrack", "--version"),
		probe("runuser", "--version"),
	}
	caps.FirewallEnabled, caps.FirewallDetail = detectFirewall()
	return caps
}

func probe(name string, args ...string) rpc.Tool {
	tool := rpc.Tool{Name: name}
	path := lookPath(name)
	if path == "" {
		return tool
	}
	tool.Available = true
	tool.Path = path
	tool.Version = runVersion(path, args...)
	return tool
}

func probeVersion(name string, args ...string) string {
	path := lookPath(name)
	if path == "" {
		return ""
	}
	return runVersion(path, args...)
}

func runVersion(path string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	line := firstLine(string(out))
	if len(line) > 180 {
		line = line[:180]
	}
	return line
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func lookPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range commonPaths {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func detectOSVersion() string {
	if v := readTrimmed("/usr/trim/etc/version"); v != "" {
		return v
	}
	// Fall back to the distribution pretty name.
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"'")
		}
	}
	return "unknown"
}

func detectFirewall() (bool, string) {
	data, err := os.ReadFile("/usr/trim/etc/fw.conf")
	if err != nil {
		return false, ""
	}
	var profiles []struct {
		Profile string `json:"profile"`
		Enable  bool   `json:"enable"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		return false, "未解析 fnOS 防火墙配置"
	}
	for _, p := range profiles {
		if p.Enable {
			return true, fmt.Sprintf("fnOS 防火墙已启用（profile=%s）", p.Profile)
		}
	}
	return false, "fnOS 防火墙当前关闭"
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readBoolFile(path string) bool {
	return readTrimmed(path) == "1"
}
