package analytics

import sdk "github.com/styropyr0/visionary/sdks/go"

// teamSchema creates the dashboards, audit log and API key tables. API keys
// get a prefixed name because they often share a database with the
// application, which may well have an api_keys table of its own.
func (s *Server) teamSchema() []string {
	switch s.q.d {
	case sdk.Postgres:
		return []string{`CREATE TABLE IF NOT EXISTS dashboards (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    layout      TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL PRIMARY KEY,
    created_at TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    user_name  TEXT NOT NULL,
    role       TEXT NOT NULL,
    action     TEXT NOT NULL,
    target     TEXT NOT NULL,
    detail     TEXT NOT NULL,
    ip         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log (created_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log (action, created_at);
CREATE TABLE IF NOT EXISTS visionary_api_keys (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    scope        TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT,
    last_used_at TEXT,
    revoked_at   TEXT
)`}
	case sdk.MySQL:
		return []string{`CREATE TABLE IF NOT EXISTS dashboards (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    description TEXT NOT NULL,
    layout      MEDIUMTEXT NOT NULL,
    created_by  VARCHAR(255) NOT NULL,
    created_at  VARCHAR(32) NOT NULL,
    updated_at  VARCHAR(32) NOT NULL
) CHARACTER SET utf8mb4;
CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at VARCHAR(32) NOT NULL,
    user_id    VARCHAR(255) NOT NULL,
    user_name  VARCHAR(255) NOT NULL,
    role       VARCHAR(16) NOT NULL,
    action     VARCHAR(64) NOT NULL,
    target     VARCHAR(255) NOT NULL,
    detail     TEXT NOT NULL,
    ip         VARCHAR(64) NOT NULL
) CHARACTER SET utf8mb4;
CREATE INDEX idx_audit_log_created ON audit_log (created_at);
CREATE INDEX idx_audit_log_action ON audit_log (action, created_at);
CREATE TABLE IF NOT EXISTS visionary_api_keys (
    id           BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name         VARCHAR(255) NOT NULL,
    scope        VARCHAR(16) NOT NULL,
    prefix       VARCHAR(32) NOT NULL,
    key_hash     VARCHAR(64) NOT NULL UNIQUE,
    created_by   VARCHAR(255) NOT NULL,
    created_at   VARCHAR(32) NOT NULL,
    expires_at   VARCHAR(32),
    last_used_at VARCHAR(32),
    revoked_at   VARCHAR(32)
) CHARACTER SET utf8mb4`}
	}
	return []string{`CREATE TABLE IF NOT EXISTS dashboards (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    layout      TEXT NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    user_name  TEXT NOT NULL,
    role       TEXT NOT NULL,
    action     TEXT NOT NULL,
    target     TEXT NOT NULL,
    detail     TEXT NOT NULL,
    ip         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log (created_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log (action, created_at);
CREATE TABLE IF NOT EXISTS visionary_api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    scope        TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT,
    last_used_at TEXT,
    revoked_at   TEXT
)`}
}
