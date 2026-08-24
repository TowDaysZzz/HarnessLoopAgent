package reminder

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type FakeRepository struct {
	mu          sync.Mutex
	reminders   map[string]Reminder
	idempotency map[string]MutationResult
	inputHashes map[string]string
	deliveries  map[string]Delivery
	occurrences map[string]string
}

func NewFakeRepository() *FakeRepository {
	return &FakeRepository{reminders: map[string]Reminder{}, idempotency: map[string]MutationResult{}, inputHashes: map[string]string{}, deliveries: map[string]Delivery{}, occurrences: map[string]string{}}
}

func reminderOwnerKey(owner Owner, value string) string {
	return fmt.Sprintf("%d:%d:%s", owner.TenantID, owner.UserID, value)
}

func (r *FakeRepository) Create(_ context.Context, input CreateInput) (MutationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(input.IdempotencyKey) == "" || !validHash(input.InputHash) || input.Reminder.Owner.Valid() == false {
		return MutationResult{}, ErrInvalidInput
	}
	if err := input.Reminder.Validate(input.Reminder.CreatedAt, DefaultMaxHorizon); err != nil {
		return MutationResult{}, err
	}
	key := reminderOwnerKey(input.Reminder.Owner, input.IdempotencyKey)
	if value, ok := r.idempotency[key]; ok {
		if r.inputHashes[key] != input.InputHash {
			return MutationResult{}, ErrIdempotencyConflict
		}
		value.Replayed = true
		return value, nil
	}
	if _, ok := r.reminders[input.Reminder.ID]; ok {
		return MutationResult{}, ErrStateConflict
	}
	value := cloneReminder(input.Reminder)
	r.reminders[value.ID] = value
	result := MutationResult{Reminder: cloneReminder(value)}
	r.idempotency[key], r.inputHashes[key] = result, input.InputHash
	return result, nil
}

func (r *FakeRepository) Get(_ context.Context, owner Owner, id string) (Reminder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.reminders[id]
	if !ok || value.Owner != owner {
		return Reminder{}, ErrNotFound
	}
	return cloneReminder(value), nil
}

func (r *FakeRepository) List(_ context.Context, query Query) (Page, error) {
	if err := query.Validate(); err != nil {
		return Page{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]Reminder, 0)
	for _, value := range r.reminders {
		if value.Owner != query.Owner || !statusAllowed(query.Statuses, value.Status) || (query.From != nil && value.NextFireAt.Before(*query.From)) || (query.Until != nil && !value.NextFireAt.Before(*query.Until)) || (query.Label != "" && !strings.Contains(strings.ToLower(value.Content), strings.ToLower(query.Label))) {
			continue
		}
		if query.CursorAt != nil && (value.NextFireAt.Before(*query.CursorAt) || (value.NextFireAt.Equal(*query.CursorAt) && value.ID <= query.CursorID)) {
			continue
		}
		values = append(values, cloneReminder(value))
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].NextFireAt.Equal(values[j].NextFireAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].NextFireAt.Before(values[j].NextFireAt)
	})
	page := Page{}
	if len(values) > query.Limit {
		page.Truncated = true
		values = values[:query.Limit]
		last := values[len(values)-1]
		cursor := last.NextFireAt
		page.NextAt, page.NextID = &cursor, last.ID
	}
	page.Items = values
	return page, nil
}

func (r *FakeRepository) Update(_ context.Context, input MutationInput) (MutationResult, error) {
	return r.mutate(input, false)
}

func (r *FakeRepository) Cancel(_ context.Context, input MutationInput) (MutationResult, error) {
	return r.mutate(input, true)
}

func (r *FakeRepository) mutate(input MutationInput, cancel bool) (MutationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !input.Owner.Valid() || input.Target.Validate() != nil || input.IdempotencyKey == "" || !validHash(input.InputHash) {
		return MutationResult{}, ErrInvalidInput
	}
	key := reminderOwnerKey(input.Owner, input.IdempotencyKey)
	if result, ok := r.idempotency[key]; ok {
		if r.inputHashes[key] != input.InputHash {
			return MutationResult{}, ErrIdempotencyConflict
		}
		result.Replayed = true
		return result, nil
	}
	value, ok := r.reminders[input.Target.ID]
	if !ok || value.Owner != input.Owner {
		return MutationResult{}, ErrNotFound
	}
	if value.RowVersion != input.Target.RowVersion || value.Status != StatusScheduled {
		return MutationResult{}, ErrStateConflict
	}
	if cancel {
		value.Status = StatusCancelled
		value.Claim = nil
	} else {
		if normalized, err := NormalizeContent(input.Content); err != nil || normalized != input.Content {
			if err != nil {
				return MutationResult{}, err
			}
			return MutationResult{}, ErrInvalidInput
		}
		hash, err := ComputeContentHash(input.Content, input.Timezone, input.NextFireAt, input.MemoryRefs)
		if err != nil || hash != input.ReplacementHash || !input.NextFireAt.After(input.OccurredAt) {
			return MutationResult{}, ErrInvalidInput
		}
		value.Content, value.Timezone, value.NextFireAt, value.MemoryRefs, value.ContentHash = input.Content, input.Timezone, input.NextFireAt, append([]MemoryRef(nil), input.MemoryRefs...), input.ReplacementHash
	}
	value.RowVersion++
	value.UpdatedAt = input.OccurredAt.UTC()
	r.reminders[value.ID] = value
	result := MutationResult{Reminder: cloneReminder(value)}
	r.idempotency[key], r.inputHashes[key] = result, input.InputHash
	return result, nil
}

