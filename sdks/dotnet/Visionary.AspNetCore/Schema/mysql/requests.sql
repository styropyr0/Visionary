CREATE TABLE IF NOT EXISTS api_logs (
    id                      BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    request_id              VARCHAR(128) NOT NULL,
    created_at              VARCHAR(32)  NOT NULL,
    method                  VARCHAR(16)  NOT NULL,
    path                    TEXT         NOT NULL,
    route                   VARCHAR(255),
    query                   TEXT,
    status                  INT          NOT NULL,
    duration_ms             DOUBLE       NOT NULL,
    ip                      VARCHAR(64),
    user_agent              TEXT,
    user_id                 VARCHAR(128),
    user_handle             VARCHAR(255),
    user_role               VARCHAR(64),
    request_headers         TEXT,
    request_content_type    VARCHAR(255),
    request_body            MEDIUMTEXT,
    request_body_truncated  TINYINT      NOT NULL DEFAULT 0,
    request_size            BIGINT       NOT NULL DEFAULT 0,
    response_content_type   VARCHAR(255),
    response_body           MEDIUMTEXT,
    response_body_truncated TINYINT      NOT NULL DEFAULT 0,
    response_size           BIGINT       NOT NULL DEFAULT 0,
    error_code              VARCHAR(128),
    error_key               VARCHAR(255),
    service                 VARCHAR(255),
    version                 VARCHAR(255),
    db_count                BIGINT,
    db_ms                   DOUBLE,
    http_count              BIGINT,
    http_ms                 DOUBLE
) CHARACTER SET utf8mb4;
CREATE INDEX idx_api_logs_created_at ON api_logs (created_at);
CREATE INDEX idx_api_logs_user_created ON api_logs (user_id, created_at);
CREATE INDEX idx_api_logs_route_status ON api_logs (route, status);
CREATE INDEX idx_api_logs_request_id ON api_logs (request_id);
