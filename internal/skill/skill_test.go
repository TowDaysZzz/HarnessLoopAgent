package skill

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type acceptCodec struct{}

func (acceptCodec) Validate(raw json.RawMessage) error {
	if !json.Valid(raw) {
		return errors.New("invalid")
	}
	return nil
}

type staticMatcher struct{}

func (staticMatcher) Match(context.Context, MatchInput) (Match, bool, error) {
	return Match{Arguments: json.RawMessage(`{}`), Confidence: 1}, true, nil
}

type directFake struct {
	result Result
	err    error
}

func (f directFake) Execute(context.Context, Request) (Result, error) { return f.result, f.err }

type workflowFake struct {
	result Result
	err    error
}

func (f workflowFake) Run(context.Context, Request) (Result, error) { return f.result, f.err }

type durableFake struct {
	result Result
	err    error
}

func (f durableFake) Start(context.Context, Request) (Result, error) { return f.result, f.err }

type streamFake struct{ events []agent.Event }

func (f streamFake) Stream(context.Context, Request) <-chan agent.Event {
	out := make(chan agent.Event, len(f.events))
	for _, event := range f.events {
		out <- event
	}
	close(out)
	return out
}

func validBudget() Budget {
	return Budget{Timeout: time.Second, MaxSteps: 8, MaxModelCalls: 2, MaxToolCalls: 2, MaxContextBytes: 4096, MaxOutputBytes: 4096}
}

func definition(mode ExecutionMode) Definition {
	d := Definition{ID: "daily_review", Version: "v1", Mode: mode, Risk: RiskReadOnly, Dependencies: []Dependency{"chat"}, Budget: validBudget(), Matcher: staticMatcher{}, InputCodec: acceptCodec{}, OutputCodec: acceptCodec{}}
	switch mode {
	case ModeDirect:
		d.Direct = directFake{result: Result{Text: "ok"}}
	case ModeStreaming:
		d.Streaming = streamFake{events: []agent.Event{{Type: agent.EventTextDelta, Delta: "ok"}, {Type: agent.EventRunCompleted}}}
	case ModeWorkflow:
		d.Workflow = workflowFake{result: Result{Text: "ok"}}
	case ModeDurableWorkflow:
		d.Durable = durableFake{result: Result{Text: "confirm", Candidate: json.RawMessage(`{"wait_id":"w1"}`), Suspended: true}}
	}
	return d
}

