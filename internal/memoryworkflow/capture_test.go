package memoryworkflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type fakeRecall struct{}

func (fakeRecall) Recall(context.Context, memory.RecallRequest, time.Time) (memory.RecallResult, error) {
	return memory.RecallResult{}, nil
}

type fakeExtractor struct{ draft Draft }

func (f fakeExtractor) ExtractMemoryDraft(context.Context, memory.Owner, string) (Draft, error) {
	return f.draft, nil
}

type fakeResolver struct{ calls int }

func (f *fakeResolver) ResolveMemoryConflict(context.Context, memory.Owner, Draft) (PolicyResult, error) {
	f.calls++
	return PolicyResult{Action: memory.ActionReview, NeedsReview: true, ReasonCode: "needs_review"}, nil
}

type fakeEditor struct{ draft Draft }

func (f fakeEditor) LoadEditedMemoryDraft(context.Context, memory.Owner, string) (Draft, error) {
	return f.draft, nil
}

func captureDraft(text, value string) Draft {
	structured := memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"drink": value}}
	normalized, structured, hash, _ := memory.NormalizeContent(text, structured)
	return Draft{Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", SlotKey: "drink", CanonicalText: normalized, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserStated, Confidence: 1, Salience: .8, Source: memory.SourceRef{Type: "workflow", ID: "capture"}}
}

func captureState(now time.Time) workflow.WorkflowState[CaptureData] {
	return workflow.WorkflowState[CaptureData]{Meta: workflow.RunMetadata{WorkflowID: "memory-capture", DefinitionVersion: "v1", RunID: "memory-run", StartedAt: now}, Control: workflow.ControlState{Status: workflow.RunPending}, Budget: workflow.BudgetState{MaxSteps: 12, MaxResumes: 3, Deadline: now.Add(24 * time.Hour)}, Data: CaptureData{Owner: memory.Owner{TenantID: 9, UserID: 7}, Query: "remember that I prefer tea"}}
}

func captureNodes(repo memory.Repository, resolver *fakeResolver, editor EditLoader, now time.Time) Nodes {
	return Nodes{Recall: RecallNode{Service: fakeRecall{}, Now: func() time.Time { return now }}, Extract: ExtractNode{Extractor: fakeExtractor{draft: captureDraft("I prefer tea", "tea")}}, Conflict: ConflictNode{Resolver: resolver}, Review: ReviewNode{Repository: repo, Resolver: resolver, EditLoader: editor, Now: func() time.Time { return now }, TTL: time.Hour}, Commit: CommitNode{Repository: repo, Now: func() time.Time { return now }}}
}

func TestMemoryCaptureWorkflowApprovesAndCommitReplayIsIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	repo := memory.NewFakeRepository()
	resolver := &fakeResolver{}
	nodes := captureNodes(repo, resolver, nil, now)
	runner, err := workflow.NewRunner(nodes.List(), workflow.RunnerOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := runner.Run(context.Background(), captureState(now))
	if err != nil || suspended.Status != workflow.RunSuspended || suspended.State.Data.Review == nil {
		t.Fatalf("Run=%+v err=%v", suspended, err)
	}
	wait := suspended.State.Control.PendingWait
	command := workflow.ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: workflow.ActionApprove}
	ctx := workflow.WithResolvedActor(context.Background(), workflow.ActorRef{Type: "user", ID: "7"})
	completed, err := runner.Resume(ctx, suspended.State, command)
	if err != nil || completed.Status != workflow.RunCompleted || completed.State.Data.Committed == nil {
		t.Fatalf("Resume=%+v err=%v", completed, err)
	}
	stored, _ := repo.BatchGet(context.Background(), completed.State.Data.Owner, []string{completed.State.Data.Committed.ID})
	if len(stored) != 1 || stored[0].Status != memory.StatusActive {
		t.Fatalf("stored=%+v", stored)
	}
	commit := nodes.Commit
	input := workflow.NodeInput[CaptureData]{State: suspended.State, ExecutionID: "memory-run:memory-commit:1"}
	input.State.Data.Review.Decision = "approved"
	input.State.Data.Review.ActorType = "user"
	input.State.Data.Review.ActorID = "7"
	first, err := commit.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := commit.Execute(context.Background(), input)
	if err != nil || first.State.Data.Committed.ID != second.State.Data.Committed.ID {
		t.Fatalf("commit replay first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestMemoryCaptureEditCreatesNewCandidateAndRerunsConflict(t *testing.T) {
	now := time.Now().UTC()
	repo := memory.NewFakeRepository()
	resolver := &fakeResolver{}
	edited := captureDraft("I prefer coffee", "coffee")
	nodes := captureNodes(repo, resolver, fakeEditor{draft: edited}, now)
	runner, _ := workflow.NewRunner(nodes.List(), workflow.RunnerOptions{Now: func() time.Time { return now }})
	first, err := runner.Run(context.Background(), captureState(now))
	if err != nil {
		t.Fatal(err)
	}
	oldID := first.State.Data.Review.Candidate.ID
	wait := first.State.Control.PendingWait
	ctx := workflow.WithResolvedActor(context.Background(), workflow.ActorRef{Type: "user", ID: "7"})
	edit := workflow.ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: workflow.ActionSubmitEdit, PayloadRef: "draft:edited"}
	second, err := runner.Resume(ctx, first.State, edit)
	if err != nil || second.Status != workflow.RunSuspended || second.State.Data.Review.WaitVersion != 2 || second.State.Data.Review.Candidate.ID == oldID {
		t.Fatalf("edit=%+v err=%v", second, err)
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls=%d", resolver.calls)
	}
	old, _ := repo.BatchGet(context.Background(), second.State.Data.Owner, []string{oldID})
	if len(old) != 1 || old[0].Status != memory.StatusRejected {
		t.Fatalf("old candidate=%+v", old)
	}
	wait = second.State.Control.PendingWait
	approve := workflow.ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: workflow.ActionApprove}
	completed, err := runner.Resume(ctx, second.State, approve)
	if err != nil || completed.Status != workflow.RunCompleted {
		t.Fatalf("approve=%+v err=%v", completed, err)
	}
}

