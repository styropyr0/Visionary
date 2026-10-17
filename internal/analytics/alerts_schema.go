package analytics

import sdk "github.com/styropyr0/visionary/sdks/go"

// alertTables creates the alerting tables. alert_events is the history the
// analyst may read; the others are Visionary's own configuration and state.
func (q sqlDialect) alertTables() string {
	switch q.d {
	case sdk.Postgres:
		return `CREATE TABLE IF NOT EXISTS alert_channels (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL,
    config         TEXT NOT NULL,
    digest_enabled INTEGER NOT NULL DEFAULT 0,
    digest_weekday INTEGER NOT NULL DEFAULT 1,
    digest_hour    INTEGER NOT NULL DEFAULT 9,
    digest_sent_at TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_rules (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    spec        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    channels    TEXT NOT NULL,
    runbook     TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    muted_until TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_state (
    rule_id       BIGINT NOT NULL,
    series_key    TEXT NOT NULL,
    label         TEXT NOT NULL DEFAULT '',
    state         TEXT NOT NULL,
    state_since   TEXT NOT NULL,
    pending_since TEXT NOT NULL DEFAULT '',
    notified_at   TEXT NOT NULL DEFAULT '',
    evaluated_at  TEXT NOT NULL DEFAULT '',
    value         DOUBLE PRECISION,
    threshold     DOUBLE PRECISION,
    message       TEXT NOT NULL DEFAULT '',
    recent        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (rule_id, series_key)
);
CREATE TABLE IF NOT EXISTS alert_events (
    id          BIGSERIAL PRIMARY KEY,
    created_at  TEXT NOT NULL,
    rule_id     BIGINT NOT NULL,
    rule_name   TEXT NOT NULL,
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    state       TEXT NOT NULL,
    series_key  TEXT NOT NULL DEFAULT '',
    value       DOUBLE PRECISION,
    threshold   DOUBLE PRECISION,
    message     TEXT NOT NULL,
    explanation TEXT NOT NULL DEFAULT '',
    notified    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_alert_events_created_at ON alert_events (created_at);
CREATE INDEX IF NOT EXISTS idx_alert_events_rule_created ON alert_events (rule_id, created_at)`
	case sdk.MySQL:
		return `CREATE TABLE IF NOT EXISTS alert_channels (
    id             BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name           VARCHAR(255) NOT NULL,
    kind           VARCHAR(32) NOT NULL,
    config         TEXT NOT NULL,
    digest_enabled INT NOT NULL DEFAULT 0,
    digest_weekday INT NOT NULL DEFAULT 1,
    digest_hour    INT NOT NULL DEFAULT 9,
    digest_sent_at VARCHAR(32) NOT NULL DEFAULT '',
    created_at     VARCHAR(32) NOT NULL,
    updated_at     VARCHAR(32) NOT NULL
) CHARACTER SET utf8mb4;
CREATE TABLE IF NOT EXISTS alert_rules (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    kind        VARCHAR(32) NOT NULL,
    spec        MEDIUMTEXT NOT NULL,
    severity    VARCHAR(16) NOT NULL,
    channels    TEXT NOT NULL,
    runbook     TEXT NOT NULL,
    enabled     INT NOT NULL DEFAULT 1,
    muted_until VARCHAR(32) NOT NULL DEFAULT '',
    created_at  VARCHAR(32) NOT NULL,
    updated_at  VARCHAR(32) NOT NULL
) CHARACTER SET utf8mb4;
CREATE TABLE IF NOT EXISTS alert_state (
    rule_id       BIGINT NOT NULL,
    series_key    VARCHAR(191) NOT NULL,
    label         VARCHAR(255) NOT NULL DEFAULT '',
    state         VARCHAR(16) NOT NULL,
    state_since   VARCHAR(32) NOT NULL,
    pending_since VARCHAR(32) NOT NULL DEFAULT '',
    notified_at   VARCHAR(32) NOT NULL DEFAULT '',
    evaluated_at  VARCHAR(32) NOT NULL DEFAULT '',
    value         DOUBLE,
    threshold     DOUBLE,
    message       TEXT NOT NULL,
    recent        TEXT NOT NULL,
    PRIMARY KEY (rule_id, series_key)
) CHARACTER SET utf8mb4;
CREATE TABLE IF NOT EXISTS alert_events (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    rule_id     BIGINT NOT NULL,
    rule_name   VARCHAR(255) NOT NULL,
    kind        VARCHAR(32) NOT NULL,
    severity    VARCHAR(16) NOT NULL,
    state       VARCHAR(16) NOT NULL,
    series_key  VARCHAR(191) NOT NULL DEFAULT '',
    value       DOUBLE,
    threshold   DOUBLE,
    message     TEXT NOT NULL,
    explanation TEXT NOT NULL,
    notified    INT NOT NULL DEFAULT 0
) CHARACTER SET utf8mb4;
CREATE INDEX idx_alert_events_created_at ON alert_events (created_at);
CREATE INDEX idx_alert_events_rule_created ON alert_events (rule_id, created_at)`
	}
	return `CREATE TABLE IF NOT EXISTS alert_channels (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL,
    config         TEXT NOT NULL,
    digest_enabled INTEGER NOT NULL DEFAULT 0,
    digest_weekday INTEGER NOT NULL DEFAULT 1,
    digest_hour    INTEGER NOT NULL DEFAULT 9,
    digest_sent_at TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    spec        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    channels    TEXT NOT NULL,
    runbook     TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    muted_until TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_state (
    rule_id       INTEGER NOT NULL,
    series_key    TEXT NOT NULL,
    label         TEXT NOT NULL DEFAULT '',
    state         TEXT NOT NULL,
    state_since   TEXT NOT NULL,
    pending_since TEXT NOT NULL DEFAULT '',
    notified_at   TEXT NOT NULL DEFAULT '',
    evaluated_at  TEXT NOT NULL DEFAULT '',
    value         REAL,
    threshold     REAL,
    message       TEXT NOT NULL DEFAULT '',
    recent        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (rule_id, series_key)
);
CREATE TABLE IF NOT EXISTS alert_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    rule_id     INTEGER NOT NULL,
    rule_name   TEXT NOT NULL,
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    state       TEXT NOT NULL,
    series_key  TEXT NOT NULL DEFAULT '',
    value       REAL,
    threshold   REAL,
    message     TEXT NOT NULL,
    explanation TEXT NOT NULL DEFAULT '',
    notified    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_alert_events_created_at ON alert_events (created_at);
CREATE INDEX IF NOT EXISTS idx_alert_events_rule_created ON alert_events (rule_id, created_at)`
}

// upsertState writes one rule series' state, inserting it the first time.
func (q sqlDialect) upsertState() string {
	const cols = `INSERT INTO alert_state (rule_id, series_key, label, state, state_since, pending_since, notified_at, evaluated_at, value, threshold, message, recent)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if q.d == sdk.MySQL {
		return cols + ` ON DUPLICATE KEY UPDATE label = VALUES(label), state = VALUES(state), state_since = VALUES(state_since),
  pending_since = VALUES(pending_since), notified_at = VALUES(notified_at), evaluated_at = VALUES(evaluated_at),
  value = VALUES(value), threshold = VALUES(threshold), message = VALUES(message), recent = VALUES(recent)`
	}
	return cols + ` ON CONFLICT (rule_id, series_key) DO UPDATE SET label = excluded.label, state = excluded.state,
  state_since = excluded.state_since, pending_since = excluded.pending_since, notified_at = excluded.notified_at,
  evaluated_at = excluded.evaluated_at, value = excluded.value, threshold = excluded.threshold,
  message = excluded.message, recent = excluded.recent`
}
