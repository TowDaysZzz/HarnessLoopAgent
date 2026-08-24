package reminderworkflow

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type scriptedPlanner struct {
	plans map[string]reminder.CommandPlan
}

func (p scriptedPlanner) Plan(_ context.Context, input string, _ time.Time) (reminder.CommandPlan, error) {
	plan, ok := p.plans[input]
	if !ok {
		return reminder.CommandPlan{}, fmt.Errorf("missing plan for %q", input)
	}
	return plan, nil
}

func createPlan(content string, at time.Time) reminder.CommandPlan {
	return reminder.CommandPlan{Version: reminder.CommandPlanVersion, Action: reminder.ActionCreate, Content: content, Trigger: &reminder.Trigger{Type: "at_time", At: at.In(time.FixedZone("+08", 8*60*60)).Format(time.RFC3339), Timezone: reminder.DefaultTimezone}, Confidence: 1}
}

func mutationPlan(action reminder.Action, content string, at time.Time) reminder.CommandPlan {
	plan := createPlan(content, at)
	plan.Action = action
	plan.Target = &reminder.TargetSelector{Statuses: []reminder.Status{reminder.StatusScheduled}}
	if action == reminder.ActionCancel {
		plan.Content, plan.Trigger = "", nil
	}
	return plan
}

type serviceHarness struct {
	service *Service
	repo    *reminder.FakeRepository
	store   *workflow.MemoryDurableStore
	edits   *EditPayloadService
	now     time.Time
	planner scriptedPlanner
}

