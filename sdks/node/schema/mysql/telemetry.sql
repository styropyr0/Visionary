CREATE TABLE IF NOT EXISTS spans (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    request_id  VARCHAR(255),
    service     VARCHAR(255),
    instance    VARCHAR(255) NOT NULL,
    kind        VARCHAR(255) NOT NULL,
    name        VARCHAR(255) NOT NULL,
    target      VARCHAR(255),
    detail      TEXT,
    offset_ms   DOUBLE,
    duration_ms DOUBLE NOT NULL,
    status      VARCHAR(255) NOT NULL,
    error       TEXT,
    row_count   INT
) CHARACTER SET utf8mb4;
CREATE INDEX idx_spans_created_at ON spans (created_at);
CREATE INDEX idx_spans_request_id ON spans (request_id);
CREATE INDEX idx_spans_kind_name_created_at ON spans (kind, name, created_at);
CREATE TABLE IF NOT EXISTS errors (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    service     VARCHAR(255),
    instance    VARCHAR(255) NOT NULL,
    version     VARCHAR(255),
    request_id  VARCHAR(255),
    method      VARCHAR(255),
    route       VARCHAR(255),
    path        TEXT,
    user_id     VARCHAR(255),
    type        VARCHAR(255) NOT NULL,
    message     TEXT,
    stack       MEDIUMTEXT,
    fingerprint VARCHAR(255) NOT NULL,
    handled     INT NOT NULL DEFAULT 0,
    runtime     VARCHAR(255)
) CHARACTER SET utf8mb4;
CREATE INDEX idx_errors_created_at ON errors (created_at);
CREATE INDEX idx_errors_fingerprint_created_at ON errors (fingerprint, created_at);
CREATE INDEX idx_errors_request_id ON errors (request_id);
CREATE TABLE IF NOT EXISTS releases (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    service     VARCHAR(255),
    instance    VARCHAR(255) NOT NULL,
    version     VARCHAR(255) NOT NULL,
    sdk         VARCHAR(255),
    sdk_version VARCHAR(255),
    runtime     VARCHAR(255)
) CHARACTER SET utf8mb4;
CREATE INDEX idx_releases_created_at ON releases (created_at);
CREATE INDEX idx_releases_version_created_at ON releases (version, created_at);
