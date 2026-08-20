package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
)

func (s *Store) CreateAuthSession(ctx context.Context, session agentauth.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_user_sessions
		(id,user_id,tenant_id,role,email,name,access_token_ciphertext,refresh_token_ciphertext,access_expires_at,expires_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, session.ID, session.UserID, session.TenantID, session.Role, session.Email, session.Name,
		session.EncryptedAccessToken, session.EncryptedRefreshToken, session.AccessExpiresAt, session.ExpiresAt, session.CreatedAt, session.UpdatedAt)
	return err
}

func (s *Store) GetAuthSession(ctx context.Context, id string) (agentauth.Session, error) {
	var session agentauth.Session
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,tenant_id,role,email,name,access_token_ciphertext,refresh_token_ciphertext,
		access_expires_at,expires_at,created_at,updated_at FROM agent_user_sessions WHERE id=?`, id).Scan(
		&session.ID, &session.UserID, &session.TenantID, &session.Role, &session.Email, &session.Name,
		&session.EncryptedAccessToken, &session.EncryptedRefreshToken, &session.AccessExpiresAt, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return agentauth.Session{}, agentauth.ErrUnauthenticated
	}
	return session, err
}

func (s *Store) UpdateAuthSessionTokens(ctx context.Context, session agentauth.Session) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_user_sessions SET role=?,access_token_ciphertext=?,refresh_token_ciphertext=?,
		access_expires_at=?,updated_at=? WHERE id=?`, session.Role, session.EncryptedAccessToken, session.EncryptedRefreshToken,
		session.AccessExpiresAt, session.UpdatedAt, session.ID)
	if err != nil {
		return err
	}
	return requireAffected(result, agentauth.ErrUnauthenticated)
}

func (s *Store) DeleteAuthSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_user_sessions WHERE id=?`, id)
	return err
}

func (s *Store) CreateNoteWithOutbox(ctx context.Context, value note.Note, idempotencyKey string, event note.OutboxEvent) (note.Note, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return note.Note{}, false, err
	}
	defer tx.Rollback()
	if existing, err := getNoteByCreateKey(ctx, tx, value.UserID, value.TenantID, idempotencyKey); err == nil {
		if existing.ContentHash != value.ContentHash || existing.Title != value.Title || existing.Content != value.Content {
			return note.Note{}, false, note.ErrConflict
		}
		return existing, true, tx.Commit()
	} else if !errors.Is(err, note.ErrNotFound) {
		return note.Note{}, false, err
	}
	tags, err := json.Marshal(value.Tags)
	if err != nil {
		return note.Note{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notes
		(id,user_id,tenant_id,external_note_id,create_idempotency_key,title,content,tags,occurred_at,status,rag_kb_id,rag_status,content_hash,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.UserID, value.TenantID, value.ExternalNoteID, idempotencyKey,
		value.Title, value.Content, tags, value.OccurredAt, value.Status, value.RAGKBID, value.RAGStatus, value.ContentHash, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return note.Note{}, false, err
	}
	if err := insertNoteOutbox(ctx, tx, event, idempotencyKey); err != nil {
		return note.Note{}, false, err
	}
	return value, false, tx.Commit()
}

func (s *Store) GetNote(ctx context.Context, userID, tenantID uint64, id string) (note.Note, error) {
	return scanNote(s.db.QueryRowContext(ctx, noteSelect+` WHERE user_id=? AND tenant_id=? AND id=?`, userID, tenantID, id))
}

func (s *Store) ListNotes(ctx context.Context, userID, tenantID uint64, limit int, cursor string) ([]note.Note, error) {
	query := noteSelect + ` WHERE user_id=? AND tenant_id=? AND status<>'deleted'`
	args := []any{userID, tenantID}
	if cursor != "" {
		query += ` AND id<?`
		args = append(args, cursor)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []note.Note
	for rows.Next() {
		value, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) QueueNoteDelete(ctx context.Context, userID, tenantID uint64, noteID, key string, event note.OutboxEvent) (note.Note, bool, error) {
	if key == "" {
		return note.Note{}, false, note.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return note.Note{}, false, err
	}
	defer tx.Rollback()
	value, err := scanNote(tx.QueryRowContext(ctx, noteSelect+` WHERE user_id=? AND tenant_id=? AND id=? FOR UPDATE`, userID, tenantID, noteID))
	if err != nil {
		return note.Note{}, false, err
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM note_outbox_events WHERE tenant_id=? AND user_id=? AND event_type=? AND idempotency_key=?`, tenantID, userID, "note.delete", key).Scan(&existing); err == nil {
		return value, true, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return note.Note{}, false, err
	}
	if value.Status == note.StatusDeleted {
		return value, true, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET status='delete_pending',last_error='',updated_at=? WHERE id=?`, event.CreatedAt, noteID); err != nil {
		return note.Note{}, false, err
	}
	if err := insertNoteOutbox(ctx, tx, event, key); err != nil {
		return note.Note{}, false, err
	}
	value.Status = note.StatusDeletePending
	value.UpdatedAt = event.CreatedAt
	return value, false, tx.Commit()
}

func (s *Store) ClaimNoteOutbox(ctx context.Context, userID, tenantID uint64, limit int) ([]note.OutboxEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,note_id,user_id,tenant_id,event_type,attempt,created_at,available_at
		FROM note_outbox_events WHERE user_id=? AND tenant_id=? AND status IN ('pending','failed') AND available_at<=?
		ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED`, userID, tenantID, time.Now().UTC(), limit)
	if err != nil {
		return nil, err
	}
	var events []note.OutboxEvent
	for rows.Next() {
		var event note.OutboxEvent
		if err := rows.Scan(&event.ID, &event.NoteID, &event.UserID, &event.TenantID, &event.EventType, &event.Attempt, &event.CreatedAt, &event.AvailableAt); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range events {
		events[i].Attempt++
		if _, err := tx.ExecContext(ctx, `UPDATE note_outbox_events SET status='processing',attempt=? WHERE id=?`, events[i].Attempt, events[i].ID); err != nil {
			return nil, err
		}
	}
	return events, tx.Commit()
}

