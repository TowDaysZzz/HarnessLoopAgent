package mysqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO memory_edit_payloads (id,tenant_id,user_id,draft_json,content_hash,status,expires_at,created_at) VALUES (?,?,?,?,?,'pending',?,?)`, id, owner.TenantID, owner.UserID, raw, copyDraft.ContentHash, expiresAt.UTC(), now.UTC())
	return mapMemoryWriteError(err)
}

func (s *Store) ConsumeMemoryEditPayload(ctx context.Context, owner memory.Owner, id string, now time.Time) (memoryworkflow.Draft, error) {
	if !owner.Valid() || id == "" || len(id) > 191 {
		return memoryworkflow.Draft{}, memory.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memoryworkflow.Draft{}, err
	}
	defer tx.Rollback()
	var raw []byte
	var hash, status string
	var expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT draft_json,content_hash,status,expires_at FROM memory_edit_payloads WHERE id=? AND tenant_id=? AND user_id=? FOR UPDATE`, id, owner.TenantID, owner.UserID).Scan(&raw, &hash, &status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return memoryworkflow.Draft{}, memory.ErrNotFound
	}
	if err != nil {
		return memoryworkflow.Draft{}, err
	}
	if status != "pending" || !now.Before(expires) {
		return memoryworkflow.Draft{}, memory.ErrNotFound
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var draft memoryworkflow.Draft
	if err := dec.Decode(&draft); err != nil {
		return memoryworkflow.Draft{}, memoryworkflow.ErrInvalidEditPayload
	}
	copyDraft := draft
	if err := copyDraft.Normalize(); err != nil || copyDraft.ContentHash != hash || draft.ContentHash != hash {
		return memoryworkflow.Draft{}, memoryworkflow.ErrInvalidEditPayload
	}
	result, err := tx.ExecContext(ctx, `UPDATE memory_edit_payloads SET status='consumed',consumed_at=? WHERE id=? AND tenant_id=? AND user_id=? AND status='pending'`, now.UTC(), id, owner.TenantID, owner.UserID)
	if err != nil {
		return memoryworkflow.Draft{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return memoryworkflow.Draft{}, memory.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return memoryworkflow.Draft{}, err
	}
	return draft, nil
}
