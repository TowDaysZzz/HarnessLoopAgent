package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

type fakeIntentRouter struct {
	mu       sync.Mutex
	calls    int
	decision routing.RouteDecision
}

func (r *fakeIntentRouter) Route(_ context.Context, _ routing.RouteInput) routing.RouteDecision {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.decision
}

type fakeIntentExecutor struct {
	mu     sync.Mutex
	calls  int
	input  routing.Input
	events []agent.Event
	err    error
}

type cancellingSkillExecutor struct {
	started chan struct{}
	input   routing.Input
}

func (e *cancellingSkillExecutor) Execute(ctx context.Context, input routing.Input) (routing.Execution, error) {
	e.input = input
	out := make(chan agent.Event, 2)
	go func() {
		defer close(out)
		close(e.started)
		<-ctx.Done()
		out <- agent.Event{Type: agent.EventRunFailed, Err: ctx.Err()}
	}()
	return routing.Execution{Handler: "skill:daily_review", Events: out}, nil
}

func (e *fakeIntentExecutor) Execute(_ context.Context, input routing.Input) (routing.Execution, error) {
	e.mu.Lock()
	e.calls++
	e.input = input
	e.mu.Unlock()
	if e.err != nil {
		return routing.Execution{}, e.err
	}
	out := make(chan agent.Event, len(e.events))
	for _, event := range e.events {
		out <- event
	}
	close(out)
	return routing.Execution{Handler: "fake", Events: out}, nil
}

type recordingRunner struct {
	mu       sync.Mutex
	messages [][]agent.Message
	block    <-chan struct{}
}

func (r *recordingRunner) StreamMessages(ctx context.Context, messages []agent.Message) <-chan agent.Event {
	r.mu.Lock()
	r.messages = append(r.messages, append([]agent.Message(nil), messages...))
	r.mu.Unlock()
	out := make(chan agent.Event)
	go func() {
		defer close(out)
		if r.block != nil {
			select {
			case <-r.block:
			case <-ctx.Done():
				out <- agent.Event{Type: agent.EventRunFailed, Err: ctx.Err()}
				return
			}
		}
		out <- agent.Event{Type: agent.EventStatus, Status: "generating"}
		out <- agent.Event{Type: agent.EventTextDelta, Delta: "answer"}
		out <- agent.Event{Type: agent.EventRunCompleted}
	}()
	return out
}

func (r *recordingRunner) lastMessages() []agent.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return nil
	}
	return append([]agent.Message(nil), r.messages[len(r.messages)-1]...)
}