func (s *Store) CompleteNoteCreate(ctx context.Context, event note.OutboxEvent, documentID, jobID uint64, ragStatus string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := note.StatusIndexing
	if ragStatus == "completed" {
		status = note.StatusIndexed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET status=?,rag_document_id=?,rag_job_id=?,rag_status=?,last_error='',updated_at=? WHERE id=? AND user_id=? AND tenant_id=?`,
		status, documentID, jobID, ragStatus, time.Now().UTC(), event.NoteID, event.UserID, event.TenantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE note_outbox_events SET status='completed',processed_at=?,last_error='' WHERE id=?`, time.Now().UTC(), event.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateNoteJobStatus(ctx context.Context, userID, tenantID uint64, noteID, status, ragStatus, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notes SET status=?,rag_status=?,last_error=?,updated_at=? WHERE id=? AND user_id=? AND tenant_id=?`,
		status, ragStatus, lastError, time.Now().UTC(), noteID, userID, tenantID)
	return err
}

func (s *Store) CompleteNoteDelete(ctx context.Context, event note.OutboxEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET status='deleted',rag_status='deleted',last_error='',deleted_at=?,updated_at=? WHERE id=? AND user_id=? AND tenant_id=?`, now, now, event.NoteID, event.UserID, event.TenantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE note_outbox_events SET status='completed',processed_at=?,last_error='' WHERE id=?`, now, event.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailNoteProjection(ctx context.Context, event note.OutboxEvent, message string, retryAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := note.StatusIndexFailed
	if event.EventType == "note.delete" {
		status = note.StatusDeletePending
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET status=?,last_error=?,updated_at=? WHERE id=? AND user_id=? AND tenant_id=?`, status, message, time.Now().UTC(), event.NoteID, event.UserID, event.TenantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE note_outbox_events SET status='failed',last_error=?,available_at=? WHERE id=?`, message, retryAt, event.ID); err != nil {
		return err
	}
	return tx.Commit()
}

const noteSelect = `SELECT id,user_id,tenant_id,external_note_id,title,content,tags,occurred_at,status,rag_kb_id,
	COALESCE(rag_document_id,0),COALESCE(rag_job_id,0),rag_status,last_error,content_hash,deleted_at,created_at,updated_at FROM notes`

type scanner interface{ Scan(...any) error }

func scanNote(row scanner) (note.Note, error) {
	var value note.Note
	var tags []byte
	if err := row.Scan(&value.ID, &value.UserID, &value.TenantID, &value.ExternalNoteID, &value.Title, &value.Content, &tags,
		&value.OccurredAt, &value.Status, &value.RAGKBID, &value.RAGDocumentID, &value.RAGJobID, &value.RAGStatus, &value.LastError,
		&value.ContentHash, &value.DeletedAt, &value.CreatedAt, &value.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return note.Note{}, note.ErrNotFound
		}
		return note.Note{}, err
	}
	if err := json.Unmarshal(tags, &value.Tags); err != nil {
		return note.Note{}, fmt.Errorf("decode note tags: %w", err)
	}
	return value, nil
}

func getNoteByCreateKey(ctx context.Context, tx *sql.Tx, userID, tenantID uint64, key string) (note.Note, error) {
	return scanNote(tx.QueryRowContext(ctx, noteSelect+` WHERE user_id=? AND tenant_id=? AND create_idempotency_key=?`, userID, tenantID, key))
}

func insertNoteOutbox(ctx context.Context, tx *sql.Tx, event note.OutboxEvent, key string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO note_outbox_events
		(id,note_id,user_id,tenant_id,event_type,idempotency_key,status,attempt,available_at,created_at)
		VALUES (?,?,?,?,?,?,'pending',0,?,?)`, event.ID, event.NoteID, event.UserID, event.TenantID, event.EventType, key, event.AvailableAt, event.CreatedAt)
	return err
}

func requireAffected(result sql.Result, notFound error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return notFound
	}
	return nil
}
