CREATE TABLE IF NOT EXISTS metrics (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    service     TEXT,
    instance    TEXT,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    unit        TEXT,
    labels      TEXT NOT NULL,
    series      TEXT NOT NULL,
    value       REAL,
    count       INTEGER,
    sum         REAL,
    min         REAL,
    max         REAL,
    buckets     TEXT
);
CREATE INDEX IF NOT EXISTS idx_metrics_name_created_at ON metrics (name, created_at);
CREATE INDEX IF NOT EXISTS idx_metrics_series_created_at ON metrics (series, created_at);
CREATE INDEX IF NOT EXISTS idx_metrics_created_at ON metrics (created_at);