func invocation(t *testing.T) Invocation {
	t.Helper()
	value, err := NewInvocation("inv-1", Owner{TenantID: 1, UserID: 2}, "session-1", "run-1", Ref{ID: "daily_review", Version: "v1"}, json.RawMessage(`{"date":"today"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestNormalizeArgumentsRejectsSecurityFields(t *testing.T) {
	tests := []string{`{"tenant_id":2}`, `{"nested":{"access_token":"secret"}}`, `[]`, `{"owner":{"id":1}}`}
	for _, raw := range tests {
		if _, _, err := NormalizeArguments(json.RawMessage(raw), 1024); !errors.Is(err, ErrInvalidInvocation) {
			t.Fatalf("NormalizeArguments(%s) error = %v", raw, err)
		}
	}
	a, hashA, err := NormalizeArguments(json.RawMessage(`{"b":2,"a":1}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	b, hashB, err := NormalizeArguments(json.RawMessage(`{ "a": 1, "b": 2 }`), 1024)
	if err != nil || string(a) != string(b) || hashA != hashB {
		t.Fatalf("canonical mismatch: %s %s", a, b)
	}
}

func TestDefinitionValidation(t *testing.T) {
	tests := []Definition{
		definition("unknown"),
		func() Definition { d := definition(ModeDirect); d.ID = "Bad ID"; return d }(),
		func() Definition { d := definition(ModeDirect); d.Budget.MaxSteps = 0; return d }(),
		func() Definition { d := definition(ModeDirect); d.Direct = nil; d.Workflow = workflowFake{}; return d }(),
	}
	for _, value := range tests {
		if err := value.Validate(map[Dependency]bool{"chat": true}); err == nil {
			t.Fatalf("definition unexpectedly valid: %#v", value)
		}
	}
	if err := definition(ModeWorkflow).Validate(map[Dependency]bool{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing dependency error = %v", err)
	}
}

func TestRegistryIsVersionedAndRejectsDuplicates(t *testing.T) {
	defs := []Definition{definition(ModeDirect)}
	registry, err := NewRegistry(defs, map[Dependency]bool{"chat": true})
	if err != nil {
		t.Fatal(err)
	}
	defs[0].ID = "mutated"
	if _, err := registry.Resolve(Ref{ID: "daily_review", Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(Ref{ID: "daily_review", Version: "v2"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown version error = %v", err)
	}
	if _, err := NewRegistry([]Definition{definition(ModeDirect), definition(ModeDirect)}, map[Dependency]bool{"chat": true}); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestExecutorModesAndTerminalEvents(t *testing.T) {
	for _, mode := range []ExecutionMode{ModeDirect, ModeStreaming, ModeWorkflow, ModeDurableWorkflow} {
		d := definition(mode)
		registry, _ := NewRegistry([]Definition{d}, map[Dependency]bool{"chat": true})
		executor, _ := NewExecutor(registry)
		execution, err := executor.Execute(context.Background(), Request{Invocation: invocation(t)})
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		var events []agent.Event
		for event := range execution.Events {
			events = append(events, event)
		}
		if len(events) < 2 || events[0].Type != EventStarted || events[len(events)-1].Type != agent.EventRunCompleted {
			t.Fatalf("mode %s events = %#v", mode, events)
		}
		if mode == ModeDurableWorkflow && len(events) != 4 {
			t.Fatalf("durable events = %#v", events)
		}
	}
}

func TestExecutorFailsClosedOnOutputAndProtocol(t *testing.T) {
	d := definition(ModeDirect)
	d.Budget.MaxOutputBytes = 2
	d.Direct = directFake{result: Result{Text: "too large"}}
	registry, _ := NewRegistry([]Definition{d}, map[Dependency]bool{"chat": true})
	executor, _ := NewExecutor(registry)
	execution, _ := executor.Execute(context.Background(), Request{Invocation: invocation(t)})
	var last agent.Event
	for event := range execution.Events {
		last = event
	}
	if last.Type != agent.EventRunFailed || !errors.Is(last.Err, ErrOutputLimit) {
		t.Fatalf("last event = %#v", last)
	}

	d = definition(ModeStreaming)
	d.Streaming = streamFake{events: []agent.Event{{Type: agent.EventTextDelta, Delta: "unterminated"}}}
	registry, _ = NewRegistry([]Definition{d}, map[Dependency]bool{"chat": true})
	executor, _ = NewExecutor(registry)
	execution, _ = executor.Execute(context.Background(), Request{Invocation: invocation(t)})
	for event := range execution.Events {
		last = event
	}
	if last.Type != agent.EventRunFailed || !errors.Is(last.Err, ErrStreamProtocol) {
		t.Fatalf("stream last event = %#v", last)
	}
}

func TestWorkflowExecutorEmitsBoundedSkillLifecycleOrder(t *testing.T) {
	d := definition(ModeWorkflow)
	d.Workflow = workflowFake{result: Result{Text: "review", Candidate: json.RawMessage(`{"version":"v1"}`), CacheState: "hit", Steps: []string{"resolve_window", "render"}}}
	registry, _ := NewRegistry([]Definition{d}, map[Dependency]bool{"chat": true})
	executor, _ := NewExecutor(registry)
	execution, _ := executor.Execute(context.Background(), Request{Invocation: invocation(t)})
	var types []agent.EventType
	for event := range execution.Events {
		types = append(types, event.Type)
	}
	want := []agent.EventType{EventStarted, EventCache, EventStep, EventStep, EventCandidate, agent.EventTextDelta, agent.EventRunCompleted}
	if len(types) != len(want) {
		t.Fatalf("events=%v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events=%v", types)
		}
	}
}
