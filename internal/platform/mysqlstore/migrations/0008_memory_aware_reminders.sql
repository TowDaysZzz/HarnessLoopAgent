CREATE TABLE IF NOT EXISTS reminders (
    id CHAR(36) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    content VARCHAR(4096) NOT NULL,
    content_hash CHAR(64) NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    next_fire_at DATETIME(6) NOT NULL,
    status VARCHAR(32) NOT NULL,
    row_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    source_type VARCHAR(64) NOT NULL,
    source_id VARCHAR(191) NOT NULL,
    last_error_code VARCHAR(128) NOT NULL DEFAULT '',
    claim_token VARCHAR(191) NULL,
    lease_until DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    KEY idx_reminders_owner_status_fire (tenant_id, user_id, status, next_fire_at, id),
    KEY idx_reminders_due_claim (status, next_fire_at, lease_until, id),
    KEY idx_reminders_owner_content_hash (tenant_id, user_id, content_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reminder_memory_refs (
    reminder_id CHAR(36) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    memory_id CHAR(36) NOT NULL,
    lineage_version BIGINT UNSIGNED NOT NULL,
    content_hash CHAR(64) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (reminder_id, memory_id),
    CONSTRAINT fk_reminder_memory_reminder FOREIGN KEY (reminder_id) REFERENCES reminders(id),
    CONSTRAINT fk_reminder_memory_record FOREIGN KEY (memory_id) REFERENCES memory_records(id),
    KEY idx_reminder_memory_owner (tenant_id, user_id, memory_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reminder_events (
    id CHAR(36) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    reminder_id CHAR(36) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    old_status VARCHAR(32) NULL,
    new_status VARCHAR(32) NOT NULL,
    actor VARCHAR(191) NOT NULL,
    reason_code VARCHAR(128) NOT NULL,
    execution_id VARCHAR(191) NOT NULL,
    input_hash CHAR(64) NOT NULL,
    occurred_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_reminder_events_reminder FOREIGN KEY (reminder_id) REFERENCES reminders(id),
    UNIQUE KEY uk_reminder_event_idempotency (tenant_id, user_id, execution_id),
    KEY idx_reminder_events_owner_reminder (tenant_id, user_id, reminder_id, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reminder_delivery_outbox (
    id VARCHAR(191) PRIMARY KEY,
    reminder_id CHAR(36) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    occurrence_id VARCHAR(191) NOT NULL,
    delivery_key VARCHAR(191) NOT NULL,
    content VARCHAR(4096) NOT NULL,
    status VARCHAR(32) NOT NULL,
    attempt INT UNSIGNED NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL,
    claim_token VARCHAR(191) NULL,
    lease_until DATETIME(6) NULL,
    processed_at DATETIME(6) NULL,
    last_error_code VARCHAR(128) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT fk_reminder_delivery_reminder FOREIGN KEY (reminder_id) REFERENCES reminders(id),
    UNIQUE KEY uk_reminder_occurrence (tenant_id, user_id, reminder_id, occurrence_id),
    UNIQUE KEY uk_reminder_delivery_key (delivery_key),
    KEY idx_reminder_delivery_claim (status, available_at, lease_until, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reminder_edit_payloads (
    id VARCHAR(191) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    edit_text TEXT NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    consumed_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    KEY idx_reminder_edit_owner_expiry (tenant_id, user_id, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
