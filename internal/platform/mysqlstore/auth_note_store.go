package mysqlstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CreateAuthSession(ctx context.Context, value agentauth.Session) error {
	return s.db.WithContext(ctx).Create(&authSessionRow{ID: value.ID, UserID: value.UserID, TenantID: value.TenantID, Role: value.Role, Email: value.Email, Name: value.Name, AccessTokenCiphertext: value.EncryptedAccessToken, RefreshTokenCiphertext: value.EncryptedRefreshToken, AccessExpiresAt: value.AccessExpiresAt, ExpiresAt: value.ExpiresAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}).Error
}

func (s *Store) GetAuthSession(ctx context.Context, id string) (agentauth.Session, error) {
	var row authSessionRow
	err := s.db.WithContext(ctx).Where("id=?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agentauth.Session{}, agentauth.ErrUnauthenticated
	}
	return agentauth.Session{ID: row.ID, UserID: row.UserID, TenantID: row.TenantID, Role: row.Role, Email: row.Email, Name: row.Name, EncryptedAccessToken: row.AccessTokenCiphertext, EncryptedRefreshToken: row.RefreshTokenCiphertext, AccessExpiresAt: row.AccessExpiresAt, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, err
}

func (s *Store) UpdateAuthSessionTokens(ctx context.Context, value agentauth.Session) error {
	result := s.db.WithContext(ctx).Model(&authSessionRow{}).Where("id=?", value.ID).Updates(map[string]any{"role": value.Role, "access_token_ciphertext": value.EncryptedAccessToken, "refresh_token_ciphertext": value.EncryptedRefreshToken, "access_expires_at": value.AccessExpiresAt, "updated_at": value.UpdatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return agentauth.ErrUnauthenticated
	}
	return nil
}

func (s *Store) DeleteAuthSession(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id=?", id).Delete(&authSessionRow{}).Error
}

func (s *Store) CreateNoteWithOutbox(ctx context.Context, value note.Note, idempotencyKey string, event note.OutboxEvent) (result note.Note, replayed bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, loadErr := getNoteByCreateKey(tx, value.UserID, value.TenantID, idempotencyKey)
		if loadErr == nil {
			if existing.ContentHash != value.ContentHash || existing.Title != value.Title || existing.Content != value.Content {
				return note.ErrConflict
			}
			result, replayed = existing, true
			return nil
		}
		if !errors.Is(loadErr, note.ErrNotFound) {
			return loadErr
		}
		row, convertErr := noteToRow(value, idempotencyKey)
		if convertErr != nil {
			return convertErr
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := insertNoteOutbox(tx, event, idempotencyKey); err != nil {
			return err
		}
		result = value
		return nil
	})
	return
}

func (s *Store) GetNote(ctx context.Context, userID, tenantID uint64, id string) (note.Note, error) {
	var row noteRow
	err := s.db.WithContext(ctx).Where("user_id=? AND tenant_id=? AND id=?", userID, tenantID, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return note.Note{}, note.ErrNotFound
	}
	if err != nil {
		return note.Note{}, err
	}
	return noteFromRow(row)
}

func (s *Store) ListNotes(ctx context.Context, userID, tenantID uint64, limit int, cursor string) ([]note.Note, error) {
	query := s.db.WithContext(ctx).Where("user_id=? AND tenant_id=? AND status<>'deleted'", userID, tenantID)
	if cursor != "" {
		query = query.Where("id<?", cursor)
	}
	var rows []noteRow
	if err := query.Order("created_at DESC,id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]note.Note, 0, len(rows))
	for _, row := range rows {
		value, err := noteFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) QueueNoteDelete(ctx context.Context, userID, tenantID uint64, noteID, key string, event note.OutboxEvent) (result note.Note, replayed bool, err error) {
	if key == "" {
		return note.Note{}, false, note.ErrInvalidInput
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row noteRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND tenant_id=? AND id=?", userID, tenantID, noteID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return note.ErrNotFound
		}
		if err != nil {
			return err
		}
		result, err = noteFromRow(row)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&noteOutboxRow{}).Where("tenant_id=? AND user_id=? AND event_type=? AND idempotency_key=?", tenantID, userID, "note.delete", key).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 || result.Status == note.StatusDeleted {
			replayed = true
			return nil
		}
		if err := tx.Model(&noteRow{}).Where("id=? AND user_id=? AND tenant_id=?", noteID, userID, tenantID).Updates(map[string]any{"status": "delete_pending", "last_error": "", "updated_at": event.CreatedAt}).Error; err != nil {
			return err
		}
		if err := insertNoteOutbox(tx, event, key); err != nil {
			return err
		}
		result.Status, result.UpdatedAt = note.StatusDeletePending, event.CreatedAt
		return nil
	})
	return
}

func (s *Store) ClaimNoteOutbox(ctx context.Context, userID, tenantID uint64, limit int) (events []note.OutboxEvent, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []noteOutboxRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("user_id=? AND tenant_id=? AND status IN ? AND available_at<=?", userID, tenantID, []string{"pending", "failed"}, time.Now().UTC()).Order("created_at").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			attempt := row.Attempt + 1
			result := tx.Model(&noteOutboxRow{}).Where("id=? AND status IN ?", row.ID, []string{"pending", "failed"}).Updates(map[string]any{"status": "processing", "attempt": attempt})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return note.ErrConflict
			}
			events = append(events, note.OutboxEvent{ID: row.ID, NoteID: row.NoteID, UserID: row.UserID, TenantID: row.TenantID, EventType: row.EventType, Attempt: attempt, CreatedAt: row.CreatedAt, AvailableAt: row.AvailableAt})
		}
		return nil
	})
	return
}

