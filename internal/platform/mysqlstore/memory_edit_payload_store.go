package mysqlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) PutMemoryEditPayload(ctx context.Context, owner memory.Owner, id string, draft memoryworkflow.Draft, expiresAt, now time.Time) error {
	if !owner.Valid() || id == "" || len(id) > 191 || !expiresAt.After(now) {
		return memoryworkflow.ErrInvalidEditPayload
	}
	copyDraft := draft
	if err := copyDraft.Normalize(); err != nil {
		return err
	}
	if copyDraft.ContentHash != draft.ContentHash {
		return memoryworkflow.ErrInvalidEditPayload
	}
	raw, err := json.Marshal(copyDraft)
	if err != nil || len(raw) > memory.MaxStructuredBytes*2 {
		return memoryworkflow.ErrInvalidEditPayload
	}
	err = s.db.WithContext(ctx).Create(&memoryEditPayloadRow{ID: id, TenantID: owner.TenantID, UserID: owner.UserID, DraftJSON: raw, ContentHash: copyDraft.ContentHash, Status: "pending", ExpiresAt: expiresAt.UTC(), CreatedAt: now.UTC()}).Error
	return mapMemoryWriteError(err)
}

func (s *Store) ConsumeMemoryEditPayload(ctx context.Context, owner memory.Owner, id string, now time.Time) (memoryworkflow.Draft, error) {
	if !owner.Valid() || id == "" || len(id) > 191 {
		return memoryworkflow.Draft{}, memory.ErrNotFound
	}
	var draft memoryworkflow.Draft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row memoryEditPayloadRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND user_id=?", id, owner.TenantID, owner.UserID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return memory.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Status != "pending" || !now.Before(row.ExpiresAt) {
			return memory.ErrNotFound
		}
		dec := json.NewDecoder(bytes.NewReader(row.DraftJSON))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&draft); err != nil {
			return memoryworkflow.ErrInvalidEditPayload
		}
		copyDraft := draft
		if err := copyDraft.Normalize(); err != nil || copyDraft.ContentHash != row.ContentHash || draft.ContentHash != row.ContentHash {
			return memoryworkflow.ErrInvalidEditPayload
		}
		result := tx.Model(&memoryEditPayloadRow{}).Where("id=? AND tenant_id=? AND user_id=? AND status='pending'", id, owner.TenantID, owner.UserID).Updates(map[string]any{"status": "consumed", "consumed_at": now.UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return memory.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return memoryworkflow.Draft{}, err
	}
	return draft, nil
}
