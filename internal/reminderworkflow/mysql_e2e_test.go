package reminderworkflow_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderdelivery"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type mysqlReminderPlanner struct{ now time.Time }

func (p mysqlReminderPlanner) Plan(_ context.Context, input string, _ time.Time) (reminder.CommandPlan, error) {
	trigger := &reminder.Trigger{Type: "at_time", At: p.now.Add(time.Minute).In(time.FixedZone("+08", 8*60*60)).Format(time.RFC3339), Timezone: reminder.DefaultTimezone}
	switch {
	case strings.Contains(input, "创建"):
		return reminder.CommandPlan{Version: "v1", Action: reminder.ActionCreate, Content: "提交周报", Trigger: trigger, MemorySelectors: []reminder.MemorySelector{{Type: memory.SelectorSlot, Namespace: "preferences", SlotKey: "weekly_report_format"}}, Confidence: 1}, nil
	case strings.Contains(input, "修改"):
		trigger.At = p.now.Add(2 * time.Minute).In(time.FixedZone("+08", 8*60*60)).Format(time.RFC3339)
		return reminder.CommandPlan{Version: "v1", Action: reminder.ActionUpdate, Content: "提交精简周报", Trigger: trigger, Target: &reminder.TargetSelector{Statuses: []reminder.Status{reminder.StatusScheduled}}, Confidence: 1}, nil
	case strings.Contains(input, "取消"):
		return reminder.CommandPlan{Version: "v1", Action: reminder.ActionCancel, Target: &reminder.TargetSelector{Statuses: []reminder.Status{reminder.StatusScheduled}}, Confidence: 1}, nil
	case strings.Contains(input, "查询"):
		return reminder.CommandPlan{Version: "v1", Action: reminder.ActionQuery, Target: &reminder.TargetSelector{Statuses: []reminder.Status{reminder.StatusScheduled}}, Confidence: 1}, nil
	default:
		return reminder.CommandPlan{}, reminder.ErrInvalidInput
	}
}

