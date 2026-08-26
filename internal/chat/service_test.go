package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
)

type recordingRunner struct {
	mu       sync.Mutex
	requests []agent.ConversationRequest
	block    <-chan struct{}
	events   []agent.Event
}

type fixedRetrievalDecider RetrievalDecision

func (d fixedRetrievalDecider) Decide([]agent.Message) RetrievalDecision {
	return RetrievalDecision(d)
}

func (r *recordingRunner) StreamConversation(ctx context.Context, request agent.ConversationRequest) <-chan agent.Event {
	r.mu.Lock()
	copyRequest := request
	copyRequest.Messages = append([]agent.Message(nil), request.Messages...)
	r.requests = append(r.requests, copyRequest)
	events := append([]agent.Event(nil), r.events...)
	r.mu.Unlock()
	out := make(chan agent.Event, len(events)+4)
	go func() {
		defer close(out)
		if r.block != nil {
			select {
			case <-ctx.Done():
				out <- agent.Event{Type: agent.EventRunFailed, Err: ctx.Err()}
				return
			case <-r.block:
			}
		}
		if len(events) == 0 {
			events = []agent.Event{
				{Type: agent.EventStatus, Status: "thinking"},
				{Type: agent.EventTextDelta, Delta: "answer"},
				{Type: agent.EventRunCompleted},
			}
		}
		for _, event := range events {
			out <- event
		}
	}()
	return out
}

func (r *recordingRunner) request(index int) agent.ConversationRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[index]
}

