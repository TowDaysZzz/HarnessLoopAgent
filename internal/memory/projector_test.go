package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type fakeMemoryIndexer struct {
	calls []ragclient.MemoryIndexRequest
	err   error
}

func (f *fakeMemoryIndexer) IndexMemory(_ context.Context, request ragclient.MemoryIndexRequest) (*ragclient.MemoryIndexResponse, error) {
	f.calls = append(f.calls, request)
	if f.err != nil {
		return nil, f.err
	}
	return &ragclient.MemoryIndexResponse{MemoryID: request.MemoryID, Indexed: true}, nil
}

type temporaryIndexError struct{}

func (temporaryIndexError) Error() string   { return "temporary" }
func (temporaryIndexError) Temporary() bool { return true }

func TestProjectorIndexesOnlyCurrentActiveMemory(t *testing.T) {
	ctx := context.Background()
	repo := NewFakeRepository()
	now := time.Now().UTC()
	value := validRecord(now)
	if _, err := repo.CommitMutation(ctx, Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "create", InputHash: "h", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	indexer := &fakeMemoryIndexer{}
	worker, err := NewProjector(repo, indexer, ProjectorConfig{BatchSize: 10, BaseBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 3, ModelVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunBatch(ctx, now)
	if err != nil || result.Indexed != 1 || len(indexer.calls) != 1 || indexer.calls[0].MemoryID != value.ID {
		t.Fatalf("result=%+v calls=%+v err=%v", result, indexer.calls, err)
	}
	result, err = worker.RunBatch(ctx, now)
	if err != nil || result.Claimed != 0 || len(indexer.calls) != 1 {
		t.Fatalf("idempotent result=%+v calls=%d err=%v", result, len(indexer.calls), err)
	}
}

func TestProjectorSkipsObsoleteWithoutVectorMutation(t *testing.T) {
	ctx := context.Background()
	repo := NewFakeRepository()
	now := time.Now().UTC()
	value := validRecord(now)
	if _, err := repo.CommitMutation(ctx, Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "create", InputHash: "h", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TransitionMemory(ctx, value.Owner, value.ID, 1, StatusRevoked, "user", "withdrawn", "revoke", "h2", now); err != nil {
		t.Fatal(err)
	}
	indexer := &fakeMemoryIndexer{}
	worker, _ := NewProjector(repo, indexer, ProjectorConfig{BatchSize: 10, BaseBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 3, ModelVersion: "v1"})
	result, err := worker.RunBatch(ctx, now)
	if err != nil || result.Skipped != 1 || len(indexer.calls) != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, len(indexer.calls), err)
	}
}

func TestProjectorBackoffAndPermanentFailure(t *testing.T) {
	ctx := context.Background()
	repo := NewFakeRepository()
	now := time.Now().UTC()
	value := validRecord(now)
	if _, err := repo.CommitMutation(ctx, Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "create", InputHash: "h", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	indexer := &fakeMemoryIndexer{err: temporaryIndexError{}}
	worker, _ := NewProjector(repo, indexer, ProjectorConfig{BatchSize: 10, BaseBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 2, ModelVersion: "v1"})
	result, err := worker.RunBatch(ctx, now)
	if err != nil || result.Failed != 1 {
		t.Fatalf("first=%+v err=%v", result, err)
	}
	result, _ = worker.RunBatch(ctx, now.Add(500*time.Millisecond))
	if result.Claimed != 0 {
		t.Fatalf("claimed before backoff: %+v", result)
	}
	result, err = worker.RunBatch(ctx, now.Add(time.Second))
	if err != nil || result.PermanentFailed != 1 {
		t.Fatalf("second=%+v err=%v", result, err)
	}
	result, _ = worker.RunBatch(ctx, now.Add(time.Hour))
	if result.Claimed != 0 {
		t.Fatalf("permanent failure retried: %+v", result)
	}
	if len(indexer.calls) != 2 {
		t.Fatalf("calls=%d", len(indexer.calls))
	}
	indexer.err = errors.New("Bearer secret-must-not-persist")
	_ = indexer
}

func TestProjectionIntentVersionAndActivationReplayAreUnique(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := NewFakeRepositoryWithProjectionVersion("embedding-v3")
	active := validRecord(now)
	active.ID = "active-versioned"
	active.LineageID = "line-active-versioned"
	mutation := Mutation{Owner: active.Owner, NewMemory: &active, IdempotencyKey: "active-create", InputHash: active.ContentHash, OccurredAt: now}
	if _, err := repo.CommitMutation(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	if replay, err := repo.CommitMutation(ctx, mutation); err != nil || !replay.Replayed {
		t.Fatalf("active replay=%+v err=%v", replay, err)
	}
	candidate := validRecord(now)
	candidate.ID = "candidate-versioned"
	candidate.LineageID = "line-candidate-versioned"
	candidate.SlotKey = "candidate-versioned"
	candidate.Status = StatusCandidate
	if _, err := repo.CommitMutation(ctx, Mutation{Owner: candidate.Owner, NewMemory: &candidate, IdempotencyKey: "candidate-create", InputHash: candidate.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	key := "candidate-activate"
	if _, err := repo.TransitionMemory(ctx, candidate.Owner, candidate.ID, 1, StatusActive, "user", "approved", key, candidate.ContentHash, now); err != nil {
		t.Fatal(err)
	}
	if replay, err := repo.TransitionMemory(ctx, candidate.Owner, candidate.ID, 1, StatusActive, "user", "approved", key, candidate.ContentHash, now); err != nil || !replay.Replayed {
		t.Fatalf("transition replay=%+v err=%v", replay, err)
	}
	projections, err := repo.ClaimProjections(ctx, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 2 {
		t.Fatalf("projections=%+v", projections)
	}
	seen := map[string]int{}
	for _, projection := range projections {
		seen[projection.MemoryID]++
		if projection.ModelVersion != "embedding-v3" {
			t.Fatalf("version=%q", projection.ModelVersion)
		}
	}
	if seen[active.ID] != 1 || seen[candidate.ID] != 1 {
		t.Fatalf("seen=%+v", seen)
	}
}

func TestProjectorBackfillsHistoricalPendingAfterEnable(t *testing.T) {
	now := time.Now().UTC()
	repo := NewFakeRepositoryWithProjectionVersion("v-history")
	value := validRecord(now)
	value.ID = "historical"
	value.LineageID = "historical-line"
	if _, err := repo.CommitMutation(context.Background(), Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "historical-create", InputHash: value.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	// No worker ran while Projection was disabled; enabling later consumes the pending intent.
	indexer := &fakeMemoryIndexer{}
	projector, _ := NewProjector(repo, indexer, ProjectorConfig{BatchSize: 10, BaseBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 3, ModelVersion: "v-history"})
	result, err := projector.RunBatch(context.Background(), now)
	if err != nil || result.Indexed != 1 || len(indexer.calls) != 1 {
		t.Fatalf("result=%+v calls=%+v err=%v", result, indexer.calls, err)
	}
}

func TestProjectorSkipsSupersededPendingAndIndexesReplacement(t *testing.T) {
	now := time.Now().UTC()
	repo := NewFakeRepositoryWithProjectionVersion("v1")
	old := validRecord(now)
	old.ID = "old-pending"
	old.LineageID = "line-pending"
	if _, err := repo.CommitMutation(context.Background(), Mutation{Owner: old.Owner, NewMemory: &old, IdempotencyKey: "old-pending-create", InputHash: old.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	text, structured, hash, _ := NormalizeContent("User prefers coffee", StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"drink": "coffee"}})
	next := old
	next.ID = "new-pending"
	next.LineageVersion = 2
	next.RowVersion = 1
	next.CanonicalText = text
	next.StructuredValue = structured
	next.ContentHash = hash
	next.SupersedesID = old.ID
	if _, err := repo.CommitMutation(context.Background(), Mutation{Owner: old.Owner, NewMemory: &next, Targets: []MutationTarget{{ID: old.ID, ExpectedRowVersion: 1, NewStatus: StatusSuperseded}}, IdempotencyKey: "new-pending-create", InputHash: next.ContentHash, OccurredAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	indexer := &fakeMemoryIndexer{}
	projector, _ := NewProjector(repo, indexer, ProjectorConfig{BatchSize: 10, BaseBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 3, ModelVersion: "v1"})
	result, err := projector.RunBatch(context.Background(), now.Add(time.Second))
	if err != nil || result.Skipped != 1 || result.Indexed != 1 || len(indexer.calls) != 1 || indexer.calls[0].MemoryID != next.ID {
		t.Fatalf("result=%+v calls=%+v err=%v", result, indexer.calls, err)
	}
}

func TestProjectorRejectsMismatchedProjectionVersionWithoutRAGCall(t *testing.T) {
	now := time.Now().UTC()
	repo := NewFakeRepositoryWithProjectionVersion("v-old")
	value := validRecord(now)
	if _, err := repo.CommitMutation(context.Background(), Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "version-mismatch", InputHash: value.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	indexer := &fakeMemoryIndexer{}
	projector, _ := NewProjector(repo, indexer, ProjectorConfig{BatchSize: 10, BaseBackoff: time.Second, MaxBackoff: time.Minute, MaxAttempts: 3, ModelVersion: "v-new"})
	result, err := projector.RunBatch(context.Background(), now)
	if err != nil || result.PermanentFailed != 1 || len(indexer.calls) != 0 {
		t.Fatalf("result=%+v calls=%+v err=%v", result, indexer.calls, err)
	}
	result, err = projector.RunBatch(context.Background(), now.Add(time.Hour))
	if err != nil || result.Claimed != 0 {
		t.Fatalf("reclaimed=%+v err=%v", result, err)
	}
}
