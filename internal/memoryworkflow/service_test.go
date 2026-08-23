package memoryworkflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type queryDraftExtractor struct{}

func (queryDraftExtractor) ExtractMemoryDraft(_ context.Context, _ memory.Owner, query string) (Draft, error) {
	if strings.Contains(query, "咖啡") {
		return captureDraft("我喜欢咖啡", "coffee"), nil
	}
	return captureDraft("我喜欢茶", "tea"), nil
}

type correctionPolicyResolver struct{}

func (correctionPolicyResolver) ResolveMemoryConflict(_ context.Context, _ memory.Owner, draft Draft, candidates []memory.Record, intent memory.IntentAuthority) (PolicyResult, error) {
	if len(candidates) == 0 {
		return PolicyResult{Action: memory.ActionAddCandidate, NeedsReview: true, ReasonCode: "new_fact"}, nil
	}
	if candidates[0].ContentHash == draft.ContentHash {
		return PolicyResult{Action: memory.ActionNoop, TargetMemoryID: candidates[0].ID, ReasonCode: "content_duplicate"}, nil
	}
	if intent == memory.IntentUserCorrection {
		return PolicyResult{Action: memory.ActionSupersede, TargetMemoryID: candidates[0].ID, NeedsReview: true, ReasonCode: "user_correction"}, nil
	}
	return PolicyResult{Action: memory.ActionReview, TargetMemoryID: candidates[0].ID, NeedsReview: true, ReasonCode: "conflict"}, nil
}

func newCaptureServiceForTest(t *testing.T, now time.Time, draft Draft) (*CaptureService, *memory.FakeRepository) {
	return newCaptureServiceForTestWithTelemetry(t, now, draft, nil)
}

