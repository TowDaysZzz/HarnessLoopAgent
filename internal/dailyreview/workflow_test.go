package dailyreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type memoryContextStub struct {
	mu                 sync.Mutex
	recalls, validates int
	context            MemoryContext
	stale              bool
}

func (m *memoryContextStub) Recall(context.Context, skill.Owner, Window) (MemoryContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recalls++
	return m.context, nil
}
func (m *memoryContextStub) ValidatePinned(context.Context, skill.Owner, []MemoryRef, time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.validates++
	if m.stale {
		return ErrStaleSnapshot
	}
	return nil
}

type harnessCollector struct {
	mu     sync.Mutex
	events []runtime.Event
}

func (h *harnessCollector) Observe(_ context.Context, event runtime.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
}

func workflowFixture(t *testing.T) (ReviewWorkflow, *activitySourceStub, *memoryContextStub, *reportRunner, skill.Request, *workflow.MemoryCollector, *harnessCollector) {
	t.Helper()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	window, _ := ResolveWindow("2026-08-24", "Asia/Shanghai")
	at := window.Start.Add(time.Hour)
	body := "完成任务"
	source := &activitySourceStub{version: 1, chatBodies: map[string]string{"chat-1": body}, noteBodies: map[string]string{}, chat: []ChatRef{{ID: "chat-1", SessionID: "session-1", RunID: "run-source", Role: "user", Sequence: 1, ContentHash: ContentHash(body), CreatedAt: at}}}
	report := EmptyReport(window, nil)
	report.Highlights = []Fact{{Text: body, Evidence: []EvidenceRef{{Type: "chat", ID: "chat-1", Version: 1, Hash: ContentHash(body)}}}}
	raw, _ := json.Marshal(report)
	runner := &reportRunner{outputs: []string{string(raw)}}
	mem := &memoryContextStub{context: MemoryContext{Bodies: map[string]string{}}}
	collector := workflow.NewMemoryCollector()
	harness := &harnessCollector{}
	review := ReviewWorkflow{Reader: ActivityReader{Chat: source, Notes: source, Memory: source}, Cache: NewMemoryCache(), Memory: mem, Generator: StructuredGenerator{Runner: runner, MaxRepairs: 1, MaxOutputBytes: 64 * 1024, Timeout: time.Second}, Config: WorkflowConfig{Options: Options{MaxChatMessages: 20, PerSession: 10, MaxNotes: 10, IncludeMemory: true}, CacheTTL: time.Hour, CacheLease: time.Minute, CacheWait: time.Second, MaxSteps: 9, MaxModelCalls: 1, MaxToolCalls: 2, OutputSchemaVersion: "v1", PromptPolicyVersion: "v1"}, Observer: collector, Harness: harness, Now: func() time.Time { return now }}
	invocation, err := skill.NewInvocation("invocation-1", skill.Owner{TenantID: 1, UserID: 2}, "session-1", "chat-run-1", skill.Ref{ID: "daily_review", Version: "v1"}, []byte(`{"date":"2026-08-24","timezone":"Asia/Shanghai"}`), now)
	if err != nil {
		t.Fatal(err)
	}
	return review, source, mem, runner, skill.Request{Invocation: invocation, Content: "回顾今天"}, collector, harness
}