func (r *recordingRunner) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func newTestService(t *testing.T, runner *recordingRunner, assembler Assembler) (*Service, *MemoryRepository) {
	t.Helper()
	if assembler == nil {
		assembler = NewBoundedAssembler(1000, 2, nil)
	}
	repo := NewMemoryRepository()
	service, err := NewService(context.Background(), repo, runner, assembler, ServiceOptions{
		MessageHistoryLimit: 100,
		DefaultModel:        "test-model",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repo
}

func TestServiceUsesLinearChatRAGEventFlow(t *testing.T) {
	runner := &recordingRunner{events: []agent.Event{
		{Type: agent.EventStatus, Status: "retrieving"},
		{Type: agent.EventToolCompleted, ToolName: "semantic_search_notes", Delta: "1 result"},
		{Type: agent.EventDraftCandidate, Delta: `{"title":"must be ignored"}`},
		{Type: agent.EventTextDelta, Delta: "grounded answer"},
		{Type: agent.EventRunCompleted},
	}}
	service, _ := newTestService(t, runner, nil)
	session, _ := service.CreateSession(context.Background(), "notes")
	created, err := service.CreateRun(context.Background(), CreateRunInput{
		SessionID: session.ID, Content: "查找我的笔记", IdempotencyKey: "linear-flow",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	waitForStatus(t, service, created.Run.ID, RunCompleted)
	t.Logf("contract RAG run_id=%s", created.Run.ID)

	events, err := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	want := []string{"run.queued", "run.started", "retrieval.decided", "run.status", "tool.completed", "text.delta", "run.completed"}
	if len(events) != len(want) {
		t.Fatalf("event count = %d, events = %#v", len(events), events)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) || event.Type != want[index] {
			t.Fatalf("event[%d] = %#v, want type %q", index, event, want[index])
		}
	}
	if events[2].Data["required"] != true || events[2].Data["reason"] != string(RetrievalReasonExplicitNoteReference) {
		t.Fatalf("retrieval event = %#v", events[2])
	}
	if !runner.request(0).RequireNoteRetrieval {
		t.Fatal("runner did not receive required retrieval decision")
	}
}

func TestServiceRestoresRecentTurnsAndReevaluatesFollowUp(t *testing.T) {
	runner := &recordingRunner{}
	service, _ := newTestService(t, runner, nil)
	session, _ := service.CreateSession(context.Background(), "multi turn")
	first, err := service.CreateRun(context.Background(), CreateRunInput{
		SessionID: session.ID, Content: "总结我笔记里的重试策略", IdempotencyKey: "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, service, first.Run.ID, RunCompleted)
	second, err := service.CreateRun(context.Background(), CreateRunInput{
		SessionID: session.ID, Content: "第二点为什么这样设计？", IdempotencyKey: "turn-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, service, second.Run.ID, RunCompleted)
	t.Logf("contextual follow-up run_id=%s", second.Run.ID)

	request := runner.request(1)
	if len(request.Messages) != 3 || request.Messages[0].Content != "总结我笔记里的重试策略" || request.Messages[1].Content != "answer" {
		t.Fatalf("restored messages = %#v", request.Messages)
	}
	if !request.RequireNoteRetrieval {
		t.Fatal("contextual follow-up must re-run retrieval")
	}
}

func TestServiceOrdinaryChatDoesNotRequireRetrievalOrStartWorkflow(t *testing.T) {
	runner := &recordingRunner{}
	service, _ := newTestService(t, runner, nil)
	session, _ := service.CreateSession(context.Background(), "ordinary")
	created, err := service.CreateRun(context.Background(), CreateRunInput{
		SessionID: session.ID, Content: "请记住我喜欢茶，并提醒我明天喝水", IdempotencyKey: "ordinary-command",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, service, created.Run.ID, RunCompleted)
	if runner.request(0).RequireNoteRetrieval {
		t.Fatal("ordinary business-like chat unexpectedly required note retrieval")
	}
	events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
	for _, event := range events {
		switch event.Type {
		case "route.decided", "executor.started", "executor.completed", "note.draft.candidate", "workflow.candidate", "skill.started":
			t.Fatalf("legacy workflow event persisted: %#v", event)
		}
	}
}

func TestServiceRejectsNonAllowListedRetrievalReasonWithoutPersistingIt(t *testing.T) {
	runner := &recordingRunner{}
	repo := NewMemoryRepository()
	service, err := NewService(context.Background(), repo, runner, NewBoundedAssembler(1000, 2, nil), ServiceOptions{
		DefaultModel:     "test-model",
		RetrievalDecider: fixedRetrievalDecider{Required: true, Reason: "prompt=secret-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := service.CreateSession(context.Background(), "safe observation")
	created, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "safe-observation"})
	waitForStatus(t, service, created.Run.ID, RunFailed)
	events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
	for _, event := range events {
		if event.Type == "retrieval.decided" {
			t.Fatalf("unsafe decision persisted: %#v", event)
		}
	}
	if runner.requestCount() != 0 {
		t.Fatal("runner invoked after invalid retrieval decision")
	}
}

func TestServiceEmitsContextTruncation(t *testing.T) {
	runner := &recordingRunner{}
	service, _ := newTestService(t, runner, NewBoundedAssembler(24, 1, fixedTokenCounter{}))
	session, _ := service.CreateSession(context.Background(), "bounded")
	first, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "1234567890", IdempotencyKey: "bounded-1"})
	waitForStatus(t, service, first.Run.ID, RunCompleted)
	second, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "abcdefghij", IdempotencyKey: "bounded-2"})
	waitForStatus(t, service, second.Run.ID, RunCompleted)
	events, _ := service.ListEvents(context.Background(), second.Run.ID, 0, 100)
	if !hasEventType(events, "context.truncated") {
		t.Fatalf("missing context.truncated: %#v", events)
	}
}

func TestServiceIdempotencyDoesNotExecuteTwice(t *testing.T) {
	runner := &recordingRunner{}
	service, _ := newTestService(t, runner, nil)
	session, _ := service.CreateSession(context.Background(), "idempotency")
	input := CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "same-key"}
	first, _ := service.CreateRun(context.Background(), input)
	waitForStatus(t, service, first.Run.ID, RunCompleted)
	replayed, err := service.CreateRun(context.Background(), input)
	if err != nil || replayed.Created || replayed.Run.ID != first.Run.ID || runner.requestCount() != 1 {
		t.Fatalf("replayed = %#v, err = %v, request count = %d", replayed, err, runner.requestCount())
	}
}

func TestServiceRejectsConcurrentRunAndSupportsCancel(t *testing.T) {
	block := make(chan struct{})
	runner := &recordingRunner{block: block}
	service, _ := newTestService(t, runner, nil)
	session, _ := service.CreateSession(context.Background(), "cancel")
	created, _ := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "wait", IdempotencyKey: "one"})
	waitForStatus(t, service, created.Run.ID, RunRunning)
	_, err := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "conflict", IdempotencyKey: "two"})
	if !errors.Is(err, ErrActiveRun) {
		t.Fatalf("concurrent CreateRun() error = %v", err)
	}
	cancelled, err := service.CancelRun(context.Background(), created.Run.ID)
	if err != nil || cancelled.Status != RunCancelled {
		t.Fatalf("CancelRun() = %#v, %v", cancelled, err)
	}
}