func newServiceHarness(t *testing.T, planner scriptedPlanner) serviceHarness {
	t.Helper()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := reminder.NewFakeRepository()
	store := workflow.NewMemoryDurableStore()
	editStore := NewMemoryEditPayloadStore()
	edits := &EditPayloadService{Store: editStore, TTL: time.Hour, Now: func() time.Time { return now }}
	evaluator := &Evaluator{Planner: planner, Repository: repo, MaxHorizon: 7 * 24 * time.Hour}
	nodes := NewNodes(evaluator, ReviewNode{TTL: time.Hour, Now: func() time.Time { return now }, EditLoader: edits}, CommitNode{Repository: repo, Now: func() time.Time { return now }})
	var token uint64
	runtime, err := workflow.NewDurableRuntime(store, nodes, "v1", CommandCodec{}, workflow.DurableRuntimeOptions{LeaseDuration: time.Second, MaxCheckpointBytes: 64 * 1024, Now: func() time.Time { return now }, NewClaimToken: func() string { return fmt.Sprintf("claim-%d", atomic.AddUint64(&token, 1)) }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(runtime, edits, ServiceConfig{DefinitionVersion: "v1", MaxSteps: 40, MaxResumes: 5, RunTTL: 24 * time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return serviceHarness{service: service, repo: repo, store: store, edits: edits, now: now, planner: planner}
}

func TestCreateReviewEditRerunsPipelineAndUsesStableRunID(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	planner := scriptedPlanner{plans: map[string]reminder.CommandPlan{"create": createPlan("提交周报", now.Add(20*time.Hour)), "edit": createPlan("提交精简周报", now.Add(22*time.Hour))}}
	h := newServiceHarness(t, planner)
	owner := workflow.WorkflowOwner{TenantID: 1, OwnerID: 2}
	started, err := h.service.Start(context.Background(), StartInput{Owner: owner, Query: "create", IdempotencyKey: "request-1"})
	if err != nil || started.Review == nil || started.Status != string(workflow.RunSuspended) {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	replayed, err := h.service.Start(context.Background(), StartInput{Owner: owner, Query: "create", IdempotencyKey: "request-1"})
	if err != nil || replayed.RunID != started.RunID || replayed.Review.WaitID != started.Review.WaitID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	edited, err := h.service.Resume(context.Background(), ResumeInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "2"}, RunID: workflow.WorkflowRunID(started.RunID), WaitID: workflow.WaitID(started.Review.WaitID), Version: started.Review.Version, ContentHash: started.Review.ContentHash, Action: workflow.ActionSubmitEdit, EditText: "edit"})
	if err != nil || edited.Review == nil || edited.Review.Version != 2 || edited.Review.Content != "提交精简周报" || edited.Review.ContentHash == started.Review.ContentHash {
		t.Fatalf("edited=%+v err=%v", edited, err)
	}
	if _, err := h.service.Resume(context.Background(), ResumeInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "2"}, RunID: workflow.WorkflowRunID(edited.RunID), WaitID: workflow.WaitID(edited.Review.WaitID), Version: edited.Review.Version, ContentHash: started.Review.ContentHash, Action: workflow.ActionApprove}); !workflow.IsCode(err, workflow.CodeInvalidResume) {
		t.Fatalf("stale hash err=%v", err)
	}
	completed, err := h.service.Resume(context.Background(), ResumeInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: "2"}, RunID: workflow.WorkflowRunID(edited.RunID), WaitID: workflow.WaitID(edited.Review.WaitID), Version: edited.Review.Version, ContentHash: edited.Review.ContentHash, Action: workflow.ActionApprove})
	if err != nil || completed.Status != string(workflow.RunCompleted) || completed.Committed == nil || completed.Committed.Content != "提交精简周报" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestCreateUpdateCancelRejectAndClarificationReview(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clarify := createPlan("不明确", now.Add(2*time.Hour))
	clarify.Clarification = &reminder.Clarification{Needed: true, Reason: "ambiguous_time", Question: "请确认时间。"}
	planner := scriptedPlanner{plans: map[string]reminder.CommandPlan{
		"create":  createPlan("原提醒", now.Add(4*time.Hour)),
		"update":  mutationPlan(reminder.ActionUpdate, "更新提醒", now.Add(5*time.Hour)),
		"cancel":  mutationPlan(reminder.ActionCancel, "", time.Time{}),
		"clarify": clarify,
	}}
	h := newServiceHarness(t, planner)
	owner := workflow.WorkflowOwner{TenantID: 4, OwnerID: 5}
	created := approveStarted(t, h.service, owner, "create", "create-key")
	if created.Committed == nil {
		t.Fatal("create did not commit")
	}
	target := &reminder.ReminderRef{ID: created.Committed.ID, RowVersion: created.Committed.RowVersion}
	updated := approveStartedTarget(t, h.service, owner, "update", "update-key", target)
	if updated.Committed == nil || updated.Committed.Content != "更新提醒" {
		t.Fatalf("updated=%+v", updated)
	}
	target = &reminder.ReminderRef{ID: updated.Committed.ID, RowVersion: updated.Committed.RowVersion}
	cancelled := approveStartedTarget(t, h.service, owner, "cancel", "cancel-key", target)
	if cancelled.Committed == nil || cancelled.Committed.Status != reminder.StatusCancelled {
		t.Fatalf("cancelled=%+v", cancelled)
	}

	rejectedStart, err := h.service.Start(context.Background(), StartInput{Owner: owner, Query: "create", IdempotencyKey: "reject-key"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := h.service.Resume(context.Background(), resumeFrom(rejectedStart, owner, workflow.ActionReject))
	if err != nil || rejected.Status != string(workflow.RunCompleted) || rejected.Committed != nil {
		t.Fatalf("rejected=%+v err=%v", rejected, err)
	}
	clarified, err := h.service.Start(context.Background(), StartInput{Owner: owner, Query: "clarify", IdempotencyKey: "clarify-key"})
	if err != nil || clarified.Review == nil || clarified.Review.Clarification == nil || containsAction(clarified.Review.AllowedActions, workflow.ActionApprove) {
		t.Fatalf("clarified=%+v err=%v", clarified, err)
	}
}

func approveStarted(t *testing.T, service *Service, owner workflow.WorkflowOwner, query, key string) CommandDTO {
	t.Helper()
	return approveStartedTarget(t, service, owner, query, key, nil)
}
func approveStartedTarget(t *testing.T, service *Service, owner workflow.WorkflowOwner, query, key string, target *reminder.ReminderRef) CommandDTO {
	t.Helper()
	started, err := service.Start(context.Background(), StartInput{Owner: owner, Query: query, IdempotencyKey: key, TrustedTarget: target})
	if err != nil || started.Review == nil {
		t.Fatalf("start %s=%+v err=%v", query, started, err)
	}
	completed, err := service.Resume(context.Background(), resumeFrom(started, owner, workflow.ActionApprove))
	if err != nil {
		t.Fatalf("approve %s err=%v", query, err)
	}
	return completed
}
func resumeFrom(value CommandDTO, owner workflow.WorkflowOwner, action workflow.HumanAction) ResumeInput {
	return ResumeInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: fmt.Sprint(owner.OwnerID)}, RunID: workflow.WorkflowRunID(value.RunID), WaitID: workflow.WaitID(value.Review.WaitID), Version: value.Review.Version, ContentHash: value.Review.ContentHash, Action: action}
}
func containsAction(values []workflow.HumanAction, target workflow.HumanAction) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestServiceRestartConcurrentResumeAndOwnerIsolation(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	planner := scriptedPlanner{plans: map[string]reminder.CommandPlan{"create": createPlan("重启测试", now.Add(time.Hour))}}
	h := newServiceHarness(t, planner)
	owner := workflow.WorkflowOwner{TenantID: 7, OwnerID: 8}
	started, err := h.service.Start(context.Background(), StartInput{Owner: owner, Query: "create", IdempotencyKey: "restart-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Get(context.Background(), workflow.WorkflowOwner{TenantID: 7, OwnerID: 9}, workflow.WorkflowRunID(started.RunID)); !workflow.IsCode(err, workflow.CodeNotFound) {
		t.Fatalf("cross-owner get err=%v", err)
	}

	// Reconstruct the runtime and service over the same durable store to model a restart.
	evaluator := &Evaluator{Planner: planner, Repository: h.repo, MaxHorizon: 7 * 24 * time.Hour}
	nodes := NewNodes(evaluator, ReviewNode{TTL: time.Hour, Now: func() time.Time { return h.now }, EditLoader: h.edits}, CommitNode{Repository: h.repo, Now: func() time.Time { return h.now }})
	var sequence uint64
	runtime, err := workflow.NewDurableRuntime(h.store, nodes, "v1", CommandCodec{}, workflow.DurableRuntimeOptions{LeaseDuration: time.Second, MaxCheckpointBytes: 64 * 1024, Now: func() time.Time { return h.now }, NewClaimToken: func() string { return fmt.Sprintf("restart-%d", atomic.AddUint64(&sequence, 1)) }})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(runtime, h.edits, ServiceConfig{DefinitionVersion: "v1", MaxSteps: 40, MaxResumes: 5, RunTTL: time.Hour, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	command := resumeFrom(started, owner, workflow.ActionApprove)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, resumeErr := restarted.Resume(context.Background(), command); results <- resumeErr }()
	}
	first, second := <-results, <-results
	if first != nil && second != nil || first == nil && second == nil {
		t.Fatalf("expected one successful resume: first=%v second=%v", first, second)
	}
	failure := first
	if failure == nil {
		failure = second
	}
	if !(workflow.IsCode(failure, workflow.CodeStateConflict) || workflow.IsCode(failure, workflow.CodeClaimConflict)) {
		t.Fatalf("concurrent error=%v", failure)
	}
	if _, err := restarted.Resume(context.Background(), ResumeInput{Owner: workflow.WorkflowOwner{TenantID: 7, OwnerID: 9}, Actor: workflow.ActorRef{Type: "user", ID: "9"}, RunID: command.RunID, WaitID: command.WaitID, Version: command.Version, ContentHash: command.ContentHash, Action: command.Action}); !workflow.IsCode(err, workflow.CodeNotFound) {
		t.Fatalf("cross-owner resume err=%v", err)
	}
	if errors.Is(first, reminder.ErrInvalidInput) || errors.Is(second, reminder.ErrInvalidInput) {
		t.Fatalf("unexpected domain failure: %v %v", first, second)
	}
}

type failSecondCommitStore struct {
	workflow.DurableStore
	calls atomic.Int32
}

func (s *failSecondCommitStore) CommitExecution(ctx context.Context, request workflow.CommitExecutionRequest) error {
	if s.calls.Add(1) == 2 {
		return errors.New("simulated checkpoint failure")
	}
	return s.DurableStore.CommitExecution(ctx, request)
}

func TestCommitSucceededCheckpointFailedReplaysIdempotently(t *testing.T) {
	clock := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	planner := scriptedPlanner{plans: map[string]reminder.CommandPlan{"create": createPlan("幂等提交", clock.Add(time.Hour))}}
	repo := reminder.NewFakeRepository()
	base := workflow.NewMemoryDurableStore()
	store := &failSecondCommitStore{DurableStore: base}
	evaluator := &Evaluator{Planner: planner, Repository: repo, MaxHorizon: 24 * time.Hour}
	nodes := NewNodes(evaluator, ReviewNode{TTL: time.Hour, Now: func() time.Time { return clock }}, CommitNode{Repository: repo, Now: func() time.Time { return clock }})
	var token atomic.Uint64
	runtime, err := workflow.NewDurableRuntime(store, nodes, "v1", CommandCodec{}, workflow.DurableRuntimeOptions{LeaseDuration: time.Second, MaxCheckpointBytes: 64 * 1024, Now: func() time.Time { return clock }, NewClaimToken: func() string { return fmt.Sprintf("retry-%d", token.Add(1)) }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(runtime, nil, ServiceConfig{DefinitionVersion: "v1", MaxSteps: 30, MaxResumes: 3, RunTTL: time.Hour, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	owner := workflow.WorkflowOwner{TenantID: 11, OwnerID: 12}
	started, err := service.Start(context.Background(), StartInput{Owner: owner, Query: "create", IdempotencyKey: "checkpoint-retry"})
	if err != nil {
		t.Fatal(err)
	}
	command := resumeFrom(started, owner, workflow.ActionApprove)
	if _, err := service.Resume(context.Background(), command); err == nil || err.Error() != "simulated checkpoint failure" {
		t.Fatalf("first resume err=%v", err)
	}
	page, err := repo.List(context.Background(), reminder.Query{Owner: reminder.Owner{TenantID: 11, UserID: 12}, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("after failed checkpoint items=%d err=%v", len(page.Items), err)
	}
	clock = clock.Add(2 * time.Second)
	completed, err := service.Resume(context.Background(), command)
	if err != nil || completed.Committed == nil || completed.Committed.ID != page.Items[0].ID {
		t.Fatalf("replay=%+v err=%v", completed, err)
	}
	page, err = repo.List(context.Background(), reminder.Query{Owner: reminder.Owner{TenantID: 11, UserID: 12}, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("duplicate reminders=%d err=%v", len(page.Items), err)
	}
}