func TestReviewWorkflowNodeOrderCacheMissThenNoOpHit(t *testing.T) {
	review, source, mem, runner, request, collector, harness := workflowFixture(t)
	first, err := review.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheState != "miss" || first.Text == "" || runner.calls != 1 || source.loads != 2 || mem.recalls != 1 {
		t.Fatalf("first=%#v calls=%d loads=%d recalls=%d", first, runner.calls, source.loads, mem.recalls)
	}
	events := collector.Events()
	want := []workflow.NodeID{"resolve_window", "snapshot_sources", "lookup_or_claim_cache", "load_daily_evidence", "recall_memory_context", "generate_structured_review", "validate_evidence", "recheck_snapshot_commit_cache", "render"}
	if len(events) != 18 {
		t.Fatalf("events=%d", len(events))
	}
	for i, id := range want {
		if events[i*2].NodeID != id || events[i*2].Type != workflow.EventNodeStarted || events[i*2+1].Type != workflow.EventNodeCompleted {
			t.Fatalf("events=%#v", events)
		}
	}
	request.Invocation.ID = "invocation-2"
	request.Invocation.ChatRunID = "chat-run-2"
	second, err := review.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheState != "hit" || second.Text != first.Text || runner.calls != 1 || source.loads != 2 || mem.recalls != 1 {
		t.Fatalf("second=%#v calls=%d loads=%d recalls=%d", second, runner.calls, source.loads, mem.recalls)
	}
	if len(harness.events) == 0 || harness.events[0].Fields["skill_invocation_id"] == nil {
		t.Fatalf("harness=%#v", harness.events)
	}
}

func TestReviewWorkflowEmptyActivitySkipsModelRecallAndTools(t *testing.T) {
	review, source, mem, runner, request, _, _ := workflowFixture(t)
	source.chat = nil
	result, err := review.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 0 || mem.recalls != 0 || source.loads != 0 || !strings.Contains(result.Text, "暂无可验证内容") {
		t.Fatalf("result=%#v calls=%d recalls=%d loads=%d", result, runner.calls, mem.recalls, source.loads)
	}
}

func TestReviewWorkflowMemoryMutationVersionInvalidatesReadyCache(t *testing.T) {
	review, source, _, runner, request, _, _ := workflowFixture(t)
	first, err := review.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	source.version++
	runner.outputs = append(runner.outputs, runner.outputs[0])
	request.Invocation.ID = "invocation-memory-change"
	request.Invocation.ChatRunID = "chat-run-memory-change"
	second, err := review.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheState != "miss" || second.CacheState != "miss" || runner.calls != 2 {
		t.Fatalf("first=%s second=%s calls=%d", first.CacheState, second.CacheState, runner.calls)
	}
}

func TestReviewWorkflowReusesCommittedCacheAfterServiceReassembly(t *testing.T) {
	review, _, _, runner, request, _, _ := workflowFixture(t)
	first, err := review.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replacement := &reportRunner{outputs: []string{"must-not-run"}}
	restarted := review
	restarted.Generator.Runner = replacement
	request.Invocation.ID = "invocation-after-restart"
	request.Invocation.ChatRunID = "chat-run-after-restart"
	second, err := restarted.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != second.Text || second.CacheState != "hit" || replacement.calls != 0 || runner.calls != 1 {
		t.Fatalf("second=%#v replacement=%d original=%d", second, replacement.calls, runner.calls)
	}
}

func TestReviewWorkflowToolBudgetTerminatesBeforeRecall(t *testing.T) {
	review, _, mem, _, request, _, _ := workflowFixture(t)
	review.Config.MaxToolCalls = 1
	_, err := review.Run(context.Background(), request)
	if !errors.Is(err, runtime.ErrToolBudgetExceeded) {
		t.Fatalf("error=%v", err)
	}
	if mem.recalls != 0 {
		t.Fatalf("recalls=%d", mem.recalls)
	}
}

func TestReviewWorkflowRepairConsumesModelBudget(t *testing.T) {
	review, _, _, runner, request, _, _ := workflowFixture(t)
	runner.outputs = []string{"not-json", runner.outputs[0]}
	review.Config.MaxModelCalls = 1
	_, err := review.Run(context.Background(), request)
	if !errors.Is(err, runtime.ErrModelBudgetExceeded) {
		t.Fatalf("error=%v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls=%d", runner.calls)
	}
}

type changingSource struct {
	*activitySourceStub
	mu    sync.Mutex
	calls int
}

func (s *changingSource) SnapshotChat(ctx context.Context, o skill.Owner, w Window, l, p int) ([]ChatRef, bool, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	refs, cut, err := s.activitySourceStub.SnapshotChat(ctx, o, w, l, p)
	if len(refs) > 0 {
		refs[0].CreatedAt = refs[0].CreatedAt.Add(time.Duration(n) * time.Microsecond)
	}
	return refs, cut, err
}