func TestCaptureCodecMinimizesCheckpointAndRejectsSecrets(t *testing.T) {
	now := time.Now().UTC()
	data := captureState(now).Data
	draft := captureDraft("tea", "tea")
	data.Draft = &draft
	codec := CaptureCodec{MaxBytes: 4096}
	raw, err := codec.Encode(data)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw)
	if err != nil || decoded.Draft.ContentHash != draft.ContentHash {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	data.Query = "Authorization: Bearer secret-token"
	if _, err := codec.Encode(data); !errors.Is(err, ErrInvalidCaptureData) {
		t.Fatalf("secret error=%v", err)
	}
	if _, err := codec.Decode([]byte(`{"owner":{"tenant_id":1,"user_id":2},"query":"x","full_chat":["unbounded"]}`)); err == nil {
		t.Fatal("unknown full_chat must be rejected")
	}
	data = captureState(now).Data
	data.Query = strings.Repeat("x", 4097)
	if _, err := codec.Encode(data); !errors.Is(err, ErrInvalidCaptureData) {
		t.Fatalf("oversize query error=%v", err)
	}
}

func TestPinnedMemoryChangeFailsExplicitly(t *testing.T) {
	now := time.Now().UTC()
	repo := memory.NewFakeRepository()
	value := memoryRecord(t, now)
	if _, err := repo.CommitMutation(context.Background(), memory.Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "create", InputHash: "h", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	ref := value.Ref()
	if err := ValidatePinned(context.Background(), value.Owner, []memory.MemoryRef{ref}, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TransitionMemory(context.Background(), value.Owner, value.ID, 1, memory.StatusRevoked, "user", "x", "revoke", "r", now); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePinned(context.Background(), value.Owner, []memory.MemoryRef{ref}, repo); !errors.Is(err, ErrPinnedMemoryChanged) {
		t.Fatalf("error=%v", err)
	}
}

func TestDurableMemoryCaptureResumesAfterRuntimeRestart(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	repo := memory.NewFakeRepository()
	resolver := &fakeResolver{}
	nodes := captureNodes(repo, resolver, nil, now)
	store := workflow.NewMemoryDurableStore()
	codec := CaptureCodec{MaxBytes: 16 * 1024}
	options := workflow.DurableRuntimeOptions{LeaseDuration: time.Minute, MaxCheckpointBytes: 32 * 1024, Now: func() time.Time { return now }, NewClaimToken: func() string { return "claim-token" }}
	runtime1, err := workflow.NewDurableRuntime(store, nodes.List(), "v1", codec, options)
	if err != nil {
		t.Fatal(err)
	}
	state := captureState(now)
	owner := workflow.WorkflowOwner{TenantID: 9, OwnerID: 7}
	started, err := runtime1.Start(context.Background(), workflow.StartWorkflowInput[CaptureData]{Owner: owner, IdempotencyKey: "capture-1", State: state})
	if err != nil || started.Status != workflow.RunSuspended {
		t.Fatalf("Start=%+v err=%v", started, err)
	}
	runtime2, err := workflow.NewDurableRuntime(store, nodes.List(), "v1", codec, options)
	if err != nil {
		t.Fatal(err)
	}
	wait := started.State.Control.PendingWait
	command := workflow.ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: workflow.ActionReject}
	completed, err := runtime2.Resume(context.Background(), owner, workflow.ActorRef{Type: "user", ID: "7"}, state.Meta.RunID, command)
	if err != nil || completed.Status != workflow.RunCompleted {
		t.Fatalf("Resume=%+v err=%v", completed, err)
	}
	records, _ := repo.BatchGet(context.Background(), state.Data.Owner, []string{completed.State.Data.Committed.ID})
	if len(records) != 1 || records[0].Status != memory.StatusRejected {
		t.Fatalf("records=%+v", records)
	}
}

func memoryRecord(t *testing.T, now time.Time) memory.Record {
	t.Helper()
	d := captureDraft("tea", "tea")
	return memory.Record{ID: "123e4567-e89b-12d3-a456-426614174000", Owner: memory.Owner{TenantID: 9, UserID: 7}, Layer: d.Layer, Kind: d.Kind, Scope: d.Scope, Namespace: d.Namespace, SlotKey: d.SlotKey, LineageID: "123e4567-e89b-12d3-a456-426614174001", LineageVersion: 1, RowVersion: 1, CanonicalText: d.CanonicalText, StructuredValue: d.StructuredValue, ContentHash: d.ContentHash, Authority: d.Authority, Confidence: d.Confidence, Salience: d.Salience, Source: d.Source, Status: memory.StatusActive, CreatedAt: now, UpdatedAt: now}
}
