package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type typedData struct{ Count int }

type typedNode struct{ id NodeID }

func (n typedNode) ID() NodeID { return n.id }
func (n typedNode) Execute(_ context.Context, input NodeInput[typedData]) (NodeResult[typedData], error) {
	input.State.Data.Count++
	return NodeResult[typedData]{State: input.State, Directive: DirectiveContinue}, nil
}

var _ Node[typedData] = typedNode{}

func TestIdentifiersAndSourceRef(t *testing.T) {
	now := time.Now().UTC()
	valid := RunMetadata{WorkflowID: "wf", DefinitionVersion: "v1", RunID: "run", StartedAt: now}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	tests := []RunMetadata{
		{DefinitionVersion: "v1", RunID: "run", StartedAt: now},
		{WorkflowID: "wf", RunID: "run", StartedAt: now},
		{WorkflowID: "wf", DefinitionVersion: "v1", StartedAt: now},
		{WorkflowID: "wf", DefinitionVersion: "v1", RunID: "run"},
		{WorkflowID: "wf", DefinitionVersion: "v1", RunID: "run", StartedAt: now, Source: SourceRef{Type: "chat_run"}},
	}
	for _, test := range tests {
		if err := test.Validate(); !IsCode(err, CodeInvalidContract) {
			t.Fatalf("Validate(%#v) = %v", test, err)
		}
	}
}