func TestServicePersistsFailureAndTimeoutTerminalStates(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus RunStatus
		wantEvent  string
	}{
		{name: "failure", err: errors.New("model unavailable"), wantStatus: RunFailed, wantEvent: "run.failed"},
		{name: "timeout", err: context.DeadlineExceeded, wantStatus: RunTimedOut, wantEvent: "run.timed_out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{events: []agent.Event{{Type: agent.EventRunFailed, Err: test.err}}}
			service, _ := newTestService(t, runner, nil)
			session, _ := service.CreateSession(context.Background(), test.name)
			created, err := service.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: test.name})
			if err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, service, created.Run.ID, test.wantStatus)
			events, _ := service.ListEvents(context.Background(), created.Run.ID, 0, 100)
			if events[len(events)-1].Type != test.wantEvent {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestNewServiceMarksPersistedRunningRunInterrupted(t *testing.T) {
	repo := NewMemoryRepository()
	now := time.Now().UTC()
	session := Session{ID: "session", Title: "restart", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	created, err := repo.CreateRun(context.Background(), CreateRunInput{SessionID: session.ID}, Run{ID: "run", SessionID: session.ID, Status: RunQueued, CreatedAt: now}, Message{ID: "message", SessionID: session.ID, Role: "user", Content: "hello", CreatedAt: now}, Event{RunID: "run", Type: "run.queued", CreatedAt: now})
	if err != nil || !created.Created {
		t.Fatalf("seed run: %#v, %v", created, err)
	}
	if err := repo.StartRun(context.Background(), "run", Event{RunID: "run", Type: "run.started", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(context.Background(), repo, &recordingRunner{}, NewBoundedAssembler(100, 1, nil), ServiceOptions{DefaultModel: "test"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := service.GetRun(context.Background(), "run")
	if run.Status != RunInterrupted {
		t.Fatalf("run status = %s", run.Status)
	}
	events, _ := service.ListEvents(context.Background(), "run", 0, 100)
	if events[len(events)-1].Type != "run.interrupted" {
		t.Fatalf("events = %#v", events)
	}
}

func TestServiceListsOnlyCurrentUsersSessions(t *testing.T) {
	service, _ := newTestService(t, &recordingRunner{}, nil)
	first := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 1, TenantID: 10})
	second := agentauth.WithPrincipal(context.Background(), agentauth.Principal{UserID: 2, TenantID: 10})
	firstSession, _ := service.CreateSession(first, "first")
	_, _ = service.CreateSession(second, "second")
	sessions, err := service.ListSessions(first, 50)
	if err != nil || len(sessions) != 1 || sessions[0].ID != firstSession.ID {
		t.Fatalf("ListSessions(first) = %#v, %v", sessions, err)
	}
	if _, err := service.GetSession(second, firstSession.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession(cross-user) error = %v", err)
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

func hasEventType(events []Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func waitForStatus(t *testing.T, service *Service, runID string, want RunStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := service.GetRun(context.Background(), runID)
		if err == nil && run.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	run, _ := service.GetRun(context.Background(), runID)
	t.Fatalf("run status = %s, want %s", run.Status, want)
}
