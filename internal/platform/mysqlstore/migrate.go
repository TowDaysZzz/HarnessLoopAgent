package mysqlstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrations embed.FS

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		for _, statement := range strings.Split(string(body), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if err := s.db.WithContext(ctx).Exec(statement).Error; err != nil && !isDuplicateIndexError(err) {
				return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
			}
		}
	}
	if err := s.ensureChatSessionOwnership(ctx); err != nil {
		return err
	}
	return s.ensureMemoryExactIndexes(ctx)
}

func isDuplicateIndexError(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1061
}

func (s *Store) ensureMemoryExactIndexes(ctx context.Context) error {
	indexes := []struct {
		name    string
		columns string
	}{
		{name: "idx_memory_exact_slot_active", columns: "tenant_id,user_id,scope_type,scope_id,namespace,slot_key,status,expires_at"},
		{name: "idx_memory_exact_entity_active", columns: "tenant_id,user_id,scope_type,scope_id,entity_type,entity_id,status,expires_at"},
		{name: "idx_memory_exact_hash_active", columns: "tenant_id,user_id,scope_type,scope_id,content_hash,status,expires_at"},
	}
	for _, index := range indexes {
		var count int
		if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='memory_records' AND INDEX_NAME=?`, index.name).Scan(&count).Error; err != nil {
			return fmt.Errorf("inspect memory index %s: %w", index.name, err)
		}
		if count != 0 {
			continue
		}
		if err := s.db.WithContext(ctx).Exec("ALTER TABLE memory_records ADD INDEX " + index.name + " (" + index.columns + ")").Error; err != nil {
			// Another process may have added the index after our information_schema
			// check. Re-read the schema before treating the ALTER failure as fatal.
			if inspectErr := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='memory_records' AND INDEX_NAME=?`, index.name).Scan(&count).Error; inspectErr != nil {
				return fmt.Errorf("inspect memory index %s after add failure: %w", index.name, inspectErr)
			}
			if count == 0 {
				return fmt.Errorf("add memory index %s: %w", index.name, err)
			}
		}
	}
	return nil
}

func (s *Store) ensureChatSessionOwnership(ctx context.Context) error {
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "user_id", definition: "BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER id"},
		{name: "tenant_id", definition: "BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER user_id"},
	} {
		var count int
		if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'chat_sessions' AND COLUMN_NAME = ?`, column.name).Scan(&count).Error; err != nil {
			return fmt.Errorf("inspect chat_sessions.%s: %w", column.name, err)
		}
		if count == 0 {
			if err := s.db.WithContext(ctx).Exec("ALTER TABLE chat_sessions ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
				return fmt.Errorf("add chat_sessions.%s: %w", column.name, err)
			}
		}
	}
	var indexCount int
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'chat_sessions' AND INDEX_NAME = 'idx_chat_sessions_owner_updated'`).Scan(&indexCount).Error; err != nil {
		return fmt.Errorf("inspect chat session owner index: %w", err)
	}
	if indexCount == 0 {
		if err := s.db.WithContext(ctx).Exec(`ALTER TABLE chat_sessions ADD INDEX idx_chat_sessions_owner_updated (tenant_id, user_id, updated_at)`).Error; err != nil {
			return fmt.Errorf("add chat session owner index: %w", err)
		}
	}
	// Existing sessions can only be assigned safely when the installation has exactly one distinct user.
	err := s.db.WithContext(ctx).Exec(`UPDATE chat_sessions cs
		JOIN (
			SELECT MIN(user_id) AS user_id, MIN(tenant_id) AS tenant_id
			FROM agent_user_sessions
			HAVING COUNT(DISTINCT CONCAT(tenant_id, ':', user_id)) = 1
		) owner
		SET cs.user_id = owner.user_id, cs.tenant_id = owner.tenant_id
		WHERE cs.user_id = 0 AND cs.tenant_id = 0`).Error
	if err != nil {
		return fmt.Errorf("backfill chat session owner: %w", err)
	}
	return nil
}
