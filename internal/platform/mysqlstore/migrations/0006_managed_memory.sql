CREATE TABLE IF NOT EXISTS memory_records (
    id CHAR(36) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    layer VARCHAR(32) NOT NULL,
    kind VARCHAR(64) NOT NULL,
    scope_type VARCHAR(32) NOT NULL,
    scope_id VARCHAR(128) NOT NULL DEFAULT '',
    namespace VARCHAR(128) NOT NULL,
    slot_key VARCHAR(191) NOT NULL DEFAULT '',
    entity_type VARCHAR(64) NOT NULL DEFAULT '',
    entity_id VARCHAR(191) NOT NULL DEFAULT '',
    lineage_id CHAR(36) NOT NULL,
    lineage_version BIGINT UNSIGNED NOT NULL,
    row_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    canonical_text TEXT NOT NULL,
    structured_value JSON NOT NULL,
    content_hash CHAR(64) NOT NULL,
    authority VARCHAR(32) NOT NULL,
    confidence DOUBLE NOT NULL,
    salience DOUBLE NOT NULL,
    source_type VARCHAR(64) NOT NULL,
    source_id VARCHAR(191) NOT NULL,
    evidence_id VARCHAR(191) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL,
    supersedes_id CHAR(36) NULL,
    superseded_by CHAR(36) NULL,
    expires_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    active_slot_guard VARCHAR(512) GENERATED ALWAYS AS (
        CASE WHEN status = 'active' AND slot_key <> ''
        THEN CONCAT(scope_type, ':', scope_id, ':', namespace, ':', slot_key)
        ELSE NULL END
    ) STORED,
    UNIQUE KEY uk_memory_lineage_version (tenant_id, user_id, lineage_id, lineage_version),
    UNIQUE KEY uk_memory_active_slot (tenant_id, user_id, active_slot_guard),
    KEY idx_memory_owner_status_expiry (tenant_id, user_id, status, expires_at),
    KEY idx_memory_owner_scope (tenant_id, user_id, scope_type, scope_id, namespace, slot_key),
    KEY idx_memory_owner_entity (tenant_id, user_id, entity_type, entity_id),
    KEY idx_memory_owner_hash (tenant_id, user_id, content_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS memory_events (
    id CHAR(36) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    memory_id CHAR(36) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    old_status VARCHAR(32) NULL,
    new_status VARCHAR(32) NOT NULL,
    actor VARCHAR(191) NOT NULL,
    reason_code VARCHAR(128) NOT NULL,
    execution_id VARCHAR(191) NOT NULL,
    input_hash CHAR(64) NOT NULL,
    result_memory_id CHAR(36) NOT NULL,
    occurred_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_memory_events_memory FOREIGN KEY (memory_id) REFERENCES memory_records(id),
    UNIQUE KEY uk_memory_event_idempotency (tenant_id, user_id, execution_id),
    KEY idx_memory_events_owner_memory (tenant_id, user_id, memory_id, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS memory_relations (
    id CHAR(36) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    from_memory_id CHAR(36) NOT NULL,
    to_memory_id CHAR(36) NOT NULL,
    relation_type VARCHAR(32) NOT NULL,
    reason_code VARCHAR(128) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_memory_relations_from FOREIGN KEY (from_memory_id) REFERENCES memory_records(id),
    CONSTRAINT fk_memory_relations_to FOREIGN KEY (to_memory_id) REFERENCES memory_records(id),
    UNIQUE KEY uk_memory_relation (tenant_id, user_id, from_memory_id, to_memory_id, relation_type),
    KEY idx_memory_relations_target (tenant_id, user_id, to_memory_id, relation_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS memory_projection_outbox (
    id VARCHAR(191) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    memory_id CHAR(36) NOT NULL,
    content_hash CHAR(64) NOT NULL,
    model_version VARCHAR(128) NOT NULL DEFAULT 'default',
    status VARCHAR(32) NOT NULL,
    attempt INT UNSIGNED NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL,
    claimed_at DATETIME(6) NULL,
    processed_at DATETIME(6) NULL,
    last_error_code VARCHAR(128) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_memory_projection_memory FOREIGN KEY (memory_id) REFERENCES memory_records(id),
    UNIQUE KEY uk_memory_projection_version (tenant_id, user_id, memory_id, content_hash, model_version),
    KEY idx_memory_projection_claim (status, available_at, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
