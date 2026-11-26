package agentapi

// The agent polls BlocksPath with a server ingest key and keeps the host's
// firewall in step with the list it gets back
const BlocksPath = "/api/agent/v1/blocks"

type BlocksPoll struct {
	Host    string `json:"host"`
	Version string `json:"version"`
	// Backend is nftables, iptables, off (VISIONARY_AGENT_FIREWALL=false) or unsupported
	Backend string `json:"backend"`
	Applied int    `json:"applied"`
	Error   string `json:"error,omitempty"`
	// Skipped are addresses the agent refused, such as a live SSH session's
	Skipped []string `json:"skipped,omitempty"`
}

type Blocks struct {
	Version string `json:"version"`
	// Addresses are IPv4 or IPv6 addresses or CIDR ranges, at most MaxBlocks
	Addresses []string `json:"addresses"`
	PollS     int      `json:"poll_s"`
}

const MaxBlocks = 10000
