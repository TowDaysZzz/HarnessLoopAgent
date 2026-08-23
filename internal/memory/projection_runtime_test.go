package memory

import (
	"context"
	"testing"
	"time"
)

type projectionRunnerFake struct{ calls int }

func (f *projectionRunnerFake) RunBatch(context.Context, time.Time) (ProjectionBatchResult, error) {
	f.calls++
	return ProjectionBatchResult{Claimed: 1}, nil
}

func TestProjectionDisabledDefersWithoutClaimOrReadinessFailure(t *testing.T) {
	now := time.Now().UTC()
	repo := NewFakeRepositoryWithProjectionVersion("v3")
	value := validRecord(now)
	if _, err := repo.CommitMutation(context.Background(), Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "disabled-active", InputHash: value.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	runner := &projectionRunnerFake{}
	runtime, err := NewProjectionRuntime(false, runner, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Ready(); err != nil {
		t.Fatalf("disabled readiness=%v", err)
	}
	result, err := runtime.RunBatch(context.Background(), now)
	if err != nil || result.Claimed != 0 || runner.calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, runner.calls, err)
	}
	backlog, err := runtime.Backlog(context.Background())
	if err != nil || backlog != 1 {
		t.Fatalf("backlog=%d err=%v", backlog, err)
	}
}

func TestProjectionEnabledRequiresRunner(t *testing.T) {
	if _, err := NewProjectionRuntime(true, nil, NewFakeRepository()); err == nil {
		t.Fatal("enabled projection must require runner")
	}
}
