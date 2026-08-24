package reminderdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
)

func TestDispatcherDueBoundaryStableBatchLeaseRecoveryAndCompetition(t *testing.T) {
	clock := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := reminder.NewFakeRepository()
	owner := reminder.Owner{TenantID: 1, UserID: 2}
	seedReminder(t, repo, owner, "b", "第二条", clock.Add(time.Hour), clock)
	seedReminder(t, repo, owner, "a", "第一条", clock.Add(time.Hour), clock)
	var sequence atomic.Uint64
	newToken := func() string { return fmt.Sprintf("dispatcher-%d", sequence.Add(1)) }
	dispatcher, err := NewDispatcher(repo, DispatcherConfig{BatchSize: 1, MaxBatches: 4, LeaseDuration: time.Second, Interval: time.Millisecond, Now: func() time.Time { return clock }, NewToken: newToken})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := dispatcher.Tick(context.Background()); err != nil || count != 0 {
		t.Fatalf("early count=%d err=%v", count, err)
	}
	clock = clock.Add(time.Hour)
	if count, err := dispatcher.Tick(context.Background()); err != nil || count != 2 {
		t.Fatalf("due count=%d err=%v", count, err)
	}
	first, _ := repo.Get(context.Background(), owner, "a")
	second, _ := repo.Get(context.Background(), owner, "b")
	if first.Status != reminder.StatusProcessing || second.Status != reminder.StatusProcessing {
		t.Fatalf("statuses=%s,%s", first.Status, second.Status)
	}
	if count, err := dispatcher.Tick(context.Background()); err != nil || count != 0 {
		t.Fatalf("competition count=%d err=%v", count, err)
	}

	seedReminder(t, repo, owner, "lease", "租约恢复", clock, clock.Add(-time.Second))
	claimed, err := repo.ClaimDue(context.Background(), reminder.DueClaimRequest{Limit: 1, Now: clock, LeaseDuration: time.Second, Token: "dead-instance"})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%d err=%v", len(claimed), err)
	}
	if count, err := dispatcher.Tick(context.Background()); err != nil || count != 0 {
		t.Fatalf("live lease count=%d err=%v", count, err)
	}
	clock = clock.Add(2 * time.Second)
	if count, err := dispatcher.Tick(context.Background()); err != nil || count != 1 {
		t.Fatalf("recovered count=%d err=%v", count, err)
	}
}

type scriptedAdapter struct {
	idempotent bool
	errors     []error
	calls      int
}

func (a *scriptedAdapter) SupportsIdempotency() bool { return a.idempotent }
func (a *scriptedAdapter) Deliver(_ context.Context, request DeliveryRequest) (DeliveryResult, error) {
	a.calls++
	if len(a.errors) > 0 {
		err := a.errors[0]
		a.errors = a.errors[1:]
		return DeliveryResult{}, err
	}
	return DeliveryResult{ProviderID: "ok:" + request.Key}, nil
}

func TestWorkerIdempotentCrashRetryTemporaryPermanentAndProductionGuard(t *testing.T) {
	clock := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if _, err := NewWorker(reminder.NewFakeRepository(), &scriptedAdapter{idempotent: false}, WorkerConfig{BatchSize: 1, MaxBatches: 1, MaxAttempts: 2, LeaseDuration: time.Second, Interval: time.Second, BaseBackoff: time.Second, MaxBackoff: time.Minute, Production: true, NewToken: func() string { return "x" }}); !errors.Is(err, reminder.ErrInvalidInput) {
		t.Fatalf("production err=%v", err)
	}

	// An external success followed by a local crash is retried with the same key;
	// the recording adapter returns the original result without a second send.
	repo := reminder.NewFakeRepository()
	owner := reminder.Owner{TenantID: 3, UserID: 4}
	seedReminder(t, repo, owner, "crash", "崩溃重放", clock, clock.Add(-time.Second))
	commitDue(t, repo, clock, "dispatch-crash")
	adapter := NewRecordingAdapter()
	failOnce := true
	worker, _ := NewWorker(repo, adapter, WorkerConfig{BatchSize: 10, MaxBatches: 1, MaxAttempts: 3, LeaseDuration: time.Second, Interval: time.Second, BaseBackoff: time.Second, MaxBackoff: time.Minute, Now: func() time.Time { return clock }, NewToken: func() string { return "worker-crash" }, BeforeComplete: func(reminder.Delivery) error {
		if failOnce {
			failOnce = false
			return &DeliveryError{Code: "local_commit_crash", Retryable: true}
		}
		return nil
	}})
	if _, err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if count, err := worker.Tick(context.Background()); err != nil || count != 1 || adapter.Calls != 1 {
		t.Fatalf("replay count=%d calls=%d err=%v", count, adapter.Calls, err)
	}
	value, _ := repo.Get(context.Background(), owner, "crash")
	if value.Status != reminder.StatusFired {
		t.Fatalf("status=%s", value.Status)
	}

	// Temporary failure retries, while a non-retryable error is terminal.
	for _, tc := range []struct {
		name        string
		deliveryErr error
		want        reminder.Status
	}{{"temporary", &DeliveryError{Code: "timeout", Retryable: true}, reminder.StatusProcessing}, {"permanent", &DeliveryError{Code: "rejected", Retryable: false}, reminder.StatusFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			localClock := clock
			localRepo := reminder.NewFakeRepository()
			seedReminder(t, localRepo, owner, tc.name, tc.name, localClock, localClock.Add(-time.Second))
			commitDue(t, localRepo, localClock, "dispatch-"+tc.name)
			scripted := &scriptedAdapter{idempotent: true, errors: []error{tc.deliveryErr}}
			localWorker, _ := NewWorker(localRepo, scripted, WorkerConfig{BatchSize: 1, MaxBatches: 1, MaxAttempts: 3, LeaseDuration: time.Second, Interval: time.Second, BaseBackoff: time.Second, MaxBackoff: time.Minute, Now: func() time.Time { return localClock }, NewToken: func() string { return "worker-" + tc.name }})
			if _, err := localWorker.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, _ := localRepo.Get(context.Background(), owner, tc.name)
			if got.Status != tc.want {
				t.Fatalf("status=%s want=%s", got.Status, tc.want)
			}
		})
	}
}

