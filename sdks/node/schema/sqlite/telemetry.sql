CREATE TABLE IF NOT EXISTS spans (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    request_id  TEXT,
    service     TEXT,
    instance    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    name        TEXT NOT NULL,
    target      TEXT,
    detail      TEXT,
    offset_ms   REAL,
    duration_ms REAL NOT NULL,
    status      TEXT NOT NULL,
    error       TEXT,
    row_count   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_spans_created_at ON spans (created_at);
CREATE INDEX IF NOT EXISTS idx_spans_request_id ON spans (request_id);
CREATE INDEX IF NOT EXISTS idx_spans_kind_name_created_at ON spans (kind, name, created_at);
CREATE TABLE IF NOT EXISTS errors (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    service     TEXT,
    instance    TEXT NOT NULL,
    version     TEXT,
    request_id  TEXT,
    method      TEXT,
    route       TEXT,
    path        TEXT,
    user_id     TEXT,
    type        TEXT NOT NULL,
    message     TEXT,
    stack       TEXT,
    fingerprint TEXT NOT NULL,
    handled     INTEGER NOT NULL DEFAULT 0,
    runtime     TEXT
);
CREATE INDEX IF NOT EXISTS idx_errors_created_at ON errors (created_at);
CREATE INDEX IF NOT EXISTS idx_errors_fingerprint_created_at ON errors (fingerprint, created_at);
CREATE INDEX IF NOT EXISTS idx_errors_request_id ON errors (request_id);
CREATE TABLE IF NOT EXISTS releases (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    service     TEXT,
    instance    TEXT NOT NULL,
    version     TEXT NOT NULL,
    sdk         TEXT,
    sdk_version TEXT,
    runtime     TEXT
);
CREATE INDEX IF NOT EXISTS idx_releases_created_at ON releases (created_at);
CREATE INDEX IF NOT EXISTS idx_releases_version_created_at ON releases (version, created_at);

CREATE TABLE IF NOT EXISTS profiles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    service     TEXT,
    instance    TEXT NOT NULL,
    version     TEXT,
    sdk         TEXT,
    duration_ms REAL NOT NULL,
    samples     INTEGER NOT NULL,
    routes      TEXT,
    folded      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_profiles_created_at ON profiles (created_at);
