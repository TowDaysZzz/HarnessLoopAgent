CREATE TABLE IF NOT EXISTS chat_sessions (
    id CHAR(36) PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    tenant_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    title VARCHAR(200) NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    KEY idx_chat_sessions_updated_at (updated_at),
    KEY idx_chat_sessions_owner_updated (tenant_id, user_id, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS agent_runs (
    id CHAR(36) PRIMARY KEY,
    session_id CHAR(36) NOT NULL,
    status VARCHAR(32) NOT NULL,
    model_name VARCHAR(128) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    error_code VARCHAR(64) NULL,
    error_message VARCHAR(500) NULL,
    last_event_sequence BIGINT NOT NULL DEFAULT 0,
    active_guard TINYINT GENERATED ALWAYS AS (
        CASE WHEN status IN ('queued', 'running') THEN 1 ELSE NULL END
    ) STORED,
    created_at DATETIME(6) NOT NULL,
    started_at DATETIME(6) NULL,
    completed_at DATETIME(6) NULL,
    CONSTRAINT fk_agent_runs_session FOREIGN KEY (session_id) REFERENCES chat_sessions(id),
    UNIQUE KEY uk_agent_runs_idempotency (session_id, idempotency_key),
    UNIQUE KEY uk_agent_runs_single_active (session_id, active_guard),
    KEY idx_agent_runs_status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS chat_messages (
    id CHAR(36) PRIMARY KEY,
    session_id CHAR(36) NOT NULL,
    run_id CHAR(36) NULL,
    sequence BIGINT NOT NULL,
    role VARCHAR(32) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_chat_messages_session FOREIGN KEY (session_id) REFERENCES chat_sessions(id),
    CONSTRAINT fk_chat_messages_run FOREIGN KEY (run_id) REFERENCES agent_runs(id),
    UNIQUE KEY uk_chat_messages_sequence (session_id, sequence),
    KEY idx_chat_messages_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS agent_run_events (
    run_id CHAR(36) NOT NULL,
    sequence BIGINT NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    event_data JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (run_id, sequence),
    CONSTRAINT fk_agent_run_events_run FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