func TestMySQLReminderNaturalLanguageReviewMemoryQueryMutationRestartDeliveryAndIsolation(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run Reminder E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 8, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ProjectionVersion: "reminder-e2e-v1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := workflow.WorkflowOwner{TenantID: uint64(now.UnixNano()%800000000) + 3000000000, OwnerID: uint64(now.UnixNano()%700000000) + 4000000000}
	memoryOwner := memory.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID}
	canonical, structured, hash, err := memory.NormalizeContent("周报使用简洁格式", memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"value": "concise"}})
	if err != nil {
		t.Fatal(err)
	}
	memoryValue := memory.Record{ID: uuid.NewString(), Owner: memoryOwner, Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "preferences", SlotKey: "weekly_report_format", LineageID: uuid.NewString(), LineageVersion: 1, RowVersion: 1, CanonicalText: canonical, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserConfirmed, Confidence: 1, Salience: .8, Source: memory.SourceRef{Type: "workflow", ID: "reminder-e2e"}, Status: memory.StatusActive, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CommitMutation(ctx, memory.Mutation{Owner: memoryOwner, NewMemory: &memoryValue, IdempotencyKey: uuid.NewString(), InputHash: hash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}

	service := newMySQLReminderService(t, store, mysqlReminderPlanner{now}, now)
	started, err := service.Start(ctx, reminderworkflow.StartInput{Owner: owner, Query: "创建：提醒我一分钟后提交周报", IdempotencyKey: "create-" + uuid.NewString()})
	if err != nil || started.Review == nil || len(started.Review.MemorySummary) != 1 {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	// Rebuild the service over the same MySQL durable store before approval.
	service = newMySQLReminderService(t, store, mysqlReminderPlanner{now}, now)
	created := approveMySQLReminder(t, ctx, service, owner, started)
	if created.Committed == nil || len(created.Committed.MemoryRefs) != 1 {
		t.Fatalf("created=%+v", created)
	}
	if _, err := store.Get(ctx, reminder.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID + 1}, created.Committed.ID); err != reminder.ErrNotFound {
		t.Fatalf("cross owner err=%v", err)
	}

	query := reminderworkflow.QueryService{Planner: mysqlReminderPlanner{now}, Repository: store, Now: func() time.Time { return now }}
	listed, err := query.Query(ctx, reminder.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID}, "查询我的提醒", 10)
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	target := &reminder.ReminderRef{ID: created.Committed.ID, RowVersion: created.Committed.RowVersion}
	updatedStart, err := service.Start(ctx, reminderworkflow.StartInput{Owner: owner, Query: "修改提醒到两分钟后", IdempotencyKey: "update-" + uuid.NewString(), TrustedTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	updated := approveMySQLReminder(t, ctx, service, owner, updatedStart)
	if updated.Committed.Content != "提交精简周报" {
		t.Fatalf("updated=%+v", updated)
	}

	dueAt := updated.Committed.NextFireAt
	dispatcher, _ := reminderdelivery.NewDispatcher(store, reminderdelivery.DispatcherConfig{BatchSize: 10, MaxBatches: 2, LeaseDuration: time.Second, Interval: time.Second, Now: func() time.Time { return dueAt }, NewToken: func() string { return "e2e-dispatch-" + uuid.NewString() }})
	if count, err := dispatcher.Tick(ctx); err != nil || count != 1 {
		t.Fatalf("dispatch count=%d err=%v", count, err)
	}
	adapter := reminderdelivery.NewRecordingAdapter()
	worker, _ := reminderdelivery.NewWorker(store, adapter, reminderdelivery.WorkerConfig{BatchSize: 10, MaxBatches: 2, MaxAttempts: 3, LeaseDuration: time.Second, Interval: time.Second, BaseBackoff: time.Second, MaxBackoff: time.Minute, Now: func() time.Time { return dueAt }, NewToken: func() string { return "e2e-worker-" + uuid.NewString() }})
	if count, err := worker.Tick(ctx); err != nil || count != 1 {
		t.Fatalf("worker count=%d err=%v", count, err)
	}
	fired, err := store.Get(ctx, reminder.Owner{TenantID: owner.TenantID, UserID: owner.OwnerID}, updated.Committed.ID)
	if err != nil || fired.Status != reminder.StatusFired {
		t.Fatalf("fired=%+v err=%v", fired, err)
	}

	secondStart, err := service.Start(ctx, reminderworkflow.StartInput{Owner: owner, Query: "创建第二条提醒", IdempotencyKey: "second-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	second := approveMySQLReminder(t, ctx, service, owner, secondStart)
	cancelStart, err := service.Start(ctx, reminderworkflow.StartInput{Owner: owner, Query: "取消第二条提醒", IdempotencyKey: "cancel-" + uuid.NewString(), TrustedTarget: &reminder.ReminderRef{ID: second.Committed.ID, RowVersion: second.Committed.RowVersion}})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := approveMySQLReminder(t, ctx, service, owner, cancelStart)
	if cancelled.Committed.Status != reminder.StatusCancelled {
		t.Fatalf("cancelled=%+v", cancelled)
	}
}

func newMySQLReminderService(t *testing.T, store *mysqlstore.Store, planner mysqlReminderPlanner, now time.Time) *reminderworkflow.Service {
	t.Helper()
	recall, err := memory.NewRecallService(store, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 1, MaxTarget: 8, PageSize: 10, MaxScanned: 20, MaxBatches: 2, MaxDuration: time.Second, MaxContextChars: 2000, PlanMinConfidence: .75, MaxExactCandidates: 10})
	if err != nil {
		t.Fatal(err)
	}
	edits := &reminderworkflow.EditPayloadService{Store: store, TTL: time.Hour, Now: func() time.Time { return now }}
	evaluator := &reminderworkflow.Evaluator{Planner: planner, Recall: recall, Repository: store, MaxHorizon: 24 * time.Hour}
	nodes := reminderworkflow.NewNodes(evaluator, reminderworkflow.ReviewNode{TTL: time.Hour, Now: func() time.Time { return now }, EditLoader: edits}, reminderworkflow.CommitNode{Repository: store, MemoryRepository: store, Now: func() time.Time { return now }})
	runtime, err := workflow.NewDurableRuntime(store.WorkflowStore(), nodes, "reminder-e2e-v1", reminderworkflow.CommandCodec{}, workflow.DurableRuntimeOptions{LeaseDuration: time.Second, MaxCheckpointBytes: 64 * 1024, Now: func() time.Time { return now }, NewClaimToken: func() string { return uuid.NewString() }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := reminderworkflow.NewService(runtime, edits, reminderworkflow.ServiceConfig{DefinitionVersion: "reminder-e2e-v1", MaxSteps: 40, MaxResumes: 6, RunTTL: time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func approveMySQLReminder(t *testing.T, ctx context.Context, service *reminderworkflow.Service, owner workflow.WorkflowOwner, value reminderworkflow.CommandDTO) reminderworkflow.CommandDTO {
	t.Helper()
	result, err := service.Resume(ctx, reminderworkflow.ResumeInput{Owner: owner, Actor: workflow.ActorRef{Type: "user", ID: fmt.Sprint(owner.OwnerID)}, RunID: workflow.WorkflowRunID(value.RunID), WaitID: workflow.WaitID(value.Review.WaitID), Version: value.Review.Version, ContentHash: value.Review.ContentHash, Action: workflow.ActionApprove})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
