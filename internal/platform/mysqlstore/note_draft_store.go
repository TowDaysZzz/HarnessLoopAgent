package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
)

func (s *Store) ReplacePending(ctx context.Context, draft notedraft.Draft) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE note_drafts SET status='cancelled',updated_at=? WHERE user_id=? AND tenant_id=? AND session_id=? AND status='pending'`, draft.CreatedAt, draft.UserID, draft.TenantID, draft.SessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO note_drafts (id,user_id,tenant_id,session_id,title,content,status,content_hash,expires_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, draft.ID, draft.UserID, draft.TenantID, draft.SessionID, draft.Title, draft.Content, draft.Status, draft.ContentHash, draft.ExpiresAt, draft.CreatedAt, draft.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LatestPending(ctx context.Context, owner notedraft.Owner, sessionID string) (notedraft.Draft, error) {
	return scanNoteDraft(s.db.QueryRowContext(ctx, noteDraftSelect+` WHERE user_id=? AND tenant_id=? AND session_id=? AND status='pending' ORDER BY created_at DESC LIMIT 1`, owner.UserID, owner.TenantID, sessionID))
}

func (s *Store) Transition(ctx context.Context, owner notedraft.Owner, sessionID, id, contentHash string, status notedraft.Status, now time.Time) (notedraft.Draft, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return notedraft.Draft{}, false, err
	}
	defer tx.Rollback()
	draft, err := scanNoteDraft(tx.QueryRowContext(ctx, noteDraftSelect+` WHERE id=? AND user_id=? AND tenant_id=? AND session_id=? AND content_hash=? FOR UPDATE`, id, owner.UserID, owner.TenantID, sessionID, contentHash))
	if err != nil {
		return notedraft.Draft{}, false, err
	}
	if draft.Status == status {
		if err := tx.Commit(); err != nil {
			return notedraft.Draft{}, false, err
		}
		return draft, true, nil
	}
	if draft.Status != notedraft.StatusPending {
		return notedraft.Draft{}, false, notedraft.ErrInvalidState
	}
	if _, err := tx.ExecContext(ctx, `UPDATE note_drafts SET status=?,updated_at=? WHERE id=? AND status='pending'`, status, now, id); err != nil {
		return notedraft.Draft{}, false, err
	}
	draft.Status, draft.UpdatedAt = status, now
	return draft, false, tx.Commit()
}

const noteDraftSelect = `SELECT id,user_id,tenant_id,session_id,title,content,status,content_hash,expires_at,created_at,updated_at FROM note_drafts`

func scanNoteDraft(row scanner) (notedraft.Draft, error) {
	var draft notedraft.Draft
	if err := row.Scan(&draft.ID, &draft.UserID, &draft.TenantID, &draft.SessionID, &draft.Title, &draft.Content, &draft.Status, &draft.ContentHash, &draft.ExpiresAt, &draft.CreatedAt, &draft.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notedraft.Draft{}, notedraft.ErrNotFound
		}
		return notedraft.Draft{}, err
	}
	return draft, nil
}