func newTestService(t *testing.T, runner *recordingRunner) (*Service, *MemoryRepository) {
	t.Helper()
	repo := NewMemoryRepository()
	service, err := NewService(context.Background(), repo, runner, contextmanager.NewBoundedAssembler(1000, 2, contextmanager.ApproxTokenCounter{}), ServiceOptions{
		MessageHistoryLimit: 100, DefaultModel: "test-model",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repo
}

func newRoutedTestService(t *testing.T, runner *recordingRunner, router IntentRouter, executor *fakeIntentExecutor, fallback bool) (*Service, *MemoryRepository) {
	t.Helper()
	repo := NewMemoryRepository()
	service, err := NewService(context.Background(), repo, runner, contextmanager.NewBoundedAssembler(1000, 2, contextmanager.ApproxTokenCounter{}), ServiceOptions{
		MessageHistoryLimit: 100, DefaultModel: "test-model", EnableIntentRouting: true,
		EnableLegacyRoutingFallback: fallback, Router: router, Executor: executor,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repo
}

func TestServiceRoutesHistorySummaryUIPhraseToDraftCandidate(t *testing.T) {
	executor := &fakeIntentExecutor{events: []agent.Event{
		{Type: agent.EventDraftCandidate, Delta: `{"id":"draft-ui","title":"对话摘要","content":"摘要正文","content_hash":"hash-ui","expires_at":"2026-08-22T10:00:00Z"}`},
		{Type: agent.EventTextDelta, Delta: "我整理了一条待确认笔记。"},
		{Type: agent.EventRunCompleted},
	}}
	service, _ := newRoutedTestService(t, &recordingRunner{}, routing.Router{Classifier: routing.Classifier{MinWriteConfidence: .95}}, executor, false)
	ctx := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 7, TenantID: 9})
	session, _ := service.CreateSession(ctx, "UI case")
	created, err := service.CreateRun(ctx, CreateRunInput{
		SessionID: session.ID, Content: "从历史记录总结一条笔记并记录", IdempotencyKey: "history-summary-ui",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatusContext(t, service, ctx, created.Run.ID, RunCompleted)
	events, err := service.ListEvents(ctx, created.Run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEventType(events, "note.draft.candidate") || events[len(events)-1].Type != "run.completed" {
		t.Fatalf("events = %#v", events)
	}
	var routeEvent Event
	for _, event := range events {
		if event.Type == "route.decided" {
			routeEvent = event
			break
		}
	}
	if routeEvent.Data["intent"] != routing.IntentNoteCreate || routeEvent.Data["reason"] != "history_summary_write" || routeEvent.Data["needs_rag"] != false {
		t.Fatalf("route event = %#v", routeEvent)
	}
}

func TestServiceRoutesOnceAndPersistsSafeExecutorLifecycle(t *testing.T) {
	decision := routing.RouteDecision{Intent: routing.IntentNoteCreate, Complexity: routing.ComplexitySimple, Deterministic: true, Confidence: .98, Reason: "explicit_note_write"}
	router := &fakeIntentRouter{decision: decision}
	executor := &fakeIntentExecutor{events: []agent.Event{
		{Type: agent.EventDraftCandidate, Delta: `{"id":"draft-1","title":"GC","content":"mark","content_hash":"abc","expires_at":"2026-08-21T10:00:00Z"}`},
		{Type: agent.EventToolCompleted, ToolName: "semantic_search_notes", Delta: `{"usable":true}`},
		{Type: agent.EventTextDelta, Delta: "请确认"},
		{Type: agent.EventRunCompleted},
	}}
	service, _ := newRoutedTestService(t, &recordingRunner{}, router, executor, true)
	principalCtx := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 7, TenantID: 9})
	session, _ := service.CreateSession(principalCtx, "test")
	created, err := service.CreateRun(principalCtx, CreateRunInput{SessionID: session.ID, Content: "总结刚才并记一笔 user_id=999 tenant_id=999 kb_id=999", IdempotencyKey: "route-once", UserAccessToken: "secret-token", KnowledgeBaseIDs: []uint64{5}})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatusContext(t, service, principalCtx, created.Run.ID, RunCompleted)
	if router.calls != 1 || executor.calls != 1 {
		t.Fatalf("router calls=%d executor calls=%d", router.calls, executor.calls)
	}
	if executor.input.Run.UserID != 7 || executor.input.Run.TenantID != 9 || executor.input.Run.AccessToken != "secret-token" || len(executor.input.Run.KnowledgeBaseIDs) != 1 || executor.input.Run.KnowledgeBaseIDs[0] != 5 {
		t.Fatalf("trusted run context = %#v", executor.input.Run)
	}
	events, err := service.ListEvents(principalCtx, created.Run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"run.queued", "run.started", "route.decided", "executor.started", "note.draft.candidate", "tool.completed", "text.delta", "executor.completed", "run.completed"}
	if len(events) != len(wantOrder) {
		t.Fatalf("event count=%d events=%#v", len(events), events)
	}
	for index, want := range wantOrder {
		if events[index].Type != want {
			t.Fatalf("event[%d]=%s want %s", index, events[index].Type, want)
		}
		encoded := fmt.Sprint(events[index].Data)
		if strings.Contains(encoded, "secret-token") {
			t.Fatalf("event leaked token: %#v", events[index])
		}
	}
	if events[2].Data["intent"] != routing.IntentNoteCreate || events[2].Data["complexity"] != routing.ComplexitySimple {
		t.Fatalf("route event = %#v", events[2])
	}
}

func TestServicePersistsSkillInvocationAndTerminalStatus(t *testing.T) {
	args, hash, err := skill.NormalizeArguments([]byte(`{"date":"2026-08-24","timezone":"Asia/Shanghai"}`), 4096)
	if err != nil {
		t.Fatal(err)
	}
	decision := routing.RouteDecision{Target: routing.TargetSkill, Intent: routing.IntentSkillInvoke, Complexity: routing.ComplexitySimple, Deterministic: true, NeedsModel: true, Confidence: .99, Reason: "daily_review", Skill: &routing.SkillTarget{ID: "daily_review", Version: "v1", Arguments: args, ArgumentsHash: hash}}
	executor := &fakeIntentExecutor{events: []agent.Event{{Type: skill.EventStarted, Delta: "daily_review"}, {Type: skill.EventCache, Delta: "miss"}, {Type: skill.EventStep, Delta: "resolve_window"}, {Type: agent.EventTextDelta, Delta: "今日回顾"}, {Type: agent.EventRunCompleted}}}
	service, repo := newRoutedTestService(t, &recordingRunner{}, &fakeIntentRouter{decision: decision}, executor, false)
	ctx := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 7, TenantID: 9})
	session, _ := service.CreateSession(ctx, "skill")
	created, err := service.CreateRun(ctx, CreateRunInput{SessionID: session.ID, Content: "回顾今天", IdempotencyKey: "daily-review"})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatusContext(t, service, ctx, created.Run.ID, RunCompleted)
	if executor.input.Run.SkillInvocation == nil {
		t.Fatal("missing skill invocation in trusted run context")
	}
	stored, err := repo.GetInvocation(context.Background(), skill.Owner{TenantID: 9, UserID: 7}, executor.input.Run.SkillInvocation.ID)
	if err != nil || stored.Status != skill.InvocationCompleted {
		t.Fatalf("invocation=%#v err=%v", stored, err)
	}
	events, _ := service.ListEvents(ctx, created.Run.ID, 0, 100)
	if !hasEventType(events, "skill.started") || !hasEventType(events, "skill.cache") || !hasEventType(events, "skill.step") || events[2].Data["arguments_hash"] != hash {
		t.Fatalf("events=%#v", events)
	}
	if encoded := fmt.Sprint(events); strings.Contains(encoded, "Asia/Shanghai") {
		t.Fatalf("route events leaked arguments: %s", encoded)
	}
}

func TestServiceMarksSkillInvocationFailedWithChatRun(t *testing.T) {
	args, hash, err := skill.NormalizeArguments([]byte(`{"date":"2026-08-24"}`), 4096)
	if err != nil {
		t.Fatal(err)
	}
	decision := routing.RouteDecision{Target: routing.TargetSkill, Intent: routing.IntentSkillInvoke, Complexity: routing.ComplexitySimple, Confidence: .99, Reason: "daily_review", Skill: &routing.SkillTarget{ID: "daily_review", Version: "v1", Arguments: args, ArgumentsHash: hash}}
	executor := &fakeIntentExecutor{events: []agent.Event{{Type: skill.EventStarted, Delta: "daily_review"}, {Type: agent.EventRunFailed, Err: errors.New("generation failed")}}}
	service, repo := newRoutedTestService(t, &recordingRunner{}, &fakeIntentRouter{decision: decision}, executor, false)
	ctx := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 17, TenantID: 19})
	session, _ := service.CreateSession(ctx, "skill failure")
	created, err := service.CreateRun(ctx, CreateRunInput{SessionID: session.ID, Content: "回顾今天", IdempotencyKey: "daily-review-failure"})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatusContext(t, service, ctx, created.Run.ID, RunFailed)
	if executor.input.Run.SkillInvocation == nil {
		t.Fatal("missing skill invocation")
	}
	stored, err := repo.GetInvocation(ctx, skill.Owner{TenantID: 19, UserID: 17}, executor.input.Run.SkillInvocation.ID)
	if err != nil || stored.Status != skill.InvocationFailed {
		t.Fatalf("invocation=%#v err=%v", stored, err)
	}
}