func TestWorkflowStateKeepsTypedData(t *testing.T) {
	state := validState(typedData{Count: 4})
	result, err := (typedNode{id: "increment"}).Execute(context.Background(), NodeInput[typedData]{State: state})
	if err != nil || result.State.Data.Count != 5 {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestRunStatusTransitions(t *testing.T) {
	allowed := map[RunStatus][]RunStatus{
		RunPending:   {RunRunning, RunCancelled, RunExpired},
		RunRunning:   {RunSuspended, RunCompleted, RunFailed, RunCancelled, RunExpired},
		RunSuspended: {RunRunning, RunFailed, RunCancelled, RunExpired},
	}
	all := []RunStatus{RunPending, RunRunning, RunSuspended, RunCompleted, RunFailed, RunCancelled, RunExpired}
	for _, from := range all {
		for _, to := range all {
			state := ControlState{Status: from, StateVersion: 7}
			err := state.Transition(to)
			want := containsStatus(allowed[from], to)
			if want && (err != nil || state.Status != to || state.StateVersion != 8) {
				t.Fatalf("Transition(%s, %s) = %#v, %v", from, to, state, err)
			}
			if !want && (!IsCode(err, CodeInvalidState) || state.Status != from || state.StateVersion != 7) {
				t.Fatalf("rejected Transition(%s, %s) = %#v, %v", from, to, state, err)
			}
		}
	}
}

func TestErrorCodesAreDistinguishable(t *testing.T) {
	codes := []ErrorCode{CodeInvalidContract, CodeInvalidState, CodeStepBudget, CodeResumeBudget, CodeTimeout, CodeCancelled}
	for _, code := range codes {
		cause := errors.New("cause")
		err := &Error{Code: code, Message: "test", Err: cause}
		if !IsCode(err, code) || !errors.Is(err, cause) {
			t.Fatalf("error code %s was not preserved: %v", code, err)
		}
	}
}

func TestWaitKindsActionsAndRequest(t *testing.T) {
	now := time.Now().UTC()
	request := validWaitRequest(now)
	if err := request.Validate(now); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	for _, kind := range []WaitKind{WaitApproval, WaitReview, WaitEdit, WaitInput} {
		if !kind.Valid() {
			t.Fatalf("kind %q is not valid", kind)
		}
	}
	for _, action := range []HumanAction{ActionApprove, ActionReject, ActionSubmitEdit, ActionCancel} {
		if !action.Valid() {
			t.Fatalf("action %q is not valid", action)
		}
	}
	invalid := []WaitRequest{
		{},
		func() WaitRequest { v := request; v.Kind = "unknown"; return v }(),
		func() WaitRequest { v := request; v.AllowedActions = nil; return v }(),
		func() WaitRequest {
			v := request
			v.AllowedActions = []HumanAction{ActionApprove, ActionApprove}
			return v
		}(),
		func() WaitRequest { v := request; v.AllowedActions = []HumanAction{""}; return v }(),
		func() WaitRequest { v := request; v.ExpiresAt = now; return v }(),
	}
	for _, value := range invalid {
		if err := value.Validate(now); !IsCode(err, CodeInvalidContract) {
			t.Fatalf("Validate(%#v) = %v", value, err)
		}
	}
}

func TestWaitPointValidatesResume(t *testing.T) {
	now := time.Now().UTC()
	point := WaitPoint(validWaitRequest(now))
	valid := ResumeCommand{RunID: point.RunID, WaitID: point.ID, Version: point.Version, ContentHash: point.ContentHash, Action: ActionApprove}
	if err := point.ValidateResume(valid, now); err != nil {
		t.Fatalf("ValidateResume() = %v", err)
	}
	tests := []struct {
		name    string
		command ResumeCommand
		code    ErrorCode
		now     time.Time
	}{
		{"run", func() ResumeCommand { v := valid; v.RunID = "other"; return v }(), CodeInvalidResume, now},
		{"wait", func() ResumeCommand { v := valid; v.WaitID = "other"; return v }(), CodeInvalidResume, now},
		{"version", func() ResumeCommand { v := valid; v.Version++; return v }(), CodeInvalidResume, now},
		{"hash", func() ResumeCommand { v := valid; v.ContentHash = "other"; return v }(), CodeInvalidResume, now},
		{"action", func() ResumeCommand { v := valid; v.Action = ActionSubmitEdit; return v }(), CodeInvalidResume, now},
		{"expired", valid, CodeWaitExpired, point.ExpiresAt},
	}
	for _, test := range tests {
		if err := point.ValidateResume(test.command, test.now); !IsCode(err, test.code) {
			t.Fatalf("%s: ValidateResume() = %v", test.name, err)
		}
	}
}

func TestNodeEventHasAllowListedShape(t *testing.T) {
	typeOfEvent := reflect.TypeOf(NodeEvent{})
	for index := 0; index < typeOfEvent.NumField(); index++ {
		field := typeOfEvent.Field(index)
		if field.Type.Kind() == reflect.Map {
			t.Fatalf("NodeEvent contains map field %s", field.Name)
		}
	}
	encoded, err := json.Marshal(NodeEvent{Sequence: 1, WorkflowID: "wf", RunID: "run", NodeID: "node", Type: EventNodeStarted})
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"token", "cookie", "password", "prompt", "input"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("event JSON contains forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestRuntimeStateAndEventsExcludeSensitiveFields(t *testing.T) {
	now := time.Now().UTC()
	wait := WaitPoint(validWaitRequest(now))
	values := []any{
		RunMetadata{WorkflowID: "wf", DefinitionVersion: "v1", RunID: "run", Source: SourceRef{Type: "chat_run", ID: "source"}, StartedAt: now},
		ControlState{Status: RunSuspended, CurrentNode: "review", PendingWait: &wait, StateVersion: 2, EventSequence: 3},
		NodeEvent{Sequence: 3, WorkflowID: "wf", RunID: "run", NodeID: "review", Type: EventNodeSuspended, WaitID: wait.ID, OccurredAt: now},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal(%T) = %v", value, err)
		}
		lower := strings.ToLower(string(encoded))
		for _, forbidden := range []string{"access_token", "cookie", "password", "model_key", "model_secret", "prompt", "raw_input", "user_input"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%T JSON contains forbidden field %q: %s", value, forbidden, encoded)
			}
		}
	}
}

func TestMemoryCollectorPreservesMonotonicOrder(t *testing.T) {
	collector := NewMemoryCollector()
	for sequence, eventType := range []NodeEventType{EventNodeStarted, EventNodeCompleted} {
		event := NodeEvent{Sequence: int64(sequence + 1), WorkflowID: "wf", RunID: "run", NodeID: "node", Type: eventType}
		if err := collector.Observe(context.Background(), event); err != nil {
			t.Fatalf("Observe() = %v", err)
		}
	}
	if err := collector.Observe(context.Background(), NodeEvent{Sequence: 2, WorkflowID: "wf", RunID: "run", NodeID: "node", Type: EventNodeFailed}); !IsCode(err, CodeInvalidContract) {
		t.Fatalf("non-monotonic Observe() = %v", err)
	}
	events := collector.Events()
	if len(events) != 2 || events[0].Type != EventNodeStarted || events[1].Type != EventNodeCompleted {
		t.Fatalf("Events() = %#v", events)
	}
	if err := (NoopObserver{}).Observe(context.Background(), events[0]); err != nil {
		t.Fatalf("NoopObserver.Observe() = %v", err)
	}
}

func validState[T any](data T) WorkflowState[T] {
	return WorkflowState[T]{
		Meta:    RunMetadata{WorkflowID: "workflow", DefinitionVersion: "v1", RunID: "run-1", StartedAt: time.Now().UTC()},
		Control: ControlState{Status: RunPending},
		Data:    data,
	}
}

func validWaitRequest(now time.Time) WaitRequest {
	return WaitRequest{
		ID: "wait-1", RunID: "run-1", NodeID: "review", Kind: WaitApproval, Version: 1,
		ContentHash: "content-hash", AllowedActions: []HumanAction{ActionApprove, ActionReject}, ExpiresAt: now.Add(time.Hour),
	}
}

func containsStatus(values []RunStatus, target RunStatus) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
