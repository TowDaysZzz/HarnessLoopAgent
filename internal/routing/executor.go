package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

var ErrHandlerUnavailable = errors.New("routing handler unavailable")

type Input struct {
	Run      RunContext
	Content  string
	Messages []agent.Message
}

type DeterministicHandler interface {
	Execute(context.Context, Input) (Result, error)
}

type StreamingHandler interface {
	Stream(context.Context, Input) <-chan agent.Event
}

type HandlerSet struct {
	NoteCreate       DeterministicHandler
	Clarification    DeterministicHandler
	DeleteRejected   DeterministicHandler
	SimpleChat       StreamingHandler
	SimpleNoteQuery  StreamingHandler
	ComplexChat      StreamingHandler
	ComplexNoteQuery StreamingHandler
}

type Execution struct {
	Handler string
	Events  <-chan agent.Event
}

type Facade struct {
	handlers HandlerSet
}

func NewFacade(handlers HandlerSet) (*Facade, error) {
	return &Facade{handlers: handlers}, nil
}

func (f *Facade) Execute(ctx context.Context, input Input) (Execution, error) {
	decision := input.Run.Decision
	switch decision.Intent {
	case IntentNoteCreate:
		return f.deterministic(ctx, "note_create", f.handlers.NoteCreate, input)
	case IntentNoteDelete:
		return f.deterministic(ctx, "note_delete_rejected", f.handlers.DeleteRejected, input)
	case IntentUnclear:
		return f.deterministic(ctx, "clarification", f.handlers.Clarification, input)
	case IntentNoteQuery:
		if decision.Complexity == ComplexityComplex {
			return f.streaming("complex_note_query", f.handlers.ComplexNoteQuery, ctx, input)
		}
		return f.streaming("simple_note_query", f.handlers.SimpleNoteQuery, ctx, input)
	case IntentChat:
		if decision.Complexity == ComplexityComplex {
			return f.streaming("complex_chat", f.handlers.ComplexChat, ctx, input)
		}
		return f.streaming("simple_chat", f.handlers.SimpleChat, ctx, input)
	default:
		return Execution{}, fmt.Errorf("%w: intent %q", ErrHandlerUnavailable, decision.Intent)
	}
}

func (f *Facade) deterministic(ctx context.Context, name string, handler DeterministicHandler, input Input) (Execution, error) {
	if handler == nil {
		return Execution{}, fmt.Errorf("%w: %s", ErrHandlerUnavailable, name)
	}
	out := make(chan agent.Event, 3)
	go func() {
		defer close(out)
		result, err := handler.Execute(ctx, input)
		if err != nil {
			emitAgentEvent(ctx, out, agent.Event{Type: agent.EventRunFailed, Err: err})
			return
		}
		if result.Candidate != nil {
			encoded, _ := json.Marshal(result.Candidate)
			if !emitAgentEvent(ctx, out, agent.Event{Type: agent.EventDraftCandidate, Delta: string(encoded)}) {
				return
			}
		}
		if result.Text != "" && !emitAgentEvent(ctx, out, agent.Event{Type: agent.EventTextDelta, Delta: result.Text}) {
			return
		}
		emitAgentEvent(ctx, out, agent.Event{Type: agent.EventRunCompleted})
	}()
	return Execution{Handler: name, Events: out}, nil
}

func (f *Facade) streaming(name string, handler StreamingHandler, ctx context.Context, input Input) (Execution, error) {
	if handler == nil {
		return Execution{}, fmt.Errorf("%w: %s", ErrHandlerUnavailable, name)
	}
	return Execution{Handler: name, Events: handler.Stream(ctx, input)}, nil
}

type ConversationHandler struct {
	Runner  agent.ConversationRunner
	Timeout time.Duration
}

type ComplexHandler struct {
	conversation ConversationHandler
	maxSteps     int
}

func NewComplexHandler(runner agent.ConversationRunner, timeout time.Duration, maxSteps int) (*ComplexHandler, error) {
	if runner == nil || timeout <= 0 || maxSteps < 1 {
		return nil, errors.New("complex handler requires runner, positive timeout, and positive step budget")
	}
	return &ComplexHandler{conversation: ConversationHandler{Runner: runner, Timeout: timeout}, maxSteps: maxSteps}, nil
}

func (h *ComplexHandler) Stream(ctx context.Context, input Input) <-chan agent.Event {
	return h.conversation.Stream(ctx, input)
}

func (h ConversationHandler) Stream(ctx context.Context, input Input) <-chan agent.Event {
	if h.Runner == nil {
		return failedStream(errors.New("conversation runner is not configured"))
	}
	if h.Timeout <= 0 {
		return h.Runner.StreamMessages(ctx, input.Messages)
	}
	bounded, cancel := context.WithTimeout(ctx, h.Timeout)
	source := h.Runner.StreamMessages(bounded, input.Messages)
	out := make(chan agent.Event)
	go func() {
		defer close(out)
		defer cancel()
		for event := range source {
			if !emitAgentEvent(ctx, out, event) {
				return
			}
		}
	}()
	return out
}

type ClarificationHandler struct{}

func (ClarificationHandler) Execute(context.Context, Input) (Result, error) {
	return Result{Text: "请补充你想要执行的操作和具体内容，例如查询哪类笔记，或要记录什么内容。"}, nil
}

type DeleteRejectedHandler struct{}

func (DeleteRejectedHandler) Execute(context.Context, Input) (Result, error) {
	return Result{Text: "聊天中不支持删除笔记。请前往笔记页面删除，或调用笔记删除 REST API。"}, nil
}

type StaticTextHandler struct{ Text string }

func (h StaticTextHandler) Execute(context.Context, Input) (Result, error) {
	return Result{Text: h.Text}, nil
}

func failedStream(err error) <-chan agent.Event {
	out := make(chan agent.Event, 1)
	out <- agent.Event{Type: agent.EventRunFailed, Err: err}
	close(out)
	return out
}

func emitAgentEvent(ctx context.Context, out chan<- agent.Event, event agent.Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