func (s *Store) CompleteNoteCreate(ctx context.Context, event note.OutboxEvent, documentID, jobID uint64, ragStatus string) error {
	status := note.StatusIndexing
	if ragStatus == "completed" {
		status = note.StatusIndexed
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&noteRow{}).Where("id=? AND user_id=? AND tenant_id=?", event.NoteID, event.UserID, event.TenantID).Updates(map[string]any{"status": status, "rag_document_id": documentID, "rag_job_id": jobID, "rag_status": ragStatus, "last_error": "", "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&noteOutboxRow{}).Where("id=?", event.ID).Updates(map[string]any{"status": "completed", "processed_at": now, "last_error": ""}).Error
	})
}

func (s *Store) UpdateNoteJobStatus(ctx context.Context, userID, tenantID uint64, noteID, status, ragStatus, lastError string) error {
	return s.db.WithContext(ctx).Model(&noteRow{}).Where("id=? AND user_id=? AND tenant_id=?", noteID, userID, tenantID).Updates(map[string]any{"status": status, "rag_status": ragStatus, "last_error": lastError, "updated_at": time.Now().UTC()}).Error
}

func (s *Store) CompleteNoteDelete(ctx context.Context, event note.OutboxEvent) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&noteRow{}).Where("id=? AND user_id=? AND tenant_id=?", event.NoteID, event.UserID, event.TenantID).Updates(map[string]any{"status": "deleted", "rag_status": "deleted", "last_error": "", "deleted_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&noteOutboxRow{}).Where("id=?", event.ID).Updates(map[string]any{"status": "completed", "processed_at": now, "last_error": ""}).Error
	})
}

func (s *Store) FailNoteProjection(ctx context.Context, event note.OutboxEvent, message string, retryAt time.Time) error {
	status := note.StatusIndexFailed
	if event.EventType == "note.delete" {
		status = note.StatusDeletePending
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&noteRow{}).Where("id=? AND user_id=? AND tenant_id=?", event.NoteID, event.UserID, event.TenantID).Updates(map[string]any{"status": status, "last_error": message, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Model(&noteOutboxRow{}).Where("id=?", event.ID).Updates(map[string]any{"status": "failed", "last_error": message, "available_at": retryAt}).Error
	})
}

func noteToRow(value note.Note, key string) (noteRow, error) {
	tags, err := json.Marshal(value.Tags)
	if err != nil {
		return noteRow{}, err
	}
	return noteRow{ID: value.ID, UserID: value.UserID, TenantID: value.TenantID, ExternalNoteID: value.ExternalNoteID, CreateIdempotencyKey: key, Title: value.Title, Content: value.Content, Tags: tags, OccurredAt: value.OccurredAt, Status: string(value.Status), RAGKBID: value.RAGKBID, RAGDocumentID: uintPtr(value.RAGDocumentID), RAGJobID: uintPtr(value.RAGJobID), RAGStatus: value.RAGStatus, LastError: value.LastError, ContentHash: value.ContentHash, DeletedAt: value.DeletedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}, nil
}

func noteFromRow(row noteRow) (note.Note, error) {
	var tags []string
	if err := json.Unmarshal(row.Tags, &tags); err != nil {
		return note.Note{}, fmt.Errorf("decode note tags: %w", err)
	}
	return note.Note{ID: row.ID, UserID: row.UserID, TenantID: row.TenantID, ExternalNoteID: row.ExternalNoteID, Title: row.Title, Content: row.Content, Tags: tags, OccurredAt: row.OccurredAt, Status: note.Status(row.Status), RAGKBID: row.RAGKBID, RAGDocumentID: uintValue(row.RAGDocumentID), RAGJobID: uintValue(row.RAGJobID), RAGStatus: row.RAGStatus, LastError: row.LastError, ContentHash: row.ContentHash, DeletedAt: row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func getNoteByCreateKey(tx *gorm.DB, userID, tenantID uint64, key string) (note.Note, error) {
	var row noteRow
	err := tx.Where("user_id=? AND tenant_id=? AND create_idempotency_key=?", userID, tenantID, key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return note.Note{}, note.ErrNotFound
	}
	if err != nil {
		return note.Note{}, err
	}
	return noteFromRow(row)
}

func insertNoteOutbox(tx *gorm.DB, event note.OutboxEvent, key string) error {
	return tx.Create(&noteOutboxRow{ID: event.ID, NoteID: event.NoteID, UserID: event.UserID, TenantID: event.TenantID, EventType: event.EventType, IdempotencyKey: key, Status: "pending", Attempt: 0, AvailableAt: event.AvailableAt, CreatedAt: event.CreatedAt}).Error
}

func uintPtr(value uint64) *uint64 {
	if value == 0 {
		return nil
	}
	return &value
}
func uintValue(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}
