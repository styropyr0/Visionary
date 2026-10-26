package analytics

import (
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// securitySchema creates the tables the security features keep beside
// security_events (hosts_schema.go):
//
//   - security_known: what each account has signed in from before (a country,
//     a device, the last place with its time), so a new one stands out.
//   - security_suppressions: "don't flag this again" rules made from false
//     positives, by kind and IP or account, optionally until a date.
func (s *Server) securitySchema() []string {
	id, key, ts := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch s.q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, key, ts = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(191)", "VARCHAR(32)"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX"
	}
	ddl := `CREATE TABLE IF NOT EXISTS security_known (
    account    {key} NOT NULL,
    kind       {key} NOT NULL,
    value      {key} NOT NULL,
    first_seen {ts} NOT NULL,
    last_seen  {ts} NOT NULL,
    hits       INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (account, kind, value)
){tail};
{index} idx_security_known_seen ON security_known (last_seen);
CREATE TABLE IF NOT EXISTS security_suppressions (
    id         {id},
    created_at {ts} NOT NULL,
    created_by TEXT NOT NULL,
    kind       {key} NOT NULL,
    ip         {key},
    actor      TEXT,
    note       TEXT,
    expires_at {ts}
){tail}`
	return []string{strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{tail}", tail, "{index}", index).Replace(ddl)}
}

// securityAddedColumns are columns added to existing tables after their
// first release: a finding's triage state, and two-step sign-in for local
// accounts.
var securityAddedColumns = map[string][]sdk.Column{
	"security_events": {
		{Name: "status", Kind: "text"},    // '' open, acknowledged, resolved, false_positive
		{Name: "note", Kind: "text"},      // what whoever triaged it wrote
		{Name: "status_by", Kind: "text"}, // who last changed status or note
		{Name: "status_at", Kind: "text"},
	},
	"visionary_users": {
		{Name: "totp_secret", Kind: "text"},   // sealed with the session key
		{Name: "totp_enabled", Kind: "int"},   // 1 once a code confirmed it
		{Name: "totp_recovery", Kind: "text"}, // JSON list of SHA-256 hashes of unused recovery codes
	},
}
