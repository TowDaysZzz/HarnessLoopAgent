package routing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type fakeDeterministicHandler struct {
	mu    sync.Mutex
	calls int
	text  string
}

func (h *fakeDeterministicHandler) Execute(context.Context, Input) (Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	return Result{Text: h.text}, nil
}

type fakeStreamingHandler struct {
	mu       sync.Mutex
	calls    int
	messages []agent.Message
}

func (h *fakeStreamingHandler) Stream(_ context.Context, input Input) <-chan agent.Event {
	h.mu.Lock()
	h.calls++
	h.messages = append([]agent.Message(nil), input.Messages...)
	h.mu.Unlock()
	out := make(chan agent.Event, 2)
	out <- agent.Event{Type: agent.EventTextDelta, Delta: "stream"}
	out <- agent.Event{Type: agent.EventRunCompleted}
	close(out)
	return out
}

func TestFacadeSelectsExactlyOneHandlerForEveryDecision(t *testing.T) {
	deterministic := []*fakeDeterministicHandler{{text: "create"}, {text: "clarify"}, {text: "reject"}}
	streaming := []*fakeStreamingHandler{{}, {}, {}, {}}
	facade, _ := NewFacade(HandlerSet{
		NoteCreate: deterministic[0], Clarification: deterministic[1], DeleteRejected: deterministic[2],
		SimpleChat: streaming[0], SimpleNoteQuery: streaming[1], ComplexChat: streaming[2], ComplexNoteQuery: streaming[3],
	})
	tests := []RouteDecision{
		{Intent: IntentNoteCreate, Complexity: ComplexitySimple},
		{Intent: IntentNoteDelete, Complexity: ComplexitySimple},
		{Intent: IntentUnclear, Complexity: ComplexitySimple},
		{Intent: IntentChat, Complexity: ComplexitySimple},
		{Intent: IntentChat, Complexity: ComplexityComplex},
		{Intent: IntentNoteQuery, Complexity: ComplexitySimple},
		{Intent: IntentNoteQuery, Complexity: ComplexityComplex},
	}
	for _, decision := range tests {
		execution, err := facade.Execute(context.Background(), Input{Run: RunContext{Decision: decision}, Messages: []agent.Message{{Role: "user", Content: "test"}}})
		if err != nil {
			t.Fatalf("Execute(%#v) error = %v", decision, err)
		}
		for range execution.Events {
		}
	}
	total := 0
	for _, handler := range deterministic {
		total += handler.calls
	}
	for _, handler := range streaming {
		total += handler.calls
	}
	if total != len(tests) {
		t.Fatalf("handler calls = %d, want %d", total, len(tests))
	}
	for _, handler := range deterministic {
		if handler.calls != 1 {
			t.Fatalf("deterministic calls = %d", handler.calls)
		}
	}
	for _, handler := range streaming {
		if handler.calls != 1 {
			t.Fatalf("streaming calls = %d", handler.calls)
		}
	}
}

func TestComplexHandlerRequiresRunnerAndTimeout(t *testing.T) {
	runner := conversationRunnerAdapter{handler: &fakeStreamingHandler{}}
	if _, err := NewComplexHandler(runner, 0); err == nil {
		t.Fatal("expected invalid timeout error")
	}
	if _, err := NewComplexHandler(runner, time.Second); err != nil {
		t.Fatalf("NewComplexHandler() error = %v", err)
	}
}

func TestConversationHandlerPassesBoundedMessages(t *testing.T) {
	runner := &fakeStreamingHandler{}
	handler := ConversationHandler{Runner: conversationRunnerAdapter{handler: runner}, Timeout: time.Second}
	messages := []agent.Message{{Role: "user", Content: "query notes"}}
	for range handler.Stream(context.Background(), Input{Messages: messages}) {
	}
	if runner.calls != 1 || len(runner.messages) != 1 || runner.messages[0].Content != "query notes" {
		t.Fatalf("runner calls=%d messages=%#v", runner.calls, runner.messages)
	}
}

type closingConversationRunner struct{}

func (closingConversationRunner) StreamConversation(context.Context, agent.ConversationRequest) <-chan agent.Event {
	out := make(chan agent.Event)
	close(out)
	return out
}

func TestConversationHandlerSynthesizesFailureWhenSourceHasNoTerminalEvent(t *testing.T) {
	handler := ConversationHandler{Runner: closingConversationRunner{}, Timeout: time.Second}
	events := handler.Stream(context.Background(), Input{Messages: []agent.Message{{Role: "user", Content: "hello"}}})
	event, ok := <-events
	if !ok {
		t.Fatal("stream closed without synthesized failure")
	}
	if event.Type != agent.EventRunFailed || event.Err == nil {
		t.Fatalf("event = %#v", event)
	}
	if _, open := <-events; open {
		t.Fatal("stream remained open after terminal event")
	}
}

type conversationRunnerAdapter struct{ handler *fakeStreamingHandler }

func (a conversationRunnerAdapter) StreamConversation(ctx context.Context, request agent.ConversationRequest) <-chan agent.Event {
	return a.handler.Stream(ctx, Input{Messages: request.Messages})
}

func TestFacadeRejectsMissingHandler(t *testing.T) {
	facade, _ := NewFacade(HandlerSet{})
	_, err := facade.Execute(context.Background(), Input{Run: RunContext{Decision: RouteDecision{Intent: IntentNoteCreate}}})
	if !errors.Is(err, ErrHandlerUnavailable) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDeterministicHandlersCompleteWithoutModel(t *testing.T) {
	facade, _ := NewFacade(HandlerSet{Clarification: ClarificationHandler{}, DeleteRejected: DeleteRejectedHandler{}})
	for _, intent := range []DomainIntent{IntentUnclear, IntentNoteDelete} {
		execution, err := facade.Execute(context.Background(), Input{Run: RunContext{Decision: RouteDecision{Intent: intent, Complexity: ComplexitySimple}}})
		if err != nil {
			t.Fatal(err)
		}
		var events []agent.Event
		for event := range execution.Events {
			events = append(events, event)
		}
		if len(events) != 2 || events[0].Type != agent.EventTextDelta || events[1].Type != agent.EventRunCompleted {
			t.Fatalf("events = %#v", events)
		}
	}
}