func TestServiceCancellationMarksSkillInvocationCancelled(t *testing.T) {
	args, hash, _ := skill.NormalizeArguments([]byte(`{"date":"2026-08-24"}`), 4096)
	decision := routing.RouteDecision{Target: routing.TargetSkill, Intent: routing.IntentSkillInvoke, Skill: &routing.SkillTarget{ID: "daily_review", Version: "v1", Arguments: args, ArgumentsHash: hash}}
	repo := NewMemoryRepository()
	executor := &cancellingSkillExecutor{started: make(chan struct{})}
	service, err := NewService(context.Background(), repo, &recordingRunner{}, contextmanager.NewBoundedAssembler(1000, 2, contextmanager.ApproxTokenCounter{}), ServiceOptions{MessageHistoryLimit: 100, DefaultModel: "test", EnableIntentRouting: true, Router: &fakeIntentRouter{decision: decision}, Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentauth.WithPrincipal(context.Background(), agentauth.Principal{TenantID: 31, UserID: 32})
	session, _ := service.CreateSession(ctx, "cancel skill")
	created, err := service.CreateRun(ctx, CreateRunInput{SessionID: session.ID, Content: "回顾今天", IdempotencyKey: "cancel-skill"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("skill did not start")
	}
	if _, err := service.CancelRun(ctx, created.Run.ID); err != nil {
		t.Fatal(err)
	}
	waitForStatusContext(t, service, ctx, created.Run.ID, RunCancelled)
	if executor.input.Run.SkillInvocation == nil {
		t.Fatal("missing invocation")
	}
	var stored skill.Invocation
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stored, _ = repo.GetInvocation(ctx, skill.Owner{TenantID: 31, UserID: 32}, executor.input.Run.SkillInvocation.ID)
		if stored.Status == skill.InvocationCancelled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if stored.Status != skill.InvocationCancelled {
		t.Fatalf("invocation=%#v", stored)
	}
}

func TestServiceDoesNotPersistInvocationForBuiltinChat(t *testing.T) {
	executor := &fakeIntentExecutor{events: []agent.Event{{Type: agent.EventTextDelta, Delta: "hello"}, {Type: agent.EventRunCompleted}}}
	service, repo := newRoutedTestService(t, &recordingRunner{}, &fakeIntentRouter{decision: routing.RouteDecision{Target: routing.TargetBuiltin, Intent: routing.IntentChat, Complexity: routing.ComplexitySimple}}, executor, false)
	session, _ := service.CreateSession(context.Background(), "chat")
	created, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "builtin"})
	waitForStatus(t, service, created.Run.ID, RunCompleted)
	repo.mu.Lock()
	count := len(repo.invocations)
	repo.mu.Unlock()
	if count != 0 {
		t.Fatalf("builtin invocation count=%d", count)
	}
}

func TestServiceLegacyFallbackOnlyForReadOnlyIntents(t *testing.T) {
	for _, test := range []struct {
		name         string
		intent       routing.DomainIntent
		wantStatus   RunStatus
		wantFallback bool
	}{
		{name: "read chat", intent: routing.IntentChat, wantStatus: RunCompleted, wantFallback: true},
		{name: "read query", intent: routing.IntentNoteQuery, wantStatus: RunCompleted, wantFallback: true},
		{name: "write", intent: routing.IntentNoteCreate, wantStatus: RunFailed, wantFallback: false},
		{name: "delete", intent: routing.IntentNoteDelete, wantStatus: RunFailed, wantFallback: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{}
			router := &fakeIntentRouter{decision: routing.RouteDecision{Intent: test.intent, Complexity: routing.ComplexitySimple}}
			executor := &fakeIntentExecutor{err: routing.ErrHandlerUnavailable}
			service, _ := newRoutedTestService(t, runner, router, executor, true)
			session, _ := service.CreateSession(context.Background(), "test")
			created, err := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "request", IdempotencyKey: test.name})
			if err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, service, created.Run.ID, test.wantStatus)
			if got := len(runner.lastMessages()) > 0; got != test.wantFallback {
				t.Fatalf("legacy fallback=%v want %v", got, test.wantFallback)
			}
		})
	}
}