func TestObservabilityContainsOnlyStableMetadata(t *testing.T) {
	clock := time.Now().UTC()
	repo := reminder.NewFakeRepository()
	owner := reminder.Owner{TenantID: 5, UserID: 6}
	secret := "正文 should-not-log"
	seedReminder(t, repo, owner, "observed", secret, clock, clock.Add(-time.Second))
	observer := &RecordingObserver{}
	dispatcher, _ := NewDispatcher(repo, DispatcherConfig{BatchSize: 1, MaxBatches: 1, LeaseDuration: time.Second, Interval: time.Second, Now: func() time.Time { return clock }, NewToken: func() string { return "observed-token" }, Observer: observer})
	_, _ = dispatcher.Tick(context.Background())
	raw, _ := json.Marshal(observer.Values)
	encoded := strings.ToLower(string(raw))
	for _, forbidden := range []string{"should-not-log", "password=", "authorization", "cookie", "bearer "} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("observation leaked %q: %s", forbidden, raw)
		}
	}
}

func TestRuntimeGracefulStopMaxAttemptsAndProcessingCancelConflict(t *testing.T) {
	clock := time.Now().UTC()
	repo := reminder.NewFakeRepository()
	owner := reminder.Owner{TenantID: 8, UserID: 9}
	seedReminder(t, repo, owner, "max-attempt", "最大尝试", clock, clock.Add(-time.Second))
	commitDue(t, repo, clock, "dispatch-max")
	processing, _ := repo.Get(context.Background(), owner, "max-attempt")
	if _, err := repo.Cancel(context.Background(), reminder.MutationInput{Owner: owner, Target: reminder.ReminderRef{ID: processing.ID, RowVersion: processing.RowVersion}, IdempotencyKey: "cancel-processing", InputHash: strings.Repeat("a", 64), Actor: "user", ReasonCode: "cancel", OccurredAt: clock}); !errors.Is(err, reminder.ErrStateConflict) {
		t.Fatalf("processing cancel err=%v", err)
	}
	adapter := &scriptedAdapter{idempotent: true, errors: []error{&DeliveryError{Code: "timeout", Retryable: true}}}
	worker, _ := NewWorker(repo, adapter, WorkerConfig{BatchSize: 1, MaxBatches: 1, MaxAttempts: 1, LeaseDuration: time.Second, Interval: time.Millisecond, BaseBackoff: time.Second, MaxBackoff: time.Minute, Now: func() time.Time { return clock }, NewToken: func() string { return "worker-max" }})
	if _, err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed, _ := repo.Get(context.Background(), owner, "max-attempt")
	if failed.Status != reminder.StatusFailed {
		t.Fatalf("max attempt status=%s", failed.Status)
	}

	dispatcher, _ := NewDispatcher(repo, DispatcherConfig{BatchSize: 1, MaxBatches: 1, LeaseDuration: time.Second, Interval: time.Millisecond, Now: func() time.Time { return clock }, NewToken: func() string { return "stop-dispatcher" }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dispatcher.Run(ctx); err != nil {
		t.Fatalf("dispatcher stop err=%v", err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("worker stop err=%v", err)
	}
}

func seedReminder(t *testing.T, repo reminder.Repository, owner reminder.Owner, id, content string, fireAt, now time.Time) {
	t.Helper()
	hash, err := reminder.ComputeContentHash(content, reminder.DefaultTimezone, fireAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Create(context.Background(), reminder.CreateInput{Reminder: reminder.Reminder{ID: id, Owner: owner, Content: content, ContentHash: hash, Timezone: reminder.DefaultTimezone, NextFireAt: fireAt, Status: reminder.StatusScheduled, RowVersion: 1, Source: reminder.SourceRef{Type: "test", ID: id}, CreatedAt: now, UpdatedAt: now}, IdempotencyKey: "seed-" + id, InputHash: hash, Actor: "test", ReasonCode: "seed"})
	if err != nil {
		t.Fatal(err)
	}
}
func commitDue(t *testing.T, repo reminder.Repository, now time.Time, token string) reminder.Delivery {
	t.Helper()
	values, err := repo.ClaimDue(context.Background(), reminder.DueClaimRequest{Limit: 10, Now: now, LeaseDuration: time.Second, Token: token})
	if err != nil || len(values) != 1 {
		t.Fatalf("claim=%d err=%v", len(values), err)
	}
	value := values[0]
	delivery, _, err := repo.CommitOccurrence(context.Background(), reminder.CommitOccurrenceInput{ReminderID: value.ID, ExpectedRowVersion: value.RowVersion, ClaimToken: token, OccurrenceID: value.ID + ":occurrence", OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}
