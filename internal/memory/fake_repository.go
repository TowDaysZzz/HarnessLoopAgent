package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type FakeRepository struct {
	mu                sync.Mutex
	records           map[string]Record
	idempotency       map[string]MutationResult
	inputHashes       map[string]string
	projections       map[string]Projection
	projectionVersion string
	mutationVersions  map[string]uint64
}

func NewFakeRepository() *FakeRepository {
	return NewFakeRepositoryWithProjectionVersion("v1")
}

func NewFakeRepositoryWithProjectionVersion(version string) *FakeRepository {
	return &FakeRepository{records: map[string]Record{}, idempotency: map[string]MutationResult{}, inputHashes: map[string]string{}, projections: map[string]Projection{}, projectionVersion: version, mutationVersions: map[string]uint64{}}
}

func ownerKey(owner Owner, key string) string {
	return fmt.Sprintf("%d:%d:%s", owner.TenantID, owner.UserID, key)
}

func (r *FakeRepository) FindExact(_ context.Context, query ExactQuery) ([]Record, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if !query.HasSelector() {
		return []Record{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Record
	for _, value := range r.records {
		if value.Owner != query.Owner || value.Scope != query.Scope {
			continue
		}
		if query.ActiveAt != nil && !value.IsActiveAt(*query.ActiveAt) {
			continue
		}
		if len(query.Layers) > 0 && !containsLayer(query.Layers, value.Layer) {
			continue
		}
		if len(query.Kinds) > 0 && !containsKind(query.Kinds, value.Kind) {
			continue
		}
		match := query.ScopeOnly
		match = match || (query.Namespace != "" && query.SlotKey != "" && value.Namespace == query.Namespace && value.SlotKey == query.SlotKey)
		match = match || (!query.Entity.Empty() && value.Entity == query.Entity)
		for _, hash := range query.ContentHashes {
			match = match || value.ContentHash == hash
		}
		for _, slot := range query.Slots {
			match = match || (value.Namespace == slot.Namespace && value.SlotKey == slot.SlotKey)
		}
		for _, entity := range query.Entities {
			match = match || value.Entity == entity
		}
		for _, ref := range query.Refs {
			if value.ID == ref.ID && value.LineageVersion == ref.LineageVersion && value.ContentHash == ref.ContentHash {
				match = true
			}
		}
		if match {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func containsLayer(values []Layer, target Layer) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func containsKind(values []Kind, target Kind) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
		projectionID := ownerKey(m.Owner, value.ID+":"+value.ContentHash+":"+r.projectionVersion)
		r.projections[projectionID] = Projection{ID: projectionID, Owner: m.Owner, MemoryID: value.ID, ContentHash: value.ContentHash, ModelVersion: r.projectionVersion, Status: ProjectionPending, AvailableAt: now}
	}
	r.records[value.ID] = value
	result := MutationResult{Memory: value, Relations: append([]Relation(nil), m.Relations...)}
	r.idempotency[key], r.inputHashes[key] = result, m.InputHash
	r.mutationVersions[ownerKey(m.Owner, "version")]++
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
		projectionID := ownerKey(owner, value.ID+":"+value.ContentHash+":"+r.projectionVersion)
		r.projections[projectionID] = Projection{ID: projectionID, Owner: owner, MemoryID: value.ID, ContentHash: value.ContentHash, ModelVersion: r.projectionVersion, Status: ProjectionPending, AvailableAt: now}
	}
	res := MutationResult{Memory: value}
	r.idempotency[idem], r.inputHashes[idem] = res, inputHash
	r.mutationVersions[ownerKey(owner, "version")]++
	return res, nil
}

func (r *FakeRepository) ActivateCandidateSuperseding(_ context.Context, activation CandidateActivation) (MutationResult, error) {
	if !activation.Owner.Valid() || activation.CandidateID == "" || activation.SupersededID == "" || activation.CandidateID == activation.SupersededID || activation.CandidateVersion == 0 || activation.TargetVersion == 0 || activation.IdempotencyKey == "" || activation.InputHash == "" {
		return MutationResult{}, ErrInvalidInput
	}
	if _, _, err := NormalizeAuditFields(activation.Actor, activation.ReasonCode); err != nil {
		return MutationResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	idem := ownerKey(activation.Owner, activation.IdempotencyKey)
	if existing, ok := r.idempotency[idem]; ok {
		if r.inputHashes[idem] != activation.InputHash {
			return MutationResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	candidate, candidateOK := r.records[activation.CandidateID]
	target, targetOK := r.records[activation.SupersededID]
	if !candidateOK || !targetOK || candidate.Owner != activation.Owner || target.Owner != activation.Owner {
		return MutationResult{}, ErrNotFound
	}
	if candidate.RowVersion != activation.CandidateVersion || target.RowVersion != activation.TargetVersion || candidate.Status != StatusCandidate || target.Status != StatusActive || !candidate.Status.CanTransition(StatusActive) || !target.Status.CanTransition(StatusSuperseded) || candidate.SupersedesID != target.ID || candidate.LineageID != target.LineageID || candidate.LineageVersion <= target.LineageVersion {
		return MutationResult{}, ErrStateConflict
	}
	now := activation.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	target.Status, target.SupersededBy, target.RowVersion, target.UpdatedAt = StatusSuperseded, candidate.ID, target.RowVersion+1, now
	candidate.Status, candidate.RowVersion, candidate.UpdatedAt = StatusActive, candidate.RowVersion+1, now
	r.records[target.ID], r.records[candidate.ID] = target, candidate
	projectionID := ownerKey(activation.Owner, candidate.ID+":"+candidate.ContentHash+":"+r.projectionVersion)
	r.projections[projectionID] = Projection{ID: projectionID, Owner: activation.Owner, MemoryID: candidate.ID, ContentHash: candidate.ContentHash, ModelVersion: r.projectionVersion, Status: ProjectionPending, AvailableAt: now}
	relations := []Relation{{FromID: candidate.ID, ToID: target.ID, Type: RelationSupersedes, ReasonCode: activation.ReasonCode}}
	result := MutationResult{Memory: candidate, Relations: relations}
	r.idempotency[idem], r.inputHashes[idem] = result, activation.InputHash
	r.mutationVersions[ownerKey(activation.Owner, "version")]++
	return result, nil
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
	if count > 0 {
		r.mutationVersions[ownerKey(owner, "version")]++
	}
	return count, nil
}

func (r *FakeRepository) MutationVersion(_ context.Context, owner Owner) (uint64, error) {
	if !owner.Valid() {
		return 0, ErrInvalidInput
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutationVersions[ownerKey(owner, "version")], nil
}

func (r *FakeRepository) ListActiveContextRefs(_ context.Context, owner Owner, kinds []Kind, now time.Time, limit int) ([]MemoryRef, error) {
	if !owner.Valid() || limit < 1 || limit > 100 || len(kinds) == 0 {
		return nil, ErrInvalidInput
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var values []Record
	for _, value := range r.records {
		if value.Owner == owner && value.IsActiveAt(now) && containsKind(kinds, value.Kind) {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Salience != values[j].Salience {
			return values[i].Salience > values[j].Salience
		}
		if !values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
			return values[i].UpdatedAt.After(values[j].UpdatedAt)
		}
		return values[i].ID < values[j].ID
	})
	if len(values) > limit {
		values = values[:limit]
	}
	refs := make([]MemoryRef, 0, len(values))
	for _, value := range values {
		refs = append(refs, value.Ref())
	}
	return refs, nil
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

func (r *FakeRepository) PendingProjectionCount(_ context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for _, projection := range r.projections {
		if projection.Status == ProjectionPending || projection.Status == ProjectionFailed {
			count++
		}
	}
	return count, nil
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
