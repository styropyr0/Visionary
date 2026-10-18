package analytics

import (
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// hostsSchema creates the tables the host agent (cmd/visionary-agent) fills
// through the ingest API, and security_events, which the agent and the
// server's own detectors both write. Host metrics themselves go to
// system_metrics with sdk = 'agent'.
//
//   - host_inventory: one row per host, replaced on every report.
//   - host_processes, host_mounts, host_units: snapshots, one set of rows per
//     host per report (about once a minute).
//   - security_events: one row per finding. source is 'host' (from the
//     agent), 'api' (detected from api_logs) or 'audit' (Visionary's audit
//     log); severity is 'info', 'warning' or 'critical'; count says how many
//     occurrences one row stands for (e.g. 12 failed SSH logins in a minute).
func (s *Server) hostsSchema() []string {
	return []string{s.q.hostsTables()}
}

func (q sqlDialect) hostsTables() string {
	// The dialects differ only in the id column and indexed text types.
	id, key, ts, real := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT", "REAL"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch q.d {
	case sdk.Postgres:
		id, real = "BIGSERIAL PRIMARY KEY", "DOUBLE PRECISION"
	case sdk.MySQL:
		id, key, ts, real = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(255)", "VARCHAR(32)", "DOUBLE"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX" // duplicate-index errors are ignored
	}
	ddl := `CREATE TABLE IF NOT EXISTS host_inventory (
    host          {key} NOT NULL PRIMARY KEY,
    service       TEXT,
    os            TEXT,
    kernel        TEXT,
    arch          TEXT,
    cpu_count     INTEGER,
    mem_total_mb  {real},
    boot_time     {ts},
    agent_version TEXT,
    ip            TEXT,
    first_seen    {ts} NOT NULL,
    last_seen     {ts} NOT NULL
){tail};
CREATE TABLE IF NOT EXISTS host_processes (
    id          {id},
    created_at  {ts} NOT NULL,
    host        {key} NOT NULL,
    pid         INTEGER NOT NULL,
    name        TEXT NOT NULL,
    username    TEXT,
    cpu_percent {real},
    rss_mb      {real},
    cmdline     TEXT
){tail};
{index} idx_host_processes_host_created ON host_processes (host, created_at);
{index} idx_host_processes_created ON host_processes (created_at);
CREATE TABLE IF NOT EXISTS host_mounts (
    id             {id},
    created_at     {ts} NOT NULL,
    host           {key} NOT NULL,
    mount          TEXT NOT NULL,
    device         TEXT,
    fstype         TEXT,
    used_percent   {real},
    free_gb        {real},
    total_gb       {real},
    inodes_percent {real}
){tail};
{index} idx_host_mounts_host_created ON host_mounts (host, created_at);
{index} idx_host_mounts_created ON host_mounts (created_at);
CREATE TABLE IF NOT EXISTS host_units (
    id         {id},
    created_at {ts} NOT NULL,
    host       {key} NOT NULL,
    unit       TEXT NOT NULL,
    state      TEXT NOT NULL,
    sub        TEXT
){tail};
{index} idx_host_units_host_created ON host_units (host, created_at);
{index} idx_host_units_created ON host_units (created_at);
CREATE TABLE IF NOT EXISTS security_events (
    id         {id},
    created_at {ts} NOT NULL,
    source     {key} NOT NULL,
    kind       {key} NOT NULL,
    severity   {key} NOT NULL,
    host       {key},
    service    TEXT,
    actor      TEXT,
    ip         {key},
    target     TEXT,
    detail     TEXT,
    count      INTEGER NOT NULL DEFAULT 1
){tail};
{index} idx_security_events_created ON security_events (created_at);
{index} idx_security_events_kind_created ON security_events (kind, created_at);
{index} idx_security_events_host_created ON security_events (host, created_at);
{index} idx_security_events_ip_created ON security_events (ip, created_at)`
	return strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{real}", real, "{tail}", tail, "{index}", index).Replace(ddl)
}
