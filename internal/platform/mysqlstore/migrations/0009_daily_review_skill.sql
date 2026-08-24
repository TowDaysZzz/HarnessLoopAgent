CREATE TABLE IF NOT EXISTS skill_invocations (
    id CHAR(36) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    session_id CHAR(36) NOT NULL,
    chat_run_id CHAR(36) NOT NULL,
    skill_id VARCHAR(128) NOT NULL,
    skill_version VARCHAR(128) NOT NULL,
    arguments_json JSON NOT NULL,
    arguments_hash CHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_skill_invocations_session FOREIGN KEY (session_id) REFERENCES chat_sessions(id),
    CONSTRAINT fk_skill_invocations_run FOREIGN KEY (chat_run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    UNIQUE KEY uk_skill_invocation_run (chat_run_id),
    KEY idx_skill_invocations_owner_id (tenant_id, user_id, id),
    KEY idx_skill_invocations_owner_skill_created (tenant_id, user_id, skill_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

