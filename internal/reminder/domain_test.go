package reminder

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func testReminder(t *testing.T, id string, fireAt time.Time) Reminder {
	t.Helper()
	ref := MemoryRef{ID: "memory-1", LineageVersion: 1, ContentHash: strings.Repeat("a", 64)}
	hash, err := ComputeContentHash("提交周报", DefaultTimezone, fireAt, []MemoryRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	now := fireAt.Add(-time.Hour)
	return Reminder{ID: id, Owner: Owner{TenantID: 9, UserID: 7}, Content: "提交周报", ContentHash: hash, Timezone: DefaultTimezone, NextFireAt: fireAt, Status: StatusScheduled, RowVersion: 1, MemoryRefs: []MemoryRef{ref}, Source: SourceRef{Type: "user_message", ID: "message-1"}, CreatedAt: now, UpdatedAt: now}
}

func TestReminderValidationAndStateMachine(t *testing.T) {
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	value := testReminder(t, "reminder-1", now.Add(time.Hour))
	if err := value.Validate(now, 24*time.Hour); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if !StatusScheduled.CanTransition(StatusProcessing) || StatusScheduled.CanTransition(StatusFired) || !StatusProcessing.CanTransition(StatusFired) || StatusFired.CanTransition(StatusScheduled) {
		t.Fatal("invalid transition contract")
	}
	past := testReminder(t, "past", now.Add(-time.Minute))
	if err := past.Validate(now, 24*time.Hour); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("past Validate() = %v", err)
	}
	secret := value
	secret.Content = "Authorization: Bearer abcdefghijklmnop"
	if _, err := NormalizeContent(secret.Content); !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("secret = %v", err)
	}
}

func TestFakeRepositoryContractAndQueryBoundaries(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	repo := NewFakeRepository()
	first := testReminder(t, "r-1", now.Add(time.Hour))
	input := CreateInput{Reminder: first, IdempotencyKey: "create-1", InputHash: strings.Repeat("b", 64)}
	created, err := repo.Create(ctx, input)
	if err != nil || created.Replayed {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
	replay, err := repo.Create(ctx, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	changed := input
	changed.InputHash = strings.Repeat("c", 64)
	if _, err := repo.Create(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict = %v", err)
	}
	second := testReminder(t, "r-2", now.Add(2*time.Hour))
	second.Content = "购买牛奶"
	second.ContentHash, _ = ComputeContentHash(second.Content, second.Timezone, second.NextFireAt, second.MemoryRefs)
	_, _ = repo.Create(ctx, CreateInput{Reminder: second, IdempotencyKey: "create-2", InputHash: strings.Repeat("d", 64)})
	page, err := repo.List(ctx, Query{Owner: first.Owner, Statuses: []Status{StatusScheduled}, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "r-1" || !page.Truncated || page.NextAt == nil {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	if _, err := repo.List(ctx, Query{Owner: first.Owner}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unbounded query = %v", err)
	}
	updateHash, _ := ComputeContentHash("提交精简周报", DefaultTimezone, now.Add(3*time.Hour), first.MemoryRefs)
	updated, err := repo.Update(ctx, MutationInput{Owner: first.Owner, Target: ReminderRef{ID: first.ID, RowVersion: 1}, Content: "提交精简周报", Timezone: DefaultTimezone, NextFireAt: now.Add(3 * time.Hour), MemoryRefs: first.MemoryRefs, ReplacementHash: updateHash, IdempotencyKey: "update-1", InputHash: strings.Repeat("e", 64), OccurredAt: now})
	if err != nil || updated.Reminder.RowVersion != 2 {
		t.Fatalf("Update() = %#v, %v", updated, err)
	}
	if _, err := repo.Cancel(ctx, MutationInput{Owner: first.Owner, Target: ReminderRef{ID: first.ID, RowVersion: 1}, IdempotencyKey: "cancel-stale", InputHash: strings.Repeat("f", 64), OccurredAt: now}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale cancel = %v", err)
	}
}

func TestFakeRepositoryClaimOccurrenceAndDelivery(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	repo := NewFakeRepository()
	value := testReminder(t, "due", now.Add(time.Minute))
	_, _ = repo.Create(ctx, CreateInput{Reminder: value, IdempotencyKey: "create", InputHash: strings.Repeat("1", 64)})
	if due, _ := repo.ClaimDue(ctx, DueClaimRequest{Limit: 1, Now: now, LeaseDuration: time.Minute, Token: "early"}); len(due) != 0 {
		t.Fatal("claimed reminder before due")
	}
	dueAt := now.Add(time.Minute)
	due, err := repo.ClaimDue(ctx, DueClaimRequest{Limit: 1, Now: dueAt, LeaseDuration: time.Minute, Token: "claim"})
	if err != nil || len(due) != 1 {
		t.Fatalf("ClaimDue() = %#v, %v", due, err)
	}
	delivery, replayed, err := repo.CommitOccurrence(ctx, CommitOccurrenceInput{ReminderID: value.ID, ExpectedRowVersion: due[0].RowVersion, ClaimToken: "claim", OccurrenceID: "occurrence-1", OccurredAt: dueAt})
	if err != nil || replayed || delivery.DeliveryKey != "occurrence-1" {
		t.Fatalf("CommitOccurrence() = %#v %v %v", delivery, replayed, err)
	}
	replay, replayed, err := repo.CommitOccurrence(ctx, CommitOccurrenceInput{ReminderID: value.ID, OccurrenceID: "occurrence-1", OccurredAt: dueAt})
	if err != nil || !replayed || replay.ID != delivery.ID {
		t.Fatalf("occurrence replay = %#v %v %v", replay, replayed, err)
	}
	claimed, err := repo.ClaimDeliveries(ctx, 1, dueAt, time.Minute, "worker")
	if err != nil || len(claimed) != 1 || claimed[0].Attempt != 1 {
		t.Fatalf("ClaimDeliveries() = %#v, %v", claimed, err)
	}
	if err := repo.CompleteDelivery(ctx, delivery.ID, "worker", dueAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.Get(ctx, value.Owner, value.ID)
	if stored.Status != StatusFired || stored.Status.CanTransition(StatusScheduled) {
		t.Fatalf("stored = %#v", stored)
	}
}
