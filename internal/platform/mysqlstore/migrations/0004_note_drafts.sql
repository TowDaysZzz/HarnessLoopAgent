CREATE TABLE IF NOT EXISTS note_drafts (
    id CHAR(36) PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id CHAR(36) NOT NULL,
    title VARCHAR(200) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    content_hash CHAR(64) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    pending_guard TINYINT GENERATED ALWAYS AS (
        CASE WHEN status = 'pending' THEN 1 ELSE NULL END
    ) STORED,
    CONSTRAINT fk_note_drafts_session FOREIGN KEY (session_id) REFERENCES chat_sessions(id),
    UNIQUE KEY uk_note_drafts_pending (tenant_id, user_id, session_id, pending_guard),
    KEY idx_note_drafts_scope_created (tenant_id, user_id, session_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
