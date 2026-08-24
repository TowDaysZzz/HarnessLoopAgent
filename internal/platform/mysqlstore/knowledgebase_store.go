package mysqlstore

import (
	"context"
	"errors"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) GetKnowledgeBaseBinding(ctx context.Context, userID, tenantID uint64) (knowledgebase.Binding, error) {
	var row knowledgeBaseRow
	err := s.db.WithContext(ctx).Where("user_id=? AND tenant_id=?", userID, tenantID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return knowledgebase.Binding{}, knowledgebase.ErrNotConfigured
	}
	return knowledgebase.Binding{UserID: row.UserID, TenantID: row.TenantID, RAGKBID: row.RAGKBID, Name: row.Name, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, err
}

func (s *Store) UpsertKnowledgeBaseBinding(ctx context.Context, value knowledgebase.Binding) error {
	row := knowledgeBaseRow{UserID: value.UserID, TenantID: value.TenantID, RAGKBID: value.RAGKBID, Name: value.Name, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"rag_kb_id", "name", "status", "updated_at"})}).Create(&row).Error
}
