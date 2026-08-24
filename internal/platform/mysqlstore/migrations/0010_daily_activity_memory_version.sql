ALTER TABLE chat_messages
    ADD KEY idx_chat_messages_created_session (created_at, session_id, sequence);

ALTER TABLE notes
    ADD KEY idx_notes_owner_occurred (tenant_id, user_id, occurred_at, id);

CREATE TABLE IF NOT EXISTS memory_mutation_versions (
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    mutation_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (tenant_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO memory_mutation_versions (tenant_id, user_id, mutation_version, updated_at)
SELECT tenant_id, user_id, COUNT(*), MAX(updated_at)
FROM memory_records
GROUP BY tenant_id, user_id
ON DUPLICATE KEY UPDATE mutation_version=GREATEST(memory_mutation_versions.mutation_version, VALUES(mutation_version));
