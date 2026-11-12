// Package rpc contains the JSON contract shared by the privileged core and
// the unprivileged web process.
package rpc

import "time"

// Health is returned by the core health endpoint.
type Health struct {
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	BuildTime string    `json:"buildTime,omitempty"`
	PID       int       `json:"pid"`
	UptimeSec int64     `json:"uptimeSec"`
	Time      time.Time `json:"time"`
}

// Tool describes a host utility the app may rely on.
type Tool struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Capabilities describes what the host currently supports.
type Capabilities struct {
	Tools           []Tool `json:"tools"`
	Kernel          string `json:"kernel"`
	OSVersion       string `json:"osVersion"`
	IPForward       bool   `json:"ipForward"`
	IPv6Forward     bool   `json:"ipv6Forward"`
	FirewallEnabled bool   `json:"firewallEnabled"`
	FirewallDetail  string `json:"firewallDetail,omitempty"`
	NetworkManager  string `json:"networkManager,omitempty"`
	Root            bool   `json:"root"`
}

// Interface is a read-only snapshot of a network interface.
type Interface struct {
	Name       string   `json:"name"`
	Index      int      `json:"index"`
	Type       string   `json:"type"`
	State      string   `json:"state"`
	OperState  string   `json:"operState"`
	MTU        int      `json:"mtu"`
	MAC        string   `json:"mac"`
	Flags      []string `json:"flags"`
	IPv4       []string `json:"ipv4"`
	IPv6       []string `json:"ipv6"`
	Master     string   `json:"master,omitempty"`
	SpeedMbps  int      `json:"speedMbps,omitempty"`
	Duplex     string   `json:"duplex,omitempty"`
	RxBytes    uint64   `json:"rxBytes"`
	TxBytes    uint64   `json:"txBytes"`
	RxPackets  uint64   `json:"rxPackets"`
	TxPackets  uint64   `json:"txPackets"`
	RxErrors   uint64   `json:"rxErrors"`
	TxErrors   uint64   `json:"txErrors"`
	RxDropped  uint64   `json:"rxDropped"`
	TxDropped  uint64   `json:"txDropped"`
	IsPhysical bool     `json:"isPhysical"`
}

// Route is a read-only snapshot of a route.
type Route struct {
	Family      string `json:"family"`
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device,omitempty"`
	Source      string `json:"source,omitempty"`
	Metric      int    `json:"metric"`
	Table       int    `json:"table"`
	Protocol    string `json:"protocol,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// Overview is the payload consumed by the dashboard.
type Overview struct {
	Health       Health       `json:"health"`
	Hostname     string       `json:"hostname"`
	Kernel       string       `json:"kernel"`
	OSVersion    string       `json:"osVersion"`
	Uptime       string       `json:"uptime"`
	UptimeSec    int64        `json:"uptimeSec"`
	Interfaces   []Interface  `json:"interfaces"`
	Routes       []Route      `json:"routes"`
	DNS          []string     `json:"dns"`
	Capabilities Capabilities `json:"capabilities"`
	Warnings     []string     `json:"warnings,omitempty"`
	FetchedAt    time.Time    `json:"fetchedAt"`
}

// Error is the standard error body returned by both processes.
type Error struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}
