CREATE TABLE IF NOT EXISTS app_logs (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    service     VARCHAR(255),
    instance    VARCHAR(255),
    version     VARCHAR(255),
    source      VARCHAR(32) NOT NULL,
    host        VARCHAR(255),
    container   VARCHAR(255),
    level       VARCHAR(8) NOT NULL,
    severity    INT NOT NULL,
    logger      VARCHAR(255),
    message     TEXT NOT NULL,
    attrs       MEDIUMTEXT,
    request_id  VARCHAR(255),
    trace_id    VARCHAR(64),
    span_id     VARCHAR(32),
    user_id     VARCHAR(255),
    session_id  VARCHAR(255),
    code_file   VARCHAR(512),
    code_line   INT,
    code_func   VARCHAR(255),
    pattern     VARCHAR(16),
    size        INT NOT NULL DEFAULT 0
) CHARACTER SET utf8mb4;
CREATE INDEX idx_app_logs_created_at ON app_logs (created_at);
CREATE INDEX idx_app_logs_service_created_at ON app_logs (service, created_at);
CREATE INDEX idx_app_logs_severity_created_at ON app_logs (severity, created_at);
CREATE INDEX idx_app_logs_request_id ON app_logs (request_id);
CREATE INDEX idx_app_logs_trace_id ON app_logs (trace_id);
CREATE INDEX idx_app_logs_pattern_created_at ON app_logs (pattern, created_at);
