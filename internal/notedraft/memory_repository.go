package notedraft

import (
	"context"
	"sync"
	"time"
)

type MemoryRepository struct {
	mu     sync.Mutex
	drafts map[string]Draft
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{drafts: make(map[string]Draft)}
}

func (r *MemoryRepository) ReplacePending(_ context.Context, draft Draft) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existing := range r.drafts {
		if existing.UserID == draft.UserID && existing.TenantID == draft.TenantID && existing.SessionID == draft.SessionID && existing.Status == StatusPending {
			existing.Status, existing.UpdatedAt = StatusCancelled, draft.CreatedAt
			r.drafts[id] = existing
		}
	}
	r.drafts[draft.ID] = draft
	return nil
}

func (r *MemoryRepository) LatestPending(_ context.Context, owner Owner, sessionID string) (Draft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest Draft
	for _, draft := range r.drafts {
		if draft.UserID == owner.UserID && draft.TenantID == owner.TenantID && draft.SessionID == sessionID && draft.Status == StatusPending && (latest.ID == "" || draft.CreatedAt.After(latest.CreatedAt)) {
			latest = draft
		}
	}
	if latest.ID == "" {
		return Draft{}, ErrNotFound
	}
	return latest, nil
}

func (r *MemoryRepository) Transition(_ context.Context, owner Owner, sessionID, id, contentHash string, status Status, now time.Time) (Draft, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	draft, ok := r.drafts[id]
	if !ok || draft.UserID != owner.UserID || draft.TenantID != owner.TenantID || draft.SessionID != sessionID || draft.ContentHash != contentHash {
		return Draft{}, false, ErrNotFound
	}
	if draft.Status == status {
		return draft, true, nil
	}
	if draft.Status != StatusPending {
		return Draft{}, false, ErrInvalidState
	}
	draft.Status, draft.UpdatedAt = status, now
	r.drafts[id] = draft
	return draft, false, nil
}