func TestServicePersistsExecutorTimeout(t *testing.T) {
	router := &fakeIntentRouter{decision: routing.RouteDecision{Intent: routing.IntentChat, Complexity: routing.ComplexityComplex}}
	executor := &fakeIntentExecutor{events: []agent.Event{{Type: agent.EventRunFailed, Err: context.DeadlineExceeded}}}
	service, _ := newRoutedTestService(t, &recordingRunner{}, router, executor, false)
	session, _ := service.CreateSession(context.Background(), "test")
	created, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "complex", IdempotencyKey: "timeout"})
	waitForStatus(t, service, created.Run.ID, RunTimedOut)
	events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
	if events[len(events)-2].Type != "executor.failed" || events[len(events)-2].Data["error_code"] != "run_timeout" || events[len(events)-1].Type != "run.timed_out" {
		t.Fatalf("timeout events = %#v", events)
	}
}

func TestServicePersistsExecutorFailureWhenStreamCloses(t *testing.T) {
	router := &fakeIntentRouter{decision: routing.RouteDecision{Intent: routing.IntentChat, Complexity: routing.ComplexitySimple}}
	executor := &fakeIntentExecutor{}
	service, _ := newRoutedTestService(t, &recordingRunner{}, router, executor, false)
	session, _ := service.CreateSession(context.Background(), "test")
	created, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "closed"})
	waitForStatus(t, service, created.Run.ID, RunFailed)
	events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
	if events[len(events)-2].Type != "executor.failed" || events[len(events)-2].Data["error_code"] != "stream_closed" || events[len(events)-1].Type != "run.failed" {
		t.Fatalf("closed stream events = %#v", events)
	}
}

