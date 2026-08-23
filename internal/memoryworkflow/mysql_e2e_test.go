package memoryworkflow_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type mysqlE2EExtractor struct{}

func (mysqlE2EExtractor) ExtractMemoryDraft(_ context.Context, _ memory.Owner, query string) (memoryworkflow.Draft, error) {
	value, text := "tea", "我喜欢茶"
	switch {
	case strings.Contains(query, "咖啡"):
		value, text = "coffee", "我喜欢咖啡"
	case strings.Contains(query, "牛奶"):
		value, text = "milk", "我喜欢牛奶"
	case strings.Contains(query, "水"):
		value, text = "water", "我喜欢水"
	}
	structured := memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"drink": value}}
	canonical, normalized, hash, err := memory.NormalizeContent(text, structured)
	if err != nil {
		return memoryworkflow.Draft{}, err
	}
	return memoryworkflow.Draft{Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", SlotKey: "drink", CanonicalText: canonical, StructuredValue: normalized, ContentHash: hash, Authority: memory.AuthorityUserStated, Confidence: 1, Salience: .8, Source: memory.SourceRef{Type: "workflow", ID: "mysql-e2e"}}, nil
}

type mysqlE2EResolver struct{}

func (mysqlE2EResolver) ResolveMemoryConflict(_ context.Context, _ memory.Owner, draft memoryworkflow.Draft, candidates []memory.Record, intent memory.IntentAuthority) (memoryworkflow.PolicyResult, error) {
	if len(candidates) == 0 {
		return memoryworkflow.PolicyResult{Action: memory.ActionAddCandidate, NeedsReview: true, ReasonCode: "new_fact"}, nil
	}
	if candidates[0].ContentHash == draft.ContentHash {
		return memoryworkflow.PolicyResult{Action: memory.ActionNoop, TargetMemoryID: candidates[0].ID, ReasonCode: "content_duplicate"}, nil
	}
	if intent == memory.IntentUserCorrection {
		return memoryworkflow.PolicyResult{Action: memory.ActionSupersede, TargetMemoryID: candidates[0].ID, NeedsReview: true, ReasonCode: "user_correction"}, nil
	}
	return memoryworkflow.PolicyResult{Action: memory.ActionReview, TargetMemoryID: candidates[0].ID, NeedsReview: true, ReasonCode: "conflict"}, nil
}

func TestMySQLOnlyMemoryCaptureRestartRecallCorrectionEditAndDeferredOutbox(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run MySQL-only Memory E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 8, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ProjectionVersion: "mysql-only-e2e-v1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := workflow.WorkflowOwner{TenantID: uint64(now.UnixNano()%900000000) + 1000000000, OwnerID: uint64(now.UnixNano()%800000000) + 2000000000}
	beforeBacklog, err := store.PendingProjectionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}

	firstService := newMySQLCaptureService(t, store, now)
	first, err := firstService.Start(ctx, memoryworkflow.StartCaptureInput{Owner: owner, Query: "请记住我喜欢茶", IdempotencyKey: "mysql-first", Intent: memory.IntentUserStatement})
	if err != nil || first.Review == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// Recreate the runtime to prove review resumes from MySQL checkpoint after restart.
	restarted := newMySQLCaptureService(t, store, now)
	first = approveMySQLCapture(t, ctx, restarted, owner, first)
	oldID := first.Committed.ID
	assertMySQLRecall(t, ctx, store, memory.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID}, now, oldID, "我喜欢茶")

	duplicateBacklog, _ := store.PendingProjectionCount(ctx)
	duplicate, err := restarted.Start(ctx, memoryworkflow.StartCaptureInput{Owner: owner, Query: "请记住我喜欢茶", IdempotencyKey: "mysql-duplicate", Intent: memory.IntentUserStatement})
	afterDuplicateBacklog, _ := store.PendingProjectionCount(ctx)
	if err != nil || duplicate.Status != string(workflow.RunCompleted) || duplicate.Committed == nil || duplicate.Committed.ID != oldID || duplicate.Review != nil || afterDuplicateBacklog != duplicateBacklog {
		t.Fatalf("duplicate=%+v backlog=%d->%d err=%v", duplicate, duplicateBacklog, afterDuplicateBacklog, err)
	}

	correction, err := restarted.Start(ctx, memoryworkflow.StartCaptureInput{Owner: owner, Query: "把饮料偏好改成咖啡", IdempotencyKey: "mysql-correction", Intent: memory.IntentUserCorrection})
	if err != nil || correction.Policy == nil || correction.Policy.Action != memory.ActionSupersede {
		t.Fatalf("correction=%+v err=%v", correction, err)
	}
	correction = approveMySQLCapture(t, ctx, restarted, owner, correction)
	coffeeID := correction.Committed.ID

	editRun, err := restarted.Start(ctx, memoryworkflow.StartCaptureInput{Owner: owner, Query: "把饮料偏好改成水", IdempotencyKey: "mysql-edit", Intent: memory.IntentUserCorrection})
	if err != nil || editRun.Review == nil {
		t.Fatalf("edit start=%+v err=%v", editRun, err)
	}
	oldCandidateID := editRun.Review.Candidate.ID
	editRun, err = restarted.Resume(ctx, memoryworkflow.ResumeCaptureInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "e2e"}, RunID: workflow.WorkflowRunID(editRun.RunID), WaitID: workflow.WaitID(editRun.Review.WaitID), Version: editRun.Review.Version, ContentHash: editRun.Review.ContentHash, Action: workflow.ActionSubmitEdit, EditText: "改成牛奶"})
	if err != nil || editRun.Review == nil || editRun.Review.Candidate.ID == oldCandidateID || editRun.Policy == nil || editRun.Policy.Action != memory.ActionSupersede {
		t.Fatalf("edited=%+v err=%v", editRun, err)
	}
	final := approveMySQLCapture(t, ctx, restarted, owner, editRun)
	assertMySQLRecall(t, ctx, store, memory.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID}, now, final.Committed.ID, "我喜欢牛奶")

	versions, err := store.BatchGet(ctx, memory.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID}, []string{oldID, coffeeID, oldCandidateID, final.Committed.ID})
	if err != nil || len(versions) != 4 || versions[0].Status != memory.StatusSuperseded || versions[0].SupersededBy != coffeeID || versions[1].Status != memory.StatusSuperseded || versions[1].SupersededBy != final.Committed.ID || versions[2].Status != memory.StatusRejected || versions[3].Status != memory.StatusActive {
		t.Fatalf("versions=%+v err=%v", versions, err)
	}
	afterBacklog, err := store.PendingProjectionCount(ctx)
	if err != nil || afterBacklog-beforeBacklog != 3 {
		t.Fatalf("deferred outbox backlog=%d->%d err=%v", beforeBacklog, afterBacklog, err)
	}
}

