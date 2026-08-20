package mysqlstore

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
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
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
			}
		}
	}
	return s.ensureChatSessionOwnership(ctx)
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
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'chat_sessions' AND COLUMN_NAME = ?`, column.name).Scan(&count); err != nil {
			return fmt.Errorf("inspect chat_sessions.%s: %w", column.name, err)
		}
		if count == 0 {
			if _, err := s.db.ExecContext(ctx, "ALTER TABLE chat_sessions ADD COLUMN "+column.name+" "+column.definition); err != nil {
				return fmt.Errorf("add chat_sessions.%s: %w", column.name, err)
			}
		}
	}
	var indexCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'chat_sessions' AND INDEX_NAME = 'idx_chat_sessions_owner_updated'`).Scan(&indexCount); err != nil {
		return fmt.Errorf("inspect chat session owner index: %w", err)
	}
	if indexCount == 0 {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE chat_sessions ADD INDEX idx_chat_sessions_owner_updated (tenant_id, user_id, updated_at)`); err != nil {
			return fmt.Errorf("add chat session owner index: %w", err)
		}
	}
	// Existing sessions can only be assigned safely when the installation has exactly one distinct user.
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions cs
		JOIN (
			SELECT MIN(user_id) AS user_id, MIN(tenant_id) AS tenant_id
			FROM agent_user_sessions
			HAVING COUNT(DISTINCT CONCAT(tenant_id, ':', user_id)) = 1
		) owner
		SET cs.user_id = owner.user_id, cs.tenant_id = owner.tenant_id
		WHERE cs.user_id = 0 AND cs.tenant_id = 0`)
	if err != nil {
		return fmt.Errorf("backfill chat session owner: %w", err)
	}
	return nil
}
