CREATE TABLE IF NOT EXISTS api_logs (
    id                      BIGSERIAL PRIMARY KEY,
    request_id              TEXT             NOT NULL,
    created_at              TEXT             NOT NULL,
    method                  TEXT             NOT NULL,
    path                    TEXT             NOT NULL,
    route                   TEXT,
    query                   TEXT,
    status                  INTEGER          NOT NULL,
    duration_ms             DOUBLE PRECISION NOT NULL,
    ip                      TEXT,
    user_agent              TEXT,
    user_id                 TEXT,
    user_handle             TEXT,
    user_role               TEXT,
    request_headers         TEXT,
    request_content_type    TEXT,
    request_body            TEXT,
    request_body_truncated  INTEGER          NOT NULL DEFAULT 0,
    request_size            BIGINT           NOT NULL DEFAULT 0,
    response_content_type   TEXT,
    response_body           TEXT,
    response_body_truncated INTEGER          NOT NULL DEFAULT 0,
    response_size           BIGINT           NOT NULL DEFAULT 0,
    error_code              TEXT,
    error_key               TEXT
,
    service                 TEXT,
    version                 TEXT,
    db_count                BIGINT,
    db_ms                   DOUBLE PRECISION,
    http_count              BIGINT,
    http_ms                 DOUBLE PRECISION,
    login_account           TEXT
);
CREATE INDEX IF NOT EXISTS idx_api_logs_created_at ON api_logs (created_at);
CREATE INDEX IF NOT EXISTS idx_api_logs_user_created ON api_logs (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_api_logs_route_status ON api_logs (route, status);
CREATE INDEX IF NOT EXISTS idx_api_logs_request_id ON api_logs (request_id);
