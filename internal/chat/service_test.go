package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
)

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
