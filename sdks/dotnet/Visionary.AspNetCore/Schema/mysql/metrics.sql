CREATE TABLE IF NOT EXISTS metrics (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    service     VARCHAR(255),
    instance    VARCHAR(255),
    name        VARCHAR(255) NOT NULL,
    kind        VARCHAR(16) NOT NULL,
    unit        VARCHAR(64),
    labels      TEXT NOT NULL,
    series      VARCHAR(16) NOT NULL,
    value       DOUBLE,
    count       BIGINT,
    sum         DOUBLE,
    min         DOUBLE,
    max         DOUBLE,
    buckets     TEXT
) CHARACTER SET utf8mb4;
CREATE INDEX idx_metrics_name_created_at ON metrics (name, created_at);
CREATE INDEX idx_metrics_series_created_at ON metrics (series, created_at);
CREATE INDEX idx_metrics_created_at ON metrics (created_at);
