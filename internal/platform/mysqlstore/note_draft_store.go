package mysqlstore

import (
	"context"
	"errors"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) ReplacePending(ctx context.Context, draft notedraft.Draft) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&noteDraftRow{}).Where("user_id=? AND tenant_id=? AND session_id=? AND status='pending'", draft.UserID, draft.TenantID, draft.SessionID).Updates(map[string]any{"status": "cancelled", "updated_at": draft.CreatedAt}).Error; err != nil {
			return err
		}
		return tx.Create(noteDraftToRow(draft)).Error
	})
}

func (s *Store) LatestPending(ctx context.Context, owner notedraft.Owner, sessionID string) (notedraft.Draft, error) {
	var row noteDraftRow
	err := s.db.WithContext(ctx).Where("user_id=? AND tenant_id=? AND session_id=? AND status='pending'", owner.UserID, owner.TenantID, sessionID).Order("created_at DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notedraft.Draft{}, notedraft.ErrNotFound
	}
	return noteDraftFromRow(row), err
}

func (s *Store) Transition(ctx context.Context, owner notedraft.Owner, sessionID, id, contentHash string, status notedraft.Status, now time.Time) (notedraft.Draft, bool, error) {
	var draft notedraft.Draft
	replayed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row noteDraftRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND tenant_id=? AND session_id=? AND content_hash=?", id, owner.UserID, owner.TenantID, sessionID, contentHash).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return notedraft.ErrNotFound
		}
		if err != nil {
			return err
		}
		draft = noteDraftFromRow(row)
		if draft.Status == status {
			replayed = true
			return nil
		}
		if draft.Status != notedraft.StatusPending {
			return notedraft.ErrInvalidState
		}
		result := tx.Model(&noteDraftRow{}).Where("id=? AND status='pending'", id).Updates(map[string]any{"status": status, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return notedraft.ErrInvalidState
		}
		draft.Status, draft.UpdatedAt = status, now
		return nil
	})
	return draft, replayed, err
}

func noteDraftToRow(v notedraft.Draft) *noteDraftRow {
	return &noteDraftRow{ID: v.ID, UserID: v.UserID, TenantID: v.TenantID, SessionID: v.SessionID, Title: v.Title, Content: v.Content, Status: string(v.Status), ContentHash: v.ContentHash, ExpiresAt: v.ExpiresAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func noteDraftFromRow(v noteDraftRow) notedraft.Draft {
	return notedraft.Draft{ID: v.ID, UserID: v.UserID, TenantID: v.TenantID, SessionID: v.SessionID, Title: v.Title, Content: v.Content, Status: notedraft.Status(v.Status), ContentHash: v.ContentHash, ExpiresAt: v.ExpiresAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
