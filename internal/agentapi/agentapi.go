// Package agentapi is the wire format of the host agent (cmd/visionary-agent):
// the JSON body it posts to POST /api/agent/v1/report, which the server
// (internal/analytics, hosts_ingest.go) decodes into the same types.
//
// One report covers one report interval (a minute by default): the samples
// taken in it, the latest inventory and snapshots, and the security findings
// since the previous report. Times are RFC 3339 in UTC.
package agentapi

// Path is where reports are posted.
const Path = "/api/agent/v1/report"

// Report is one agent report.
type Report struct {
	// Host names the machine; it is system_metrics.instance and
	// host_inventory.host. Letters, digits and . _ : - only, up to 128.
	Host    string `json:"host"`
	Service string `json:"service,omitempty"`
	// At is when the report was made; snapshots and events carry this time.
	At string `json:"at"`
	// ReportS is the agent's report interval in seconds, so the server can
	// tell an online host from a stale one.
	ReportS float64 `json:"report_s"`
	// Runtime describes the OS, e.g. "Ubuntu 24.04 (linux 6.8.0-45-generic)";
	// it fills system_metrics.runtime.
	Runtime   string    `json:"runtime"`
	Inventory Inventory `json:"inventory"`
	Samples   []Sample  `json:"samples"`
	Processes []Process `json:"processes,omitempty"`
	Mounts    []Mount   `json:"mounts,omitempty"`
	// Units are failed systemd services plus the watched ones
	// (VISIONARY_AGENT_UNITS). Nil when systemd is not readable.
	Units  []Unit  `json:"units,omitempty"`
	Events []Event `json:"events,omitempty"`
}

// Inventory describes the machine.
type Inventory struct {
	OS           string  `json:"os"`     // e.g. "Ubuntu 24.04"
	Kernel       string  `json:"kernel"` // e.g. "6.8.0-45-generic"
	Arch         string  `json:"arch"`   // e.g. "x86_64"
	CPUCount     int     `json:"cpu_count"`
	MemTotalMB   float64 `json:"mem_total_mb"`
	BootTime     string  `json:"boot_time"`
	AgentVersion string  `json:"agent_version"`
	IP           string  `json:"ip,omitempty"` // primary address
}

// Sample is one system_metrics row: the sample time and the values of
// visionary.HostColumns that could be read.
type Sample struct {
	At     string             `json:"at"`
	Values map[string]float64 `json:"values"`
}

// Process is one row of the top-processes snapshot.
type Process struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	Username   string  `json:"username,omitempty"`
	CPUPercent float64 `json:"cpu_percent"` // percent of one core
	RSSMB      float64 `json:"rss_mb"`
	// Cmdline is clipped to 300 characters with secrets redacted.
	Cmdline string `json:"cmdline,omitempty"`
}

// Mount is one real filesystem.
type Mount struct {
	Mount         string   `json:"mount"`
	Device        string   `json:"device,omitempty"`
	FSType        string   `json:"fstype,omitempty"`
	UsedPercent   float64  `json:"used_percent"`
	FreeGB        float64  `json:"free_gb"`
	TotalGB       float64  `json:"total_gb"`
	InodesPercent *float64 `json:"inodes_percent,omitempty"` // nil when the filesystem has no inodes
}

// Unit is a systemd service.
type Unit struct {
	Unit  string `json:"unit"`
	State string `json:"state"` // active, inactive, failed, activating, not-found, ...
	Sub   string `json:"sub,omitempty"`
}

// Event is a host security finding, aggregated over the report interval.
// The server sets source = 'host' and the severity.
type Event struct {
	// Kind is ssh_failed, ssh_login, sudo, port_opened, port_closed or
	// updates_pending.
	Kind   string `json:"kind"`
	Actor  string `json:"actor,omitempty"`  // the user name tried or used
	IP     string `json:"ip,omitempty"`     // the remote address
	Target string `json:"target,omitempty"` // the sudo command, or "tcp 0.0.0.0:5432 postgres"
	Detail string `json:"detail,omitempty"` // e.g. "3 security" for updates_pending
	Count  int    `json:"count"`            // occurrences; updates_pending: total updates
	// Security is the security-only update count (updates_pending).
	Security int `json:"security,omitempty"`
}
