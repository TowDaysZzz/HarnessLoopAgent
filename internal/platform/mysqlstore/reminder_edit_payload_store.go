package mysqlstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
)

type reminderEditPayloadRow struct {
	ID                   string `gorm:"column:id;primaryKey"`
	TenantID, UserID     uint64
	EditText             string
	ExpiresAt, CreatedAt time.Time
	ConsumedAt           *time.Time
}

func (reminderEditPayloadRow) TableName() string { return "reminder_edit_payloads" }

func (s *Store) PutReminderEditPayload(ctx context.Context, owner reminder.Owner, id, text string, expiresAt, now time.Time) error {
	if s == nil || !owner.Valid() || id == "" || text == "" || !expiresAt.After(now) {
		return reminderworkflow.ErrInvalidEditPayload
	}
	row := reminderEditPayloadRow{ID: id, TenantID: owner.TenantID, UserID: owner.UserID, EditText: text, ExpiresAt: expiresAt.UTC(), CreatedAt: now.UTC()}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return mapReminderWriteError(err)
	}
	return nil
}

func (s *Store) ConsumeReminderEditPayload(ctx context.Context, owner reminder.Owner, id string, now time.Time) (text string, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row reminderEditPayloadRow
		find := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND user_id=? AND consumed_at IS NULL AND expires_at>?", id, owner.TenantID, owner.UserID, now.UTC()).First(&row).Error
		if errors.Is(find, gorm.ErrRecordNotFound) {
			return reminder.ErrNotFound
		}
		if find != nil {
			return find
		}
		write := tx.Model(&reminderEditPayloadRow{}).Where("id=? AND consumed_at IS NULL", id).Update("consumed_at", now.UTC())
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return reminder.ErrStateConflict
		}
		text = row.EditText
		return nil
	})
	return
}

var _ reminderworkflow.EditPayloadStore = (*Store)(nil)
