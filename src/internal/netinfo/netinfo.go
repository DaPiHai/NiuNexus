package netinfo

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"

	"niunexus/internal/rpc"
)

// Interfaces returns a snapshot of all links, addresses and counters.
func Interfaces() ([]rpc.Interface, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("读取网口列表失败: %w", err)
	}

	names := make(map[int]string, len(links))
	for _, link := range links {
		names[link.Attrs().Index] = link.Attrs().Name
	}

	out := make([]rpc.Interface, 0, len(links))
	for _, link := range links {
		attrs := link.Attrs()
		item := rpc.Interface{
			Name:       attrs.Name,
			Index:      attrs.Index,
			Type:       linkType(link, attrs),
			MTU:        attrs.MTU,
			MAC:        attrs.HardwareAddr.String(),
			Flags:      flags(attrs),
			OperState:  attrs.OperState.String(),
			State:      state(attrs),
			IsPhysical: link.Type() == "device" && attrs.Flags&net.FlagLoopback == 0,
			Master:     names[attrs.MasterIndex],
		}
		if item.Type == "loopback" && item.State == "up" {
			item.OperState = "up"
		}
		if attrs.Statistics != nil {
			item.RxBytes = attrs.Statistics.RxBytes
			item.TxBytes = attrs.Statistics.TxBytes
			item.RxPackets = attrs.Statistics.RxPackets
			item.TxPackets = attrs.Statistics.TxPackets
			item.RxErrors = attrs.Statistics.RxErrors
			item.TxErrors = attrs.Statistics.TxErrors
			item.RxDropped = attrs.Statistics.RxDropped
			item.TxDropped = attrs.Statistics.TxDropped
		}
		item.IPv4, item.IPv6 = addresses(link)
		item.SpeedMbps = readIntFile(fmt.Sprintf("/sys/class/net/%s/speed", attrs.Name))
		item.Duplex = readTrimmed(fmt.Sprintf("/sys/class/net/%s/duplex", attrs.Name))
		out = append(out, item)
	}

	sort.Slice(out, func(i, j int) bool {
		// Put loopback at the end; otherwise sort by name.
		if out[i].Type == "loopback" {
			return false
		}
		if out[j].Type == "loopback" {
			return true
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func addresses(link netlink.Link) ([]string, []string) {
	v4 := make([]string, 0, 2)
	v6 := make([]string, 0, 2)
	if addrs, err := netlink.AddrList(link, netlink.FAMILY_V4); err == nil {
		for _, addr := range addrs {
			if addr.IPNet != nil {
				v4 = append(v4, addr.IPNet.String())
			}
		}
	}
	if addrs, err := netlink.AddrList(link, netlink.FAMILY_V6); err == nil {
		for _, addr := range addrs {
			if addr.IPNet != nil {
				v6 = append(v6, addr.IPNet.String())
			}
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return v4, v6
}

// Routes returns all routes visible in the current network namespace.
func Routes() ([]rpc.Route, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("读取网口列表失败: %w", err)
	}
	names := make(map[int]string, len(links))
	for _, link := range links {
		names[link.Attrs().Index] = link.Attrs().Name
	}

	out := make([]rpc.Route, 0, 32)
	families := []struct {
		name   string
		family int
	}{
		{"ipv4", netlink.FAMILY_V4},
		{"ipv6", netlink.FAMILY_V6},
	}
	for _, fam := range families {
		routes, err := netlink.RouteList(nil, fam.family)
		if err != nil {
			return nil, fmt.Errorf("读取路由失败: %w", err)
		}
		for _, route := range routes {
			out = append(out, rpc.Route{
				Family:      fam.name,
				Destination: destination(route),
				Gateway:     ipString(route.Gw),
				Device:      names[route.LinkIndex],
				Source:      ipString(route.Src),
				Metric:      route.Priority,
				Table:       table(route.Table),
				Protocol:    routeProtocol(route.Protocol),
				Scope:       route.Scope.String(),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Destination == "default" && out[j].Destination != "default" {
			return true
		}
		if out[j].Destination == "default" && out[i].Destination != "default" {
			return false
		}
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Destination < out[j].Destination
	})
	return out, nil
}

func destination(route netlink.Route) string {
	if route.Dst == nil {
		return "default"
	}
	if ones, _ := route.Dst.Mask.Size(); ones == 0 && route.Dst.IP.IsUnspecified() {
		return "default"
	}
	return route.Dst.String()
}

func table(t int) int {
	if t == 0 {
		return 254 // RT_TABLE_MAIN
	}
	return t
}

func routeProtocol(p netlink.RouteProtocol) string {
	switch p {
	case 0:
		return "unspec"
	case 1:
		return "redirect"
	case 2:
		return "kernel"
	case 3:
		return "boot"
	case 4:
		return "static"
	case 8:
		return "gated"
	case 9:
		return "ra"
	case 10:
		return "mrt"
	case 11:
		return "zebra"
	case 12:
		return "bird"
	case 13:
		return "dnrouted"
	case 14:
		return "xorp"
	case 15:
		return "ntk"
	case 16:
		return "dhcp"
	case 18:
		return "keepalived"
	case 42:
		return "babel"
	case 186:
		return "bgp"
	case 187:
		return "isis"
	case 188:
		return "ospf"
	case 189:
		return "rip"
	case 192:
		return "eigrp"
	default:
		return strconv.Itoa(int(p))
	}
}

// LocalIPv4Addresses returns non-loopback IPv4 addresses without prefixes.
func LocalIPv4Addresses() []string {
	ifaces, err := Interfaces()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		for _, value := range iface.IPv4 {
			ip, _, err := net.ParseCIDR(value)
			if err == nil && !ip.IsLoopback() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

// DNS reads nameservers from the active resolv.conf.
func DNS() []string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if !seen[fields[1]] {
			seen[fields[1]] = true
			out = append(out, fields[1])
		}
	}
	return out
}

// Hostname returns the current hostname.
func Hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

// Uptime returns a human-readable uptime and the raw seconds.
func Uptime() (string, int64) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "unknown", 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "unknown", 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "unknown", 0
	}
	total := int64(seconds)
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时 %d 分", days, hours, minutes), total
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分", hours, minutes), total
	default:
		return fmt.Sprintf("%d 分", minutes), total
	}
}

func flags(attrs *netlink.LinkAttrs) []string {
	out := make([]string, 0, 4)
	if attrs.Flags&net.FlagUp != 0 {
		out = append(out, "up")
	}
	if attrs.Flags&net.FlagBroadcast != 0 {
		out = append(out, "broadcast")
	}
	if attrs.Flags&net.FlagLoopback != 0 {
		out = append(out, "loopback")
	}
	if attrs.Flags&net.FlagPointToPoint != 0 {
		out = append(out, "point-to-point")
	}
	if attrs.Flags&net.FlagMulticast != 0 {
		out = append(out, "multicast")
	}
	return out
}

func state(attrs *netlink.LinkAttrs) string {
	if attrs.Flags&net.FlagUp == 0 {
		return "down"
	}
	if attrs.Flags&net.FlagLoopback != 0 {
		return "up"
	}
	if attrs.OperState.String() == "up" {
		return "up"
	}
	return attrs.OperState.String()
}

func linkType(link netlink.Link, attrs *netlink.LinkAttrs) string {
	if attrs.Flags&net.FlagLoopback != 0 {
		return "loopback"
	}
	if kind := link.Type(); kind != "" {
		return kind
	}
	return "unknown"
}

func ipString(ip net.IP) string {
	if ip == nil || ip.IsUnspecified() {
		return ""
	}
	return ip.String()
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readIntFile(path string) int {
	v, err := strconv.Atoi(readTrimmed(path))
	if err != nil || v <= 0 {
		return 0
	}
	return v
}
