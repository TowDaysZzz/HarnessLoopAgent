package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type FakeRepository struct {
	mu          sync.Mutex
	records     map[string]Record
	idempotency map[string]MutationResult
	inputHashes map[string]string
	projections map[string]Projection
}

func NewFakeRepository() *FakeRepository {
	return &FakeRepository{records: map[string]Record{}, idempotency: map[string]MutationResult{}, inputHashes: map[string]string{}, projections: map[string]Projection{}}
}

func ownerKey(owner Owner, key string) string {
	return fmt.Sprintf("%d:%d:%s", owner.TenantID, owner.UserID, key)
}

func (r *FakeRepository) FindExact(_ context.Context, query ExactQuery) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Record
	for _, value := range r.records {
		if value.Owner != query.Owner || value.Scope != query.Scope {
			continue
		}
		match := query.ContentHash != "" && value.ContentHash == query.ContentHash
		match = match || (query.Namespace != "" && query.SlotKey != "" && value.Namespace == query.Namespace && value.SlotKey == query.SlotKey)
		match = match || (!query.Entity.Empty() && value.Entity == query.Entity)
		for _, ref := range query.Refs {
			if value.ID == ref.ID {
				match = true
			}
		}
		if match {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (r *FakeRepository) BatchGet(_ context.Context, owner Owner, ids []string) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		if v, ok := r.records[id]; ok && v.Owner == owner {
			out = append(out, v)
		}
	}
	return out, nil
}

func (r *FakeRepository) CommitMutation(_ context.Context, m Mutation) (MutationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !m.Owner.Valid() || m.NewMemory == nil || m.IdempotencyKey == "" || m.InputHash == "" {
		return MutationResult{}, ErrInvalidInput
	}
	if _, _, err := NormalizeAuditFields(m.Actor, m.ReasonCode); err != nil {
		return MutationResult{}, err
	}
	key := ownerKey(m.Owner, m.IdempotencyKey)
	if existing, ok := r.idempotency[key]; ok {
		if r.inputHashes[key] != m.InputHash {
			return MutationResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	now := m.OccurredAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := m.NewMemory.Validate(now); err != nil {
		return MutationResult{}, err
	}
	if m.NewMemory.Owner != m.Owner {
		return MutationResult{}, ErrNotFound
	}
	if _, exists := r.records[m.NewMemory.ID]; exists {
		return MutationResult{}, ErrStateConflict
	}
	for _, target := range m.Targets {
		old, ok := r.records[target.ID]
		if !ok || old.Owner != m.Owner {
			return MutationResult{}, ErrNotFound
		}
		if old.RowVersion != target.ExpectedRowVersion || !old.Status.CanTransition(target.NewStatus) {
			return MutationResult{}, ErrStateConflict
		}
	}
	for _, old := range r.records {
		if m.NewMemory.Status == StatusActive && m.NewMemory.SlotKey != "" && old.Owner == m.Owner && old.Status == StatusActive && old.Scope == m.NewMemory.Scope && old.Namespace == m.NewMemory.Namespace && old.SlotKey == m.NewMemory.SlotKey {
			found := false
			for _, target := range m.Targets {
				if target.ID == old.ID && target.NewStatus == StatusSuperseded {
					found = true
				}
			}
			if !found {
				return MutationResult{}, ErrStateConflict
			}
		}
	}
	for _, target := range m.Targets {
		old := r.records[target.ID]
		old.Status, old.RowVersion, old.UpdatedAt = target.NewStatus, old.RowVersion+1, now
		if target.NewStatus == StatusSuperseded {
			old.SupersededBy = m.NewMemory.ID
		}
		r.records[old.ID] = old
	}
	value := *m.NewMemory
	value.CreatedAt, value.UpdatedAt = now, now
	if value.Status == StatusActive {
		projectionID := ownerKey(m.Owner, value.ID+":"+value.ContentHash)
		r.projections[projectionID] = Projection{ID: projectionID, Owner: m.Owner, MemoryID: value.ID, ContentHash: value.ContentHash, Status: ProjectionPending, AvailableAt: now}
	}
	r.records[value.ID] = value
	result := MutationResult{Memory: value, Relations: append([]Relation(nil), m.Relations...)}
	r.idempotency[key], r.inputHashes[key] = result, m.InputHash
	return result, nil
}

func (r *FakeRepository) TransitionMemory(ctx context.Context, owner Owner, id string, version uint64, to Status, actor, reason, key, inputHash string, now time.Time) (MutationResult, error) {
	_ = ctx
	if _, _, err := NormalizeAuditFields(actor, reason); err != nil {
		return MutationResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	idem := ownerKey(owner, key)
	if old, exists := r.idempotency[idem]; exists {
		if r.inputHashes[idem] != inputHash {
			return MutationResult{}, ErrIdempotencyConflict
		}
		old.Replayed = true
		return old, nil
	}
	value, ok := r.records[id]
	if !ok || value.Owner != owner {
		return MutationResult{}, ErrNotFound
	}
	if value.RowVersion != version || !value.Status.CanTransition(to) {
		return MutationResult{}, ErrStateConflict
	}
	value.Status, value.RowVersion, value.UpdatedAt = to, value.RowVersion+1, now
	r.records[id] = value
	if to == StatusActive {
		projectionID := ownerKey(owner, value.ID+":"+value.ContentHash)
		r.projections[projectionID] = Projection{ID: projectionID, Owner: owner, MemoryID: value.ID, ContentHash: value.ContentHash, Status: ProjectionPending, AvailableAt: now}
	}
	res := MutationResult{Memory: value}
	r.idempotency[idem], r.inputHashes[idem] = res, inputHash
	return res, nil
}

func (r *FakeRepository) Expire(_ context.Context, owner Owner, now time.Time, limit int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for id, value := range r.records {
		if count == limit && limit > 0 {
			break
		}
		if value.Owner == owner && (value.Status == StatusActive || value.Status == StatusCandidate) && value.ExpiresAt != nil && !now.Before(*value.ExpiresAt) {
			value.Status, value.RowVersion, value.UpdatedAt = StatusExpired, value.RowVersion+1, now
			r.records[id] = value
			count++
		}
	}
	return count, nil
}

func (r *FakeRepository) ClaimProjections(_ context.Context, limit int, now time.Time) ([]Projection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Projection
	for id, p := range r.projections {
		if len(out) == limit {
			break
		}
		if (p.Status == ProjectionPending || p.Status == ProjectionFailed) && !p.AvailableAt.After(now) {
			p.Status = ProjectionProcessing
			p.Attempt++
			p.ClaimedAt = &now
			r.projections[id] = p
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *FakeRepository) CompleteProjection(_ context.Context, owner Owner, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projections[id]
	if !ok || p.Owner != owner {
		return ErrNotFound
	}
	p.Status, p.ProcessedAt, p.LastErrorCode = ProjectionCompleted, &now, ""
	r.projections[id] = p
	return nil
}

func (r *FakeRepository) FailProjection(_ context.Context, owner Owner, id, code string, next time.Time, permanent bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projections[id]
	if !ok || p.Owner != owner {
		return ErrNotFound
	}
	p.Status, p.AvailableAt, p.LastErrorCode = ProjectionFailed, next, boundedReason(code)
	if permanent {
		p.Status = ProjectionPermanentFailed
	}
	r.projections[id] = p
	return nil
}