func TestReviewWorkflowRetriesSnapshotOnceThenFailsStable(t *testing.T) {
	review, source, _, runner, request, _, _ := workflowFixture(t)
	changing := &changingSource{activitySourceStub: source}
	review.Reader.Chat = changing
	runner.outputs = append(runner.outputs, runner.outputs[0])
	_, err := review.Run(context.Background(), request)
	if !errors.Is(err, ErrSourceChanging) {
		t.Fatalf("error=%v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("model calls=%d", runner.calls)
	}
}

func TestDailyReviewStateSerializationExcludesEvidenceBodies(t *testing.T) {
	state := DailyReviewState{Evidence: Evidence{Chat: map[string]string{"x": "secret-token"}}, MemoryBodies: map[string]string{"m": "secret-memory"}}
	raw, _ := json.Marshal(state)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("state leaked evidence: %s", raw)
	}
}

func TestRecallAdapterPinnedRejectsObsoleteAndCrossScope(t *testing.T) {
	repo := memory.NewFakeRepository()
	service, err := memory.NewRecallService(repo, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 1, MaxTarget: 10, PageSize: 2, MaxScanned: 10, MaxBatches: 2, MaxDuration: time.Second, MaxContextChars: 4096, PlanMinConfidence: .8, MaxExactCandidates: 10})
	if err != nil {
		t.Fatal(err)
	}
	adapter := RecallAdapter{Service: service, Target: 2, MaxContextChars: 4096}
	if err := adapter.ValidatePinned(context.Background(), skill.Owner{TenantID: 1, UserID: 2}, []MemoryRef{{ID: "missing", LineageVersion: 1, ContentHash: ContentHash("x")}}, time.Now()); err == nil {
		t.Fatal("expected stale pinned memory")
	}
}

func TestRecallAdapterLoadsBoundedActiveGoalThroughExistingRecall(t *testing.T) {
	repo := memory.NewFakeRepository()
	service, err := memory.NewRecallService(repo, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 1, MaxTarget: 10, PageSize: 2, MaxScanned: 10, MaxBatches: 2, MaxDuration: time.Second, MaxContextChars: 4096, PlanMinConfidence: .8, MaxExactCandidates: 10})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	owner := memory.Owner{TenantID: 1, UserID: 2}
	text, structured, hash, err := memory.NormalizeContent("完成项目", memory.StructuredValue{Schema: "goal", Version: 1, Data: map[string]any{"goal": "project"}})
	if err != nil {
		t.Fatal(err)
	}
	record := memory.Record{ID: "goal-1", Owner: owner, Layer: memory.LayerLongTerm, Kind: memory.KindGoal, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "goals", SlotKey: "project", LineageID: "line-goal-1", LineageVersion: 1, RowVersion: 1, CanonicalText: text, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserConfirmed, Confidence: 1, Salience: 1, Source: memory.SourceRef{Type: "workflow", ID: "source"}, Status: memory.StatusActive, CreatedAt: now, UpdatedAt: now}
	if _, err := repo.CommitMutation(context.Background(), memory.Mutation{Owner: owner, NewMemory: &record, IdempotencyKey: "goal-create", InputHash: hash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	adapter := RecallAdapter{Service: service, Candidates: repo, Target: 3, MaxContextChars: 4096}
	window := Window{LocalDate: "2026-08-24", Timezone: "Asia/Shanghai", Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
	contextResult, err := adapter.Recall(context.Background(), skill.Owner{TenantID: 1, UserID: 2}, window)
	if err != nil || len(contextResult.Refs) != 1 || contextResult.Bodies["goal-1"] != "完成项目" {
		t.Fatalf("context=%#v err=%v", contextResult, err)
	}
	if _, err := repo.TransitionMemory(context.Background(), owner, record.ID, 1, memory.StatusRevoked, "user", "revoke", "goal-revoke", hash, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidatePinned(context.Background(), skill.Owner{TenantID: 1, UserID: 2}, contextResult.Refs, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected revoked pinned memory rejection")
	}
}
