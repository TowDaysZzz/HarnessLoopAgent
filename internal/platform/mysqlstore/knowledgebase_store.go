package mysqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
)

func (s *Store) GetKnowledgeBaseBinding(ctx context.Context, userID, tenantID uint64) (knowledgebase.Binding, error) {
	var value knowledgebase.Binding
	err := s.db.QueryRowContext(ctx, `SELECT user_id,tenant_id,rag_kb_id,name,status,created_at,updated_at
		FROM agent_user_knowledge_bases WHERE user_id=? AND tenant_id=?`, userID, tenantID).Scan(
		&value.UserID, &value.TenantID, &value.RAGKBID, &value.Name, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledgebase.Binding{}, knowledgebase.ErrNotConfigured
	}
	return value, err
}

func (s *Store) UpsertKnowledgeBaseBinding(ctx context.Context, value knowledgebase.Binding) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_user_knowledge_bases
		(user_id,tenant_id,rag_kb_id,name,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE rag_kb_id=VALUES(rag_kb_id),name=VALUES(name),status=VALUES(status),updated_at=VALUES(updated_at)`,
		value.UserID, value.TenantID, value.RAGKBID, value.Name, value.Status, value.CreatedAt, value.UpdatedAt)
	return err
}
