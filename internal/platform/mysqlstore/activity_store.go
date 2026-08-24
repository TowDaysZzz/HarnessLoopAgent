package mysqlstore

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/dailyreview"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"gorm.io/gorm"
)

type chatActivityRow struct {
	ID, SessionID, RunID, Role, Content, ContentHash string
	Sequence                                         int64
	CreatedAt                                        time.Time
}

func (s *Store) SnapshotChat(ctx context.Context, owner skill.Owner, window dailyreview.Window, limit, perSession int) ([]dailyreview.ChatRef, bool, error) {
	if !owner.Valid() || !window.Valid() || limit < 1 || limit > 1000 || perSession < 1 || perSession > limit {
		return nil, false, dailyreview.ErrStaleSnapshot
	}
	var rows []chatActivityRow
	query := `SELECT id,session_id,COALESCE(run_id,'') run_id,role,sequence,created_at,SHA2(content,256) content_hash FROM (
 SELECT m.*, ROW_NUMBER() OVER (PARTITION BY m.session_id ORDER BY m.created_at,m.sequence,m.id) session_rank
 FROM chat_messages m JOIN chat_sessions s ON s.id=m.session_id
 WHERE s.tenant_id=? AND s.user_id=? AND m.created_at>=? AND m.created_at<?
 AND NOT EXISTS (SELECT 1 FROM skill_invocations i WHERE i.chat_run_id=m.run_id AND i.tenant_id=? AND i.user_id=? AND i.skill_id='daily_review')
) bounded WHERE session_rank<=? ORDER BY created_at,session_id,sequence,id LIMIT ?`
	if err := s.db.WithContext(ctx).Raw(query, owner.TenantID, owner.UserID, window.Start, window.End, owner.TenantID, owner.UserID, perSession, limit+1).Scan(&rows).Error; err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	refs := make([]dailyreview.ChatRef, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, dailyreview.ChatRef{ID: r.ID, SessionID: r.SessionID, RunID: r.RunID, Role: r.Role, Sequence: r.Sequence, ContentHash: r.ContentHash, CreatedAt: r.CreatedAt.UTC()})
	}
	return refs, truncated, nil
}

func (s *Store) LoadChatPinned(ctx context.Context, owner skill.Owner, refs []dailyreview.ChatRef) (map[string]string, error) {
	if len(refs) > 1000 {
		return nil, dailyreview.ErrStaleSnapshot
	}
	out := make(map[string]string, len(refs))
	for _, ref := range refs {
		var row chatActivityRow
		err := s.db.WithContext(ctx).Raw(`SELECT m.id,m.session_id,COALESCE(m.run_id,'') run_id,m.role,m.sequence,m.content,m.created_at,SHA2(m.content,256) content_hash FROM chat_messages m JOIN chat_sessions s ON s.id=m.session_id WHERE m.id=? AND s.tenant_id=? AND s.user_id=?`, ref.ID, owner.TenantID, owner.UserID).Scan(&row).Error
		if err != nil {
			return nil, err
		}
		if row.ID == "" || row.SessionID != ref.SessionID || row.RunID != ref.RunID || row.Role != ref.Role || row.Sequence != ref.Sequence || row.ContentHash != ref.ContentHash || !row.CreatedAt.UTC().Equal(ref.CreatedAt.UTC()) {
			return nil, dailyreview.ErrStaleSnapshot
		}
		out[ref.ID] = row.Content
	}
	return out, nil
}

type noteActivityRow struct {
	ID, Status, Content, ContentHash string
	OccurredAt, UpdatedAt            time.Time
}

func (s *Store) SnapshotNotes(ctx context.Context, owner skill.Owner, window dailyreview.Window, limit int) ([]dailyreview.NoteRef, bool, error) {
	if !owner.Valid() || !window.Valid() || limit < 1 || limit > 500 {
		return nil, false, dailyreview.ErrStaleSnapshot
	}
	var rows []noteActivityRow
	err := s.db.WithContext(ctx).Raw(`SELECT id,status,content_hash,COALESCE(occurred_at,created_at) occurred_at,updated_at FROM notes WHERE tenant_id=? AND user_id=? AND status<>'deleted' AND COALESCE(occurred_at,created_at)>=? AND COALESCE(occurred_at,created_at)<? ORDER BY occurred_at,id LIMIT ?`, owner.TenantID, owner.UserID, window.Start, window.End, limit+1).Scan(&rows).Error
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	refs := make([]dailyreview.NoteRef, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, dailyreview.NoteRef{ID: r.ID, Status: r.Status, Version: timeVersion(r.UpdatedAt), ContentHash: r.ContentHash, OccurredAt: r.OccurredAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
	}
	return refs, truncated, nil
}

func (s *Store) LoadNotesPinned(ctx context.Context, owner skill.Owner, refs []dailyreview.NoteRef) (map[string]string, error) {
	if len(refs) > 500 {
		return nil, dailyreview.ErrStaleSnapshot
	}
	out := make(map[string]string, len(refs))
	for _, ref := range refs {
		var row noteActivityRow
		err := s.db.WithContext(ctx).Raw(`SELECT id,status,content,content_hash,COALESCE(occurred_at,created_at) occurred_at,updated_at FROM notes WHERE id=? AND tenant_id=? AND user_id=?`, ref.ID, owner.TenantID, owner.UserID).Scan(&row).Error
		if err != nil {
			return nil, err
		}
		if row.ID == "" || row.Status != ref.Status || timeVersion(row.UpdatedAt) != ref.Version || row.ContentHash != ref.ContentHash || !row.OccurredAt.UTC().Equal(ref.OccurredAt.UTC()) {
			return nil, dailyreview.ErrStaleSnapshot
		}
		out[ref.ID] = row.Content
	}
	return out, nil
}

func (s *Store) MemoryMutationVersion(ctx context.Context, owner skill.Owner) (uint64, error) {
	if !owner.Valid() {
		return 0, dailyreview.ErrStaleSnapshot
	}
	var row memoryMutationVersionRow
	err := s.db.WithContext(ctx).Where("tenant_id=? AND user_id=?", owner.TenantID, owner.UserID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return row.MutationVersion, err
}

func timeVersion(value time.Time) uint64 { return uint64(value.UTC().UnixMicro()) }

func normalizeActivityRefs(chat []dailyreview.ChatRef, notes []dailyreview.NoteRef) {
	sort.Slice(chat, func(i, j int) bool { return chat[i].ID < chat[j].ID })
	sort.Slice(notes, func(i, j int) bool { return notes[i].ID < notes[j].ID })
}