func TestServiceRoutedCancellationKeepsTerminalEvent(t *testing.T) {
	block := make(chan struct{})
	runner := &recordingRunner{block: block}
	facade, _ := routing.NewFacade(routing.HandlerSet{SimpleChat: routing.ConversationHandler{Runner: runner}})
	router := &fakeIntentRouter{decision: routing.RouteDecision{Intent: routing.IntentChat, Complexity: routing.ComplexitySimple}}
	repo := NewMemoryRepository()
	service, err := NewService(context.Background(), repo, runner, contextmanager.NewBoundedAssembler(1000, 2, contextmanager.ApproxTokenCounter{}), ServiceOptions{
		MessageHistoryLimit: 100, DefaultModel: "test-model", EnableIntentRouting: true,
		Router: router, Executor: facade,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := service.CreateSession(context.Background(), "test")
	created, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "wait", IdempotencyKey: "cancel-routed"})
	waitForStatus(t, service, created.Run.ID, RunRunning)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
		if hasEventType(events, "executor.started") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := service.CancelRun(context.Background(), created.Run.ID); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, service, created.Run.ID, RunCancelled)
	events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
	if !hasEventType(events, "route.decided") || !hasEventType(events, "executor.started") || events[len(events)-1].Type != "run.cancelled" {
		t.Fatalf("cancel events = %#v", events)
	}
}

