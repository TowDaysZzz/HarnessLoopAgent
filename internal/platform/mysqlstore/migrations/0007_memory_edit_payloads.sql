CREATE TABLE IF NOT EXISTS memory_edit_payloads (
    id VARCHAR(191) PRIMARY KEY,
    tenant_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    draft_json JSON NOT NULL,
    content_hash CHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    consumed_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    KEY idx_memory_edit_owner_status_expiry (tenant_id, user_id, status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
