CREATE TABLE IF NOT EXISTS agent_user_sessions (
    id CHAR(64) PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    role VARCHAR(32) NOT NULL,
    email VARCHAR(320) NOT NULL,
    name VARCHAR(200) NOT NULL DEFAULT '',
    access_token_ciphertext TEXT NOT NULL,
    refresh_token_ciphertext TEXT NOT NULL,
    access_expires_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    KEY idx_agent_sessions_user_expiry (tenant_id, user_id, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS notes (
    id CHAR(36) PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    external_note_id VARCHAR(128) NOT NULL,
    create_idempotency_key VARCHAR(128) NOT NULL,
    title VARCHAR(200) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    tags JSON NOT NULL,
    occurred_at DATETIME(6) NULL,
    status VARCHAR(32) NOT NULL,
    rag_kb_id BIGINT UNSIGNED NOT NULL,
    rag_document_id BIGINT UNSIGNED NULL,
    rag_job_id BIGINT UNSIGNED NULL,
    rag_status VARCHAR(64) NOT NULL DEFAULT '',
    last_error VARCHAR(1000) NOT NULL DEFAULT '',
    content_hash CHAR(64) NOT NULL,
    deleted_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uk_notes_external (tenant_id, user_id, external_note_id),
    UNIQUE KEY uk_notes_create_idempotency (tenant_id, user_id, create_idempotency_key),
    KEY idx_notes_user_created (tenant_id, user_id, created_at, id),
    KEY idx_notes_projection_status (status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS note_outbox_events (
    id CHAR(36) PRIMARY KEY,
    note_id CHAR(36) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    attempt INT NOT NULL DEFAULT 0,
    last_error VARCHAR(1000) NOT NULL DEFAULT '',
    available_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    processed_at DATETIME(6) NULL,
    CONSTRAINT fk_note_outbox_note FOREIGN KEY (note_id) REFERENCES notes(id),
    UNIQUE KEY uk_note_outbox_idempotency (tenant_id, user_id, event_type, idempotency_key),
    KEY idx_note_outbox_claim (tenant_id, user_id, status, available_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
