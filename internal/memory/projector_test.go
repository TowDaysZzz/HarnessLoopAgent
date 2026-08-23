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
