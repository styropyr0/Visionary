CREATE TABLE IF NOT EXISTS app_logs (
    id          BIGSERIAL PRIMARY KEY,
    created_at  TEXT NOT NULL,
    service     TEXT,
    instance    TEXT,
    version     TEXT,
    source      TEXT NOT NULL,
    host        TEXT,
    container   TEXT,
    level       TEXT NOT NULL,
    severity    INTEGER NOT NULL,
    logger      TEXT,
    message     TEXT NOT NULL,
    attrs       TEXT,
    request_id  TEXT,
    trace_id    TEXT,
    span_id     TEXT,
    user_id     TEXT,
    session_id  TEXT,
    code_file   TEXT,
    code_line   INTEGER,
    code_func   TEXT,
    pattern     TEXT,
    size        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_app_logs_created_at ON app_logs (created_at);
CREATE INDEX IF NOT EXISTS idx_app_logs_service_created_at ON app_logs (service, created_at);
CREATE INDEX IF NOT EXISTS idx_app_logs_severity_created_at ON app_logs (severity, created_at);
CREATE INDEX IF NOT EXISTS idx_app_logs_request_id ON app_logs (request_id);
CREATE INDEX IF NOT EXISTS idx_app_logs_trace_id ON app_logs (trace_id);
CREATE INDEX IF NOT EXISTS idx_app_logs_pattern_created_at ON app_logs (pattern, created_at);