func hasEventType(events []Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func TestServicePersistsRunEventsAndConversationHistory(t *testing.T) {
	runner := &recordingRunner{}
	service, _ := newTestService(t, runner)
	session, err := service.CreateSession(context.Background(), "test")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	first, err := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "first question", IdempotencyKey: "first"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	waitForStatus(t, service, first.Run.ID, RunCompleted)

	second, err := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "second question", IdempotencyKey: "second"})
	if err != nil {
		t.Fatalf("CreateRun(second) error = %v", err)
	}
	waitForStatus(t, service, second.Run.ID, RunCompleted)
	messages := runner.lastMessages()
	if len(messages) != 3 || messages[0].Content != "first question" || messages[1].Content != "answer" || messages[2].Content != "second question" {
		t.Fatalf("runner history = %#v", messages)
	}

	events, err := service.ListEvents(context.Background(), second.Run.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) < 5 || events[len(events)-1].Type != string(agent.EventRunCompleted) {
		t.Fatalf("events = %#v", events)
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) {
			t.Fatalf("event sequence at %d = %d", i, event.Sequence)
		}
	}
}

func TestServiceIdempotencyDoesNotExecuteTwice(t *testing.T) {
	runner := &recordingRunner{}
	service, _ := newTestService(t, runner)
	session, _ := service.CreateSession(context.Background(), "test")
	input := CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "same-key"}
	first, err := service.CreateRun(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	waitForStatus(t, service, first.Run.ID, RunCompleted)
	replayed, err := service.CreateRun(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateRun(replay) error = %v", err)
	}
	if replayed.Created || replayed.Run.ID != first.Run.ID {
		t.Fatalf("replayed = %#v", replayed)
	}
}

func TestServiceRejectsConcurrentRunAndSupportsCancel(t *testing.T) {
	block := make(chan struct{})
	runner := &recordingRunner{block: block}
	service, _ := newTestService(t, runner)
	session, _ := service.CreateSession(context.Background(), "test")
	created, err := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "wait", IdempotencyKey: "one"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	waitForStatus(t, service, created.Run.ID, RunRunning)
	_, err = service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "conflict", IdempotencyKey: "two"})
	if !errors.Is(err, ErrActiveRun) {
		t.Fatalf("concurrent CreateRun() error = %v", err)
	}
	cancelled, err := service.CancelRun(context.Background(), created.Run.ID)
	if err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	if cancelled.Status != RunCancelled {
		t.Fatalf("cancelled status = %s", cancelled.Status)
	}
}

func TestNotifierUnsubscribeRemovesWaiter(t *testing.T) {
	notifier := NewNotifier()
	_, unsubscribe := notifier.Subscribe("run-1")
	unsubscribe()
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.waiters) != 0 {
		t.Fatalf("waiters = %#v", notifier.waiters)
	}
}

func TestServiceListsOnlyCurrentUsersSessions(t *testing.T) {
	service, _ := newTestService(t, &recordingRunner{})
	first := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 1, TenantID: 10})
	second := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 2, TenantID: 10})
	firstSession, err := service.CreateSession(first, "first user's notes")
	if err != nil {
		t.Fatalf("CreateSession(first) error = %v", err)
	}
	if _, err := service.CreateSession(second, "second user's notes"); err != nil {
		t.Fatalf("CreateSession(second) error = %v", err)
	}
	sessions, err := service.ListSessions(first, 50)
	if err != nil || len(sessions) != 1 || sessions[0].ID != firstSession.ID {
		t.Fatalf("ListSessions(first) = %#v, %v", sessions, err)
	}
	if _, err := service.GetSession(second, firstSession.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession(cross-user) error = %v, want ErrNotFound", err)
	}
	if _, err := service.ListMessages(second, firstSession.ID, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListMessages(cross-user) error = %v, want ErrNotFound", err)
	}
}

func waitForStatus(t *testing.T, service *Service, runID string, want RunStatus) {
	waitForStatusContext(t, service, context.Background(), runID, want)
}

func waitForStatusContext(t *testing.T, service *Service, ctx context.Context, runID string, want RunStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := service.GetRun(ctx, runID)
		if err == nil && run.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	run, _ := service.GetRun(ctx, runID)
	t.Fatalf("run status = %s, want %s", run.Status, want)
}