func newMySQLCaptureService(t *testing.T, store *mysqlstore.Store, now time.Time) *memoryworkflow.CaptureService {
	t.Helper()
	extractor, resolver := mysqlE2EExtractor{}, mysqlE2EResolver{}
	edits := &memoryworkflow.EditPayloadService{Store: store, Extractor: extractor, TTL: time.Hour, Now: func() time.Time { return now }}
	nodes := memoryworkflow.Nodes{Extract: memoryworkflow.ExtractNode{Extractor: extractor}, ExactCandidateLookup: memoryworkflow.ExactCandidateLookupNode{Repository: store, Now: func() time.Time { return now }, MaxCandidates: 20}, Conflict: memoryworkflow.ConflictNode{Resolver: resolver, Repository: store, Now: func() time.Time { return now }}, Review: memoryworkflow.ReviewNode{Repository: store, Resolver: resolver, EditLoader: edits, Now: func() time.Time { return now }, TTL: time.Hour, MaxCandidates: 20}, Commit: memoryworkflow.CommitNode{Repository: store, Now: func() time.Time { return now }}}
	runtime, err := workflow.NewDurableRuntime(store.WorkflowStore(), nodes.List(), "mysql-memory-v1", memoryworkflow.CaptureCodec{MaxBytes: 32 * 1024}, workflow.DurableRuntimeOptions{LeaseDuration: time.Minute, MaxCheckpointBytes: 64 * 1024, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := memoryworkflow.NewCaptureService(runtime, edits, memoryworkflow.CaptureServiceConfig{DefinitionVersion: "mysql-memory-v1", MaxSteps: 16, MaxResumes: 5, MaxDraftChars: 256, RunTTL: 24 * time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func approveMySQLCapture(t *testing.T, ctx context.Context, service *memoryworkflow.CaptureService, owner workflow.WorkflowOwner, value memoryworkflow.CaptureDTO) memoryworkflow.CaptureDTO {
	t.Helper()
	completed, err := service.Resume(ctx, memoryworkflow.ResumeCaptureInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "e2e"}, RunID: workflow.WorkflowRunID(value.RunID), WaitID: workflow.WaitID(value.Review.WaitID), Version: value.Review.Version, ContentHash: value.Review.ContentHash, Action: workflow.ActionApprove})
	if err != nil || completed.Committed == nil {
		t.Fatalf("approve=%+v err=%v", completed, err)
	}
	return completed
}

func assertMySQLRecall(t *testing.T, ctx context.Context, repository memory.Repository, owner memory.Owner, now time.Time, wantID, wantText string) {
	t.Helper()
	recall, err := memory.NewRecallService(repository, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 5, MaxTarget: 10, PageSize: 10, MaxScanned: 20, MaxBatches: 2, MaxDuration: time.Second, MaxContextChars: 4096, PlanMinConfidence: .75, MaxExactCandidates: 20})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recall.Recall(ctx, memory.RecallRequest{Owner: owner, Query: "饮料偏好", Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", SlotKey: "drink"}, now)
	if err != nil || len(result.Items) != 1 || result.Items[0].Memory.ID != wantID || !strings.Contains(result.Context, wantText) {
		t.Fatalf("recall=%+v err=%v", result, err)
	}
}
