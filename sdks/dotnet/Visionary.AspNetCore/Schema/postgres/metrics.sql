CREATE TABLE IF NOT EXISTS metrics (
    id          BIGSERIAL PRIMARY KEY,
    created_at  TEXT NOT NULL,
    service     TEXT,
    instance    TEXT,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    unit        TEXT,
    labels      TEXT NOT NULL,
    series      TEXT NOT NULL,
    value       DOUBLE PRECISION,
    count       BIGINT,
    sum         DOUBLE PRECISION,
    min         DOUBLE PRECISION,
    max         DOUBLE PRECISION,
    buckets     TEXT
);
CREATE INDEX IF NOT EXISTS idx_metrics_name_created_at ON metrics (name, created_at);
CREATE INDEX IF NOT EXISTS idx_metrics_series_created_at ON metrics (series, created_at);
CREATE INDEX IF NOT EXISTS idx_metrics_created_at ON metrics (created_at);