func (r *FakeRepository) ClaimDue(_ context.Context, request DueClaimRequest) ([]Reminder, error) {
	if request.Limit < 1 || request.Limit > MaxPageSize || request.Now.IsZero() || request.LeaseDuration <= 0 || request.Token == "" {
		return nil, ErrInvalidInput
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0)
	for id, value := range r.reminders {
		if value.Status == StatusScheduled && !value.NextFireAt.After(request.Now) && (value.Claim == nil || !value.Claim.LeaseUntil.After(request.Now)) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := r.reminders[ids[i]], r.reminders[ids[j]]
		if a.NextFireAt.Equal(b.NextFireAt) {
			return a.ID < b.ID
		}
		return a.NextFireAt.Before(b.NextFireAt)
	})
	if len(ids) > request.Limit {
		ids = ids[:request.Limit]
	}
	out := make([]Reminder, 0, len(ids))
	for _, id := range ids {
		value := r.reminders[id]
		value.Claim = &Claim{Token: request.Token, LeaseUntil: request.Now.Add(request.LeaseDuration)}
		value.RowVersion++
		value.UpdatedAt = request.Now
		r.reminders[id] = value
		out = append(out, cloneReminder(value))
	}
	return out, nil
}

func (r *FakeRepository) RenewClaim(_ context.Context, id string, version uint64, token string, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.reminders[id]
	if !ok {
		return ErrNotFound
	}
	if value.RowVersion != version || value.Claim == nil || value.Claim.Token != token {
		return ErrLeaseLost
	}
	value.Claim.LeaseUntil = until
	r.reminders[id] = value
	return nil
}

func (r *FakeRepository) CommitOccurrence(_ context.Context, input CommitOccurrenceInput) (Delivery, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.reminders[input.ReminderID]
	if !ok {
		return Delivery{}, false, ErrNotFound
	}
	if deliveryID, ok := r.occurrences[input.OccurrenceID]; ok {
		return r.deliveries[deliveryID], true, nil
	}
	if value.RowVersion != input.ExpectedRowVersion || value.Status != StatusScheduled || value.Claim == nil || value.Claim.Token != input.ClaimToken || !value.Claim.LeaseUntil.After(input.OccurredAt) {
		return Delivery{}, false, ErrLeaseLost
	}
	value.Status, value.RowVersion, value.Claim, value.UpdatedAt = StatusProcessing, value.RowVersion+1, nil, input.OccurredAt
	delivery := Delivery{ID: input.OccurrenceID, ReminderID: value.ID, Owner: value.Owner, Content: value.Content, DeliveryKey: input.OccurrenceID, Status: DeliveryPending, AvailableAt: input.OccurredAt}
	r.reminders[value.ID], r.deliveries[delivery.ID], r.occurrences[input.OccurrenceID] = value, delivery, delivery.ID
	return delivery, false, nil
}

func (r *FakeRepository) ClaimDeliveries(_ context.Context, limit int, now time.Time, lease time.Duration, token string) ([]Delivery, error) {
	if limit < 1 || limit > MaxPageSize || now.IsZero() || lease <= 0 || token == "" {
		return nil, ErrInvalidInput
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0)
	for id, value := range r.deliveries {
		if (value.Status == DeliveryPending || value.Status == DeliveryProcessing) && !value.AvailableAt.After(now) && (value.LeaseUntil == nil || !value.LeaseUntil.After(now)) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]Delivery, 0, len(ids))
	for _, id := range ids {
		value := r.deliveries[id]
		until := now.Add(lease)
		value.Status, value.ClaimToken, value.LeaseUntil, value.Attempt = DeliveryProcessing, token, &until, value.Attempt+1
		r.deliveries[id] = value
		out = append(out, value)
	}
	return out, nil
}

func (r *FakeRepository) CompleteDelivery(_ context.Context, id, token string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delivery, ok := r.deliveries[id]
	if !ok {
		return ErrNotFound
	}
	if delivery.Status == DeliveryCompleted {
		return nil
	}
	if delivery.Status != DeliveryProcessing || delivery.ClaimToken != token {
		return ErrLeaseLost
	}
	value := r.reminders[delivery.ReminderID]
	if value.Status != StatusProcessing {
		return ErrStateConflict
	}
	delivery.Status, delivery.ClaimToken, delivery.LeaseUntil = DeliveryCompleted, "", nil
	value.Status, value.RowVersion, value.UpdatedAt = StatusFired, value.RowVersion+1, now
	r.deliveries[id], r.reminders[value.ID] = delivery, value
	return nil
}

func (r *FakeRepository) FailDelivery(_ context.Context, failure DeliveryFailure) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delivery, ok := r.deliveries[failure.ID]
	if !ok {
		return ErrNotFound
	}
	if delivery.Status != DeliveryProcessing || delivery.ClaimToken != failure.ClaimToken {
		return ErrLeaseLost
	}
	delivery.LastErrorCode, delivery.ClaimToken, delivery.LeaseUntil = failure.ErrorCode, "", nil
	value := r.reminders[delivery.ReminderID]
	if failure.Permanent {
		delivery.Status = DeliveryPermanentFailed
		value.Status, value.LastErrorCode, value.RowVersion, value.UpdatedAt = StatusFailed, failure.ErrorCode, value.RowVersion+1, failure.Now
	} else {
		delivery.Status, delivery.AvailableAt = DeliveryPending, failure.NextAvailable
	}
	r.deliveries[delivery.ID], r.reminders[value.ID] = delivery, value
	return nil
}

func statusAllowed(values []Status, target Status) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func cloneReminder(value Reminder) Reminder {
	value.MemoryRefs = append([]MemoryRef(nil), value.MemoryRefs...)
	if value.Claim != nil {
		claim := *value.Claim
		value.Claim = &claim
	}
	return value
}