func newCaptureServiceForTestWithTelemetry(t *testing.T, now time.Time, draft Draft, telemetry CaptureTelemetry) (*CaptureService, *memory.FakeRepository) {
	t.Helper()
	repo := memory.NewFakeRepository()
	resolver := &fakeResolver{}
	nodes := Nodes{Extract: ExtractNode{Extractor: fakeExtractor{draft: draft}}, ExactCandidateLookup: ExactCandidateLookupNode{Repository: repo, Now: func() time.Time { return now }, MaxCandidates: 20}, Conflict: ConflictNode{Resolver: resolver, Repository: repo, Now: func() time.Time { return now }}, Review: ReviewNode{Repository: repo, Resolver: resolver, Now: func() time.Time { return now }, TTL: time.Hour, MaxCandidates: 20}, Commit: CommitNode{Repository: repo, Now: func() time.Time { return now }}}
	runtime, err := workflow.NewDurableRuntime(workflow.NewMemoryDurableStore(), nodes.List(), "v1", CaptureCodec{MaxBytes: 16 * 1024}, workflow.DurableRuntimeOptions{LeaseDuration: time.Minute, MaxCheckpointBytes: 32 * 1024, Now: func() time.Time { return now }, NewClaimToken: func() string { return "service-claim" }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCaptureService(runtime, nil, CaptureServiceConfig{DefinitionVersion: "v1", MaxSteps: 12, MaxResumes: 3, MaxDraftChars: 64, RunTTL: 24 * time.Hour, Now: func() time.Time { return now }, Telemetry: telemetry})
	if err != nil {
		t.Fatal(err)
	}
	return service, repo
}

func TestCaptureServiceEmitsBoundedLifecycleMetrics(t *testing.T) {
	now := time.Now().UTC()
	metrics := memory.NewMetrics()
	service, _ := newCaptureServiceForTestWithTelemetry(t, now, captureDraft("tea", "tea"), metrics)
	owner := workflow.WorkflowOwner{TenantID: 9, OwnerID: 7}
	started, err := service.Start(context.Background(), StartCaptureInput{Owner: owner, Query: "请记住", IdempotencyKey: "telemetry", Intent: memory.IntentUserStatement})
	if err != nil || started.Review == nil {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	_, err = service.Resume(context.Background(), ResumeCaptureInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "7"}, RunID: workflow.WorkflowRunID(started.RunID), WaitID: workflow.WaitID(started.Review.WaitID), Version: started.Review.Version, ContentHash: started.Review.ContentHash, Action: workflow.ActionReject})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.Snapshot()
	if snapshot.CaptureLifecycle["started"] != 1 || snapshot.CaptureLifecycle["suspended"] != 1 || snapshot.CaptureLifecycle["rejected"] != 1 || snapshot.CaptureLifecycle["completed"] != 1 {
		t.Fatalf("capture metrics=%+v", snapshot.CaptureLifecycle)
	}
}

func TestCaptureServiceCorrectionAtomicallySupersedesAndRecallReturnsOnlyNewVersion(t *testing.T) {
	now := time.Now().UTC()
	repository := memory.NewFakeRepositoryWithProjectionVersion("v1")
	extractor := queryDraftExtractor{}
	resolver := correctionPolicyResolver{}
	nodes := Nodes{
		Extract:              ExtractNode{Extractor: extractor},
		ExactCandidateLookup: ExactCandidateLookupNode{Repository: repository, Now: func() time.Time { return now }, MaxCandidates: 20},
		Conflict:             ConflictNode{Resolver: resolver, Repository: repository, Now: func() time.Time { return now }},
		Review:               ReviewNode{Repository: repository, Resolver: resolver, Now: func() time.Time { return now }, TTL: time.Hour, MaxCandidates: 20},
		Commit:               CommitNode{Repository: repository, Now: func() time.Time { return now }},
	}
	durable, err := workflow.NewDurableRuntime(workflow.NewMemoryDurableStore(), nodes.List(), "v1", CaptureCodec{MaxBytes: 16 * 1024}, workflow.DurableRuntimeOptions{LeaseDuration: time.Minute, MaxCheckpointBytes: 32 * 1024, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCaptureService(durable, nil, CaptureServiceConfig{DefinitionVersion: "v1", MaxSteps: 12, MaxResumes: 3, MaxDraftChars: 128, RunTTL: 24 * time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	owner := workflow.WorkflowOwner{TenantID: 9, OwnerID: 7}
	first, err := service.Start(context.Background(), StartCaptureInput{Owner: owner, Query: "请记住我喜欢茶", IdempotencyKey: "first", Intent: memory.IntentUserStatement})
	if err != nil {
		t.Fatal(err)
	}
	first = approveCapture(t, service, owner, first)
	oldID := first.Committed.ID
	backlogBeforeDuplicate, _ := repository.PendingProjectionCount(context.Background())
	duplicate, err := service.Start(context.Background(), StartCaptureInput{Owner: owner, Query: "请记住我喜欢茶", IdempotencyKey: "duplicate", Intent: memory.IntentUserStatement})
	backlogAfterDuplicate, _ := repository.PendingProjectionCount(context.Background())
	if err != nil || duplicate.Status != string(workflow.RunCompleted) || duplicate.Review != nil || duplicate.Committed == nil || duplicate.Committed.ID != oldID || backlogAfterDuplicate != backlogBeforeDuplicate {
		t.Fatalf("duplicate=%+v backlog=%d->%d err=%v", duplicate, backlogBeforeDuplicate, backlogAfterDuplicate, err)
	}

	second, err := service.Start(context.Background(), StartCaptureInput{Owner: owner, Query: "把饮料偏好改成咖啡", IdempotencyKey: "second", Intent: memory.IntentUserCorrection})
	if err != nil || second.Policy == nil || second.Policy.Action != memory.ActionSupersede || second.Policy.TargetMemoryID != oldID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	second = approveCapture(t, service, owner, second)
	values, err := repository.BatchGet(context.Background(), memory.Owner{TenantID: 9, UserID: 7}, []string{oldID, second.Committed.ID})
	if err != nil || len(values) != 2 || values[0].Status != memory.StatusSuperseded || values[0].SupersededBy != second.Committed.ID || values[1].Status != memory.StatusActive || values[1].SupersedesID != oldID || values[1].LineageID != values[0].LineageID || values[1].LineageVersion <= values[0].LineageVersion {
		t.Fatalf("versions=%+v err=%v", values, err)
	}

	recall, err := memory.NewRecallService(repository, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 5, MaxTarget: 10, PageSize: 10, MaxScanned: 20, MaxBatches: 2, MaxDuration: time.Second, MaxContextChars: 4096, PlanMinConfidence: .75, MaxExactCandidates: 20})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recall.Recall(context.Background(), memory.RecallRequest{Owner: memory.Owner{TenantID: 9, UserID: 7}, Query: "我的饮料偏好", Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", SlotKey: "drink"}, now)
	if err != nil || len(result.Items) != 1 || result.Items[0].Memory.ID != second.Committed.ID || strings.Contains(result.Context, "我喜欢茶") {
		t.Fatalf("recall=%+v err=%v", result, err)
	}
}

func approveCapture(t *testing.T, service *CaptureService, owner workflow.WorkflowOwner, value CaptureDTO) CaptureDTO {
	t.Helper()
	if value.Review == nil {
		t.Fatalf("capture has no review: %+v", value)
	}
	completed, err := service.Resume(context.Background(), ResumeCaptureInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "7"}, RunID: workflow.WorkflowRunID(value.RunID), WaitID: workflow.WaitID(value.Review.WaitID), Version: value.Review.Version, ContentHash: value.Review.ContentHash, Action: workflow.ActionApprove})
	if err != nil || completed.Committed == nil {
		t.Fatalf("approve=%+v err=%v", completed, err)
	}
	return completed
}

func TestCaptureServiceStableIdempotencyStateReviewAndBoundedDTO(t *testing.T) {
	now := time.Now().UTC()
	long := strings.Repeat("记忆", 80)
	draft := captureDraft(long, "tea")
	service, _ := newCaptureServiceForTest(t, now, draft)
	owner := workflow.WorkflowOwner{TenantID: 9, OwnerID: 7}
	first, err := service.Start(context.Background(), StartCaptureInput{Owner: owner, Query: "请记住", IdempotencyKey: "stable-capture", Intent: memory.IntentUserStatement})
	if err != nil || first.Status != string(workflow.RunSuspended) || first.Review == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := service.Start(context.Background(), StartCaptureInput{Owner: owner, Query: "请记住", IdempotencyKey: "stable-capture", Intent: memory.IntentUserStatement})
	if err != nil || second.RunID != first.RunID || second.Status != first.Status {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if len([]rune(first.Draft.CanonicalText)) != 64 {
		t.Fatalf("bounded draft chars=%d", len([]rune(first.Draft.CanonicalText)))
	}
	state, err := service.Get(context.Background(), owner, workflow.WorkflowRunID(first.RunID))
	if err != nil || state.RunID != first.RunID {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	review, err := service.GetReview(context.Background(), owner, workflow.WorkflowRunID(first.RunID))
	if err != nil || review.WaitID != first.Review.WaitID || len(review.AllowedActions) != 3 {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	completed, err := service.Resume(context.Background(), ResumeCaptureInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "7"}, RunID: workflow.WorkflowRunID(first.RunID), WaitID: workflow.WaitID(review.WaitID), Version: review.Version, ContentHash: review.ContentHash, Action: workflow.ActionReject})
	if err != nil || completed.Status != string(workflow.RunCompleted) || completed.Committed == nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if _, err := service.Get(context.Background(), workflow.WorkflowOwner{TenantID: 9, OwnerID: 8}, workflow.WorkflowRunID(first.RunID)); !workflow.IsCode(err, workflow.CodeNotFound) {
		t.Fatalf("cross owner err=%v", err)
	}
}
