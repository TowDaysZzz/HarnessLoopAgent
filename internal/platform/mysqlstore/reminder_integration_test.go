package mysqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

func integrationReminder(t *testing.T, owner reminder.Owner, fireAt time.Time) reminder.Reminder {
	t.Helper()
	content := "提交周报 " + uuid.NewString()
	hash, err := reminder.ComputeContentHash(content, reminder.DefaultTimezone, fireAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := fireAt.Add(-time.Hour)
	return reminder.Reminder{ID: uuid.NewString(), Owner: owner, Content: content, ContentHash: hash, Timezone: reminder.DefaultTimezone, NextFireAt: fireAt, Status: reminder.StatusScheduled, RowVersion: 1, Source: reminder.SourceRef{Type: "user_message", ID: uuid.NewString()}, CreatedAt: now, UpdatedAt: now}
}

func TestReminderMigrationIndexesAndRepositoryLifecycle(t *testing.T) {
	store, ctx := openMemoryStore(t)
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, check := range []struct{ table, index string }{{"reminders", "idx_reminders_due_claim"}, {"reminder_events", "uk_reminder_event_idempotency"}, {"reminder_delivery_outbox", "uk_reminder_occurrence"}} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND INDEX_NAME=?`, check.table, check.index).Scan(&count); err != nil || count == 0 {
			t.Fatalf("index %s.%s count=%d err=%v", check.table, check.index, count, err)
		}
	}
	owner := reminder.Owner{TenantID: 8601, UserID: uint64(time.Now().UnixNano()%500000000) + 8000000000}
	now := time.Now().UTC().Truncate(time.Microsecond)
	value := integrationReminder(t, owner, now.Add(time.Hour))
	input := reminder.CreateInput{Reminder: value, IdempotencyKey: "create:" + uuid.NewString(), InputHash: strings.Repeat("a", 64), Actor: "user", ReasonCode: "approved"}
	created, err := store.Create(ctx, input)
	if err != nil || created.Reminder.ID != value.ID {
		t.Fatalf("Create=%+v err=%v", created, err)
	}
	replayed, err := store.Create(ctx, input)
	if err != nil || !replayed.Replayed {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := input
	changed.InputHash = strings.Repeat("b", 64)
	if _, err := store.Create(ctx, changed); !errors.Is(err, reminder.ErrIdempotencyConflict) {
		t.Fatalf("idempotency=%v", err)
	}
	if _, err := store.Get(ctx, reminder.Owner{TenantID: owner.TenantID, UserID: owner.UserID + 1}, value.ID); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatalf("cross owner=%v", err)
	}
	page, err := store.List(ctx, reminder.Query{Owner: owner, Statuses: []reminder.Status{reminder.StatusScheduled}, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("List=%+v err=%v", page, err)
	}
	newTime := now.Add(2 * time.Hour)
	newContent := "提交精简周报"
	newHash, _ := reminder.ComputeContentHash(newContent, reminder.DefaultTimezone, newTime, nil)
	updated, err := store.Update(ctx, reminder.MutationInput{Owner: owner, Target: reminder.ReminderRef{ID: value.ID, RowVersion: 1}, Content: newContent, Timezone: reminder.DefaultTimezone, NextFireAt: newTime, ReplacementHash: newHash, IdempotencyKey: "update:" + uuid.NewString(), InputHash: strings.Repeat("c", 64), Actor: "user", ReasonCode: "approved", OccurredAt: now})
	if err != nil || updated.Reminder.RowVersion != 2 {
		t.Fatalf("Update=%+v err=%v", updated, err)
	}
	if _, err := store.Cancel(ctx, reminder.MutationInput{Owner: owner, Target: reminder.ReminderRef{ID: value.ID, RowVersion: 1}, IdempotencyKey: "stale:" + uuid.NewString(), InputHash: strings.Repeat("d", 64), OccurredAt: now}); !errors.Is(err, reminder.ErrStateConflict) {
		t.Fatalf("stale=%v", err)
	}
	cancelled, err := store.Cancel(ctx, reminder.MutationInput{Owner: owner, Target: reminder.ReminderRef{ID: value.ID, RowVersion: 2}, IdempotencyKey: "cancel:" + uuid.NewString(), InputHash: strings.Repeat("e", 64), Actor: "user", ReasonCode: "cancelled", OccurredAt: now})
	if err != nil || cancelled.Reminder.Status != reminder.StatusCancelled {
		t.Fatalf("Cancel=%+v err=%v", cancelled, err)
	}
}

func TestReminderDueClaimOccurrenceRollbackAndDelivery(t *testing.T) {
	store, _ := openMemoryStore(t)
	owner := reminder.Owner{TenantID: 8701, UserID: uint64(time.Now().UnixNano()%500000000) + 8500000000}
	now := time.Now().UTC().Truncate(time.Microsecond)
	value := integrationReminder(t, owner, now.Add(-time.Second))
	if _, err := store.Create(context.Background(), reminder.CreateInput{Reminder: value, IdempotencyKey: uuid.NewString(), InputHash: strings.Repeat("1", 64)}); err != nil {
		t.Fatal(err)
	}
	// The MySQL adapter must use database time. A stale application timestamp
	// must not prevent an already-due reminder from being claimed.
	dueAt := now.Add(-time.Hour)
	type claimResult struct {
		values []reminder.Reminder
		err    error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			values, err := store.ClaimDue(context.Background(), reminder.DueClaimRequest{Limit: 1, Now: dueAt, LeaseDuration: time.Minute, Token: "claim-" + string(rune('a'+i))})
			results <- claimResult{values, err}
		}(i)
	}
	wg.Wait()
	close(results)
	var claimed reminder.Reminder
	count := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		count += len(result.values)
		if len(result.values) == 1 {
			claimed = result.values[0]
		}
	}
	if count != 1 {
		t.Fatalf("claim count=%d", count)
	}
	_, _, err := store.CommitOccurrence(context.Background(), reminder.CommitOccurrenceInput{ReminderID: value.ID, ExpectedRowVersion: claimed.RowVersion, ClaimToken: claimed.Claim.Token, OccurrenceID: strings.Repeat("x", 300), OccurredAt: dueAt})
	if err == nil {
		t.Fatal("expected oversized occurrence rollback")
	}
	stillScheduled, err := store.Get(context.Background(), owner, value.ID)
	if err != nil || stillScheduled.Status != reminder.StatusScheduled || stillScheduled.Claim == nil {
		t.Fatalf("rollback=%+v err=%v", stillScheduled, err)
	}
	delivery, replay, err := store.CommitOccurrence(context.Background(), reminder.CommitOccurrenceInput{ReminderID: value.ID, ExpectedRowVersion: claimed.RowVersion, ClaimToken: claimed.Claim.Token, OccurrenceID: "occurrence-" + uuid.NewString(), OccurredAt: dueAt})
	if err != nil || replay {
		t.Fatalf("CommitOccurrence=%+v replay=%v err=%v", delivery, replay, err)
	}
	replayed, replay, err := store.CommitOccurrence(context.Background(), reminder.CommitOccurrenceInput{ReminderID: value.ID, OccurrenceID: delivery.ID, OccurredAt: dueAt})
	if err != nil || !replay || replayed.ID != delivery.ID {
		t.Fatalf("replay=%+v %v %v", replayed, replay, err)
	}
	claimedDelivery, err := store.ClaimDeliveries(context.Background(), 1, dueAt, time.Minute, "worker-1")
	if err != nil || len(claimedDelivery) != 1 {
		t.Fatalf("ClaimDeliveries=%+v err=%v", claimedDelivery, err)
	}
	if err := store.FailDelivery(context.Background(), reminder.DeliveryFailure{ID: delivery.ID, ClaimToken: "worker-1", ErrorCode: "temporary", Now: dueAt, NextAvailable: dueAt.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if early, _ := store.ClaimDeliveries(context.Background(), 1, dueAt.Add(time.Second), time.Minute, "early"); len(early) != 0 {
		t.Fatal("retried before available_at")
	}
	retry, err := store.ClaimDeliveries(context.Background(), 1, dueAt.Add(time.Minute), time.Minute, "worker-2")
	if err != nil || len(retry) != 1 || retry[0].DeliveryKey != delivery.DeliveryKey {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	if err := store.CompleteDelivery(context.Background(), delivery.ID, "worker-2", dueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	fired, _ := store.Get(context.Background(), owner, value.ID)
	if fired.Status != reminder.StatusFired {
		t.Fatalf("fired=%+v", fired)
	}
}
