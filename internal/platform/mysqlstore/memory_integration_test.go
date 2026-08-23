package mysqlstore_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
)

func openMemoryStore(t *testing.T) (*mysqlstore.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run managed memory integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 12, MaxIdleConns: 4, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func integrationMemory(t *testing.T, owner memory.Owner, status memory.Status, slot string, now time.Time) memory.Record {
	t.Helper()
	text, structured, hash, err := memory.NormalizeContent("remind me tomorrow", memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"value": uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	return memory.Record{ID: uuid.NewString(), Owner: owner, Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "integration", SlotKey: slot, LineageID: uuid.NewString(), LineageVersion: 1, RowVersion: 1, CanonicalText: text, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserConfirmed, Confidence: 1, Salience: .5, Source: memory.SourceRef{Type: "workflow", ID: "integration"}, Status: status, CreatedAt: now, UpdatedAt: now}
}

func TestMemoryMigrationAndRepositoryLifecycle(t *testing.T) {
	store, ctx := openMemoryStore(t)
	owner := memory.Owner{TenantID: 9001, UserID: 9002}
	now := time.Now().UTC().Truncate(time.Microsecond)
	value := integrationMemory(t, owner, memory.StatusActive, "slot-"+uuid.NewString(), now)
	mutation := memory.Mutation{Owner: owner, NewMemory: &value, Actor: "integration-user", ReasonCode: "created", IdempotencyKey: "exec-" + uuid.NewString() + ":0", InputHash: value.ContentHash, OccurredAt: now}
	created, err := store.CommitMutation(ctx, mutation)
	if err != nil || created.Memory.ID != value.ID {
		t.Fatalf("CommitMutation=%+v err=%v", created, err)
	}
	replay, err := store.CommitMutation(ctx, mutation)
	if err != nil || !replay.Replayed || replay.Memory.ID != value.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	mutation.InputHash = stringsOf('a', 64)
	if _, err := store.CommitMutation(ctx, mutation); !errors.Is(err, memory.ErrIdempotencyConflict) {
		t.Fatalf("idempotency error=%v", err)
	}
	got, err := store.BatchGet(ctx, owner, []string{value.ID})
	if err != nil || len(got) != 1 {
		t.Fatalf("BatchGet=%v err=%v", got, err)
	}
	other, err := store.BatchGet(ctx, memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID + 1}, []string{value.ID})
	if err != nil || len(other) != 0 {
		t.Fatalf("cross owner=%v err=%v", other, err)
	}
	exact, err := store.FindExact(ctx, memory.ExactQuery{Owner: owner, Scope: value.Scope, Namespace: value.Namespace, SlotKey: value.SlotKey, Limit: 10})
	if err != nil || len(exact) != 1 {
		t.Fatalf("FindExact=%v err=%v", exact, err)
	}
	projections, err := store.ClaimProjections(ctx, 10, now.Add(time.Second))
	if err != nil || len(projections) == 0 {
		t.Fatalf("ClaimProjections=%v err=%v", projections, err)
	}
	var projection memory.Projection
	for _, p := range projections {
		if p.MemoryID == value.ID {
			projection = p
		}
	}
	if projection.ID == "" {
		t.Fatal("active memory projection missing")
	}
	if err := store.CompleteProjection(ctx, owner, projection.ID, now); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryAtomicSupersedeAndConcurrentVersion(t *testing.T) {
	store, ctx := openMemoryStore(t)
	owner := memory.Owner{TenantID: 9101, UserID: 9102}
	now := time.Now().UTC().Truncate(time.Microsecond)
	slot := "slot-" + uuid.NewString()
	old := integrationMemory(t, owner, memory.StatusActive, slot, now)
	if _, err := store.CommitMutation(ctx, memory.Mutation{Owner: owner, NewMemory: &old, IdempotencyKey: uuid.NewString(), InputHash: old.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	failed := integrationMemory(t, owner, memory.StatusActive, slot, now.Add(time.Second))
	failed.LineageID = old.LineageID
	failed.LineageVersion = 2
	failed.SupersedesID = old.ID
	_, err := store.CommitMutation(ctx, memory.Mutation{Owner: owner, NewMemory: &failed, Targets: []memory.MutationTarget{{ID: old.ID, ExpectedRowVersion: 1, NewStatus: memory.StatusSuperseded}}, Relations: []memory.Relation{{FromID: failed.ID, ToID: uuid.NewString(), Type: memory.RelationSupersedes}}, IdempotencyKey: uuid.NewString(), InputHash: failed.ContentHash, OccurredAt: now.Add(time.Second)})
	if err == nil {
		t.Fatal("expected relation FK failure")
	}
	stillOld, _ := store.BatchGet(ctx, owner, []string{old.ID, failed.ID})
	if len(stillOld) != 1 || stillOld[0].Status != memory.StatusActive {
		t.Fatalf("rollback failed: %+v", stillOld)
	}

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := integrationMemory(t, owner, memory.StatusActive, slot, now.Add(time.Duration(i+2)*time.Second))
			next.LineageID = old.LineageID
			next.LineageVersion = 2
			next.SupersedesID = old.ID
			_, err := store.CommitMutation(context.Background(), memory.Mutation{Owner: owner, NewMemory: &next, Targets: []memory.MutationTarget{{ID: old.ID, ExpectedRowVersion: 1, NewStatus: memory.StatusSuperseded}}, Relations: []memory.Relation{{FromID: next.ID, ToID: old.ID, Type: memory.RelationSupersedes}}, IdempotencyKey: uuid.NewString(), InputHash: next.ContentHash, OccurredAt: next.CreatedAt})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, memory.ErrStateConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent error=%v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
}

func TestMemoryCandidateTransitionsAndExpiry(t *testing.T) {
	store, ctx := openMemoryStore(t)
	owner := memory.Owner{TenantID: 9201, UserID: 9202}
	now := time.Now().UTC().Truncate(time.Microsecond)
	candidate := integrationMemory(t, owner, memory.StatusCandidate, "candidate-"+uuid.NewString(), now)
	if _, err := store.CommitMutation(ctx, memory.Mutation{Owner: owner, NewMemory: &candidate, IdempotencyKey: uuid.NewString(), InputHash: candidate.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	activated, err := store.TransitionMemory(ctx, owner, candidate.ID, 1, memory.StatusActive, "user", "approved", uuid.NewString(), candidate.ContentHash, now.Add(time.Second))
	if err != nil || activated.Memory.Status != memory.StatusActive {
		t.Fatalf("activate=%+v err=%v", activated, err)
	}
	revoked, err := store.TransitionMemory(ctx, owner, candidate.ID, 2, memory.StatusRevoked, "user", "withdrawn", uuid.NewString(), stringsOf('b', 64), now.Add(2*time.Second))
	if err != nil || revoked.Memory.Status != memory.StatusRevoked {
		t.Fatalf("revoke=%+v err=%v", revoked, err)
	}
	if _, err := store.TransitionMemory(ctx, owner, candidate.ID, 3, memory.StatusActive, "user", "illegal", uuid.NewString(), stringsOf('c', 64), now); !errors.Is(err, memory.ErrStateConflict) {
		t.Fatalf("terminal restore error=%v", err)
	}
	expiry := now.Add(time.Second)
	expiring := integrationMemory(t, owner, memory.StatusCandidate, "expire-"+uuid.NewString(), now)
	expiring.Layer = memory.LayerSession
	expiring.Scope = memory.Scope{Type: memory.ScopeSession, ID: "session"}
	expiring.ExpiresAt = &expiry
	if _, err := store.CommitMutation(ctx, memory.Mutation{Owner: owner, NewMemory: &expiring, IdempotencyKey: uuid.NewString(), InputHash: expiring.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	count, err := store.Expire(ctx, owner, now.Add(2*time.Second), 10)
	if err != nil || count < 1 {
		t.Fatalf("Expire=%d err=%v", count, err)
	}
}

func stringsOf(ch byte, n int) string {
	value := make([]byte, n)
	for i := range value {
		value[i] = ch
	}
	return string(value)
}
