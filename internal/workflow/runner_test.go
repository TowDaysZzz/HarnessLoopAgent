package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type runnerData struct {
	Value int
	Order []string
}

type functionNode struct {
	id      NodeID
	execute func(context.Context, NodeInput[runnerData]) (NodeResult[runnerData], error)
	calls   int
}

func (n *functionNode) ID() NodeID { return n.id }
func (n *functionNode) Execute(ctx context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
	n.calls++
	return n.execute(ctx, input)
}

type failingObserver struct {
	failType NodeEventType
	events   []NodeEvent
}

func (o *failingObserver) Observe(_ context.Context, event NodeEvent) error {
	if event.Type == o.failType {
		return errors.New("observer unavailable")
	}
	o.events = append(o.events, event)
	return nil
}

func TestNewRunnerValidatesNodes(t *testing.T) {
	good := &functionNode{id: "good", execute: continueNode("good", 0)}
	var typedNil *functionNode
	tests := []struct {
		name  string
		nodes []Node[runnerData]
	}{
		{"empty", nil},
		{"nil", []Node[runnerData]{nil}},
		{"typed nil", []Node[runnerData]{typedNil}},
		{"empty id", []Node[runnerData]{&functionNode{execute: continueNode("", 0)}}},
		{"duplicate", []Node[runnerData]{good, &functionNode{id: "good", execute: continueNode("other", 0)}}},
	}
	for _, test := range tests {
		if _, err := NewRunner(test.nodes, RunnerOptions{}); !IsCode(err, CodeInvalidContract) {
			t.Fatalf("%s: NewRunner() = %v", test.name, err)
		}
	}
}

func TestRunnerPassesTypedStateInOrder(t *testing.T) {
	first := &functionNode{id: "first", execute: continueNode("first", 1)}
	second := &functionNode{id: "second", execute: func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
		input.State.Data.Value *= 2
		input.State.Data.Order = append(input.State.Data.Order, "second")
		return NodeResult[runnerData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	collector := NewMemoryCollector()
	runner := mustRunner(t, []Node[runnerData]{first, second}, RunnerOptions{Observer: collector})
	result, err := runner.Run(context.Background(), runnerState(runnerData{Value: 2}))
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if result.Status != RunCompleted || result.State.Data.Value != 6 || !reflect.DeepEqual(result.State.Data.Order, []string{"first", "second"}) {
		t.Fatalf("Run() = %#v", result)
	}
	if first.calls != 1 || second.calls != 1 || result.State.Control.StepsExecuted != 2 || len(result.State.Control.CompletedNodes) != 2 || result.State.Control.StateVersion == 0 {
		t.Fatalf("progress = %#v, calls = %d/%d", result.State.Control, first.calls, second.calls)
	}
	assertEventTypes(t, collector.Events(), EventNodeStarted, EventNodeCompleted, EventNodeStarted, EventNodeCompleted)
}

func TestRunnerRejectsDirtyInitialProgress(t *testing.T) {
	node := &functionNode{id: "node", execute: continueNode("node", 0)}
	runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{})
	state := runnerState(runnerData{})
	state.Control.CompletedNodes = []NodeID{"old"}
	result, err := runner.Run(context.Background(), state)
	if !IsCode(err, CodeInvalidState) || result.Status != RunPending || node.calls != 0 {
		t.Fatalf("Run() = %#v, %v, calls=%d", result, err, node.calls)
	}
}

func TestRunnerNodeFailureAndContextTermination(t *testing.T) {
	tests := []struct {
		name    string
		nodeErr error
		code    ErrorCode
		status  RunStatus
	}{
		{"node", errors.New("boom"), CodeNodeFailed, RunFailed},
		{"cancelled", context.Canceled, CodeCancelled, RunCancelled},
		{"timeout", context.DeadlineExceeded, CodeTimeout, RunExpired},
	}
	for _, test := range tests {
		node := &functionNode{id: "fail", execute: func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
			return NodeResult[runnerData]{State: input.State}, test.nodeErr
		}}
		collector := NewMemoryCollector()
		runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Observer: collector})
		result, err := runner.Run(context.Background(), runnerState(runnerData{}))
		if !IsCode(err, test.code) || result.Status != test.status {
			t.Fatalf("%s: Run() = %#v, %v", test.name, result, err)
		}
		assertEventTypes(t, collector.Events(), EventNodeStarted, EventNodeFailed)
	}
}

func TestRunnerStopsBeforeNodeForCancelledContextAndDeadline(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	node := &functionNode{id: "node", execute: continueNode("node", 0)}
	runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Now: func() time.Time { return now }})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runner.Run(ctx, runnerStateAt(runnerData{}, now))
	if !IsCode(err, CodeCancelled) || result.Status != RunCancelled || node.calls != 0 {
		t.Fatalf("cancelled Run() = %#v, %v, calls=%d", result, err, node.calls)
	}

	state := runnerStateAt(runnerData{}, now)
	state.Budget.Deadline = now
	result, err = runner.Run(context.Background(), state)
	if !IsCode(err, CodeTimeout) || result.Status != RunExpired || node.calls != 0 {
		t.Fatalf("expired Run() = %#v, %v, calls=%d", result, err, node.calls)
	}
}

func TestRunnerTreatsContextTerminationDuringNodeAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	node := &functionNode{id: "node", execute: func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
		cancel()
		return NodeResult[runnerData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	collector := NewMemoryCollector()
	runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Observer: collector})
	result, err := runner.Run(ctx, runnerState(runnerData{}))
	if !IsCode(err, CodeCancelled) || result.Status != RunCancelled || len(result.State.Control.CompletedNodes) != 0 {
		t.Fatalf("cancelled during node Run() = %#v, %v", result, err)
	}
	assertEventTypes(t, collector.Events(), EventNodeStarted, EventNodeFailed)
}

func TestRunnerPropagatesObserverFailures(t *testing.T) {
	node := &functionNode{id: "node", execute: continueNode("node", 1)}
	startedObserver := &failingObserver{failType: EventNodeStarted}
	runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Observer: startedObserver})
	result, err := runner.Run(context.Background(), runnerState(runnerData{}))
	if !IsCode(err, CodeObserverFailed) || result.Status != RunFailed || node.calls != 0 {
		t.Fatalf("started observer Run() = %#v, %v, calls=%d", result, err, node.calls)
	}

	node = &functionNode{id: "node", execute: continueNode("node", 1)}
	completedObserver := &failingObserver{failType: EventNodeCompleted}
	runner = mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Observer: completedObserver})
	result, err = runner.Run(context.Background(), runnerState(runnerData{}))
	if !IsCode(err, CodeObserverFailed) || result.Status != RunFailed || node.calls != 1 {
		t.Fatalf("completed observer Run() = %#v, %v, calls=%d", result, err, node.calls)
	}

	now := time.Now().UTC()
	wait := WaitPoint(runnerWait(now, "node"))
	suspended := runnerStateAt(runnerData{}, now)
	suspended.Control = ControlState{Status: RunSuspended, CurrentNode: "node", PendingWait: &wait, StepsExecuted: 1}
	resumeObserver := &failingObserver{failType: EventNodeResumed}
	node = &functionNode{id: "node", execute: continueNode("node", 1)}
	runner = mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Observer: resumeObserver})
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	result, err = runner.Resume(context.Background(), suspended, command)
	if !IsCode(err, CodeObserverFailed) || !reflect.DeepEqual(result.State, suspended) || node.calls != 0 {
		t.Fatalf("resume observer Resume() = %#v, %v, calls=%d", result, err, node.calls)
	}
}

func TestRunnerSuspendsAndResumesSameNode(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	review := &functionNode{id: "review", execute: func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
		if input.Resume == nil {
			wait := runnerWait(now, "review")
			return NodeResult[runnerData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
		}
		if input.Resume.Action != ActionApprove {
			return NodeResult[runnerData]{State: input.State}, errors.New("unexpected resume action")
		}
		input.State.Data.Order = append(input.State.Data.Order, "approved")
		return NodeResult[runnerData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	finish := &functionNode{id: "finish", execute: continueNode("finish", 1)}
	collector := NewMemoryCollector()
	runner := mustRunner(t, []Node[runnerData]{review, finish}, RunnerOptions{Observer: collector, Now: func() time.Time { return now }})
	state := runnerStateAt(runnerData{}, now)
	state.Budget = BudgetState{MaxSteps: 4, MaxResumes: 2, Deadline: now.Add(2 * time.Hour)}

	suspended, err := runner.Run(context.Background(), state)
	if err != nil || suspended.Status != RunSuspended || suspended.State.Control.PendingWait == nil || finish.calls != 0 {
		t.Fatalf("suspended Run() = %#v, %v, finish calls=%d", suspended, err, finish.calls)
	}
	point := suspended.State.Control.PendingWait
	command := ResumeCommand{RunID: point.RunID, WaitID: point.ID, Version: point.Version, ContentHash: point.ContentHash, Action: ActionApprove}
	completed, err := runner.Resume(context.Background(), suspended.State, command)
	if err != nil || completed.Status != RunCompleted || review.calls != 2 || finish.calls != 1 {
		t.Fatalf("Resume() = %#v, %v, calls=%d/%d", completed, err, review.calls, finish.calls)
	}
	if completed.State.Control.ResumeCount != 1 || completed.State.Control.StepsExecuted != 3 || completed.State.Control.PendingWait != nil || !reflect.DeepEqual(completed.State.Data.Order, []string{"approved", "finish"}) {
		t.Fatalf("completed state = %#v", completed.State)
	}
	assertEventTypes(t, collector.Events(),
		EventNodeStarted, EventNodeSuspended,
		EventNodeResumed, EventNodeStarted, EventNodeCompleted,
		EventNodeStarted, EventNodeCompleted,
	)
	events := collector.Events()
	if events[3].Attempt != 2 || events[2].ResumeCount != 0 || events[3].ResumeCount != 1 {
		t.Fatalf("resume event counters = %#v", events)
	}

	beforeCalls := review.calls + finish.calls
	repeated, err := runner.Resume(context.Background(), completed.State, command)
	if !IsCode(err, CodeInvalidState) || repeated.Status != RunCompleted || review.calls+finish.calls != beforeCalls {
		t.Fatalf("repeated Resume() = %#v, %v", repeated, err)
	}
}

func TestRunnerRejectsInvalidSuspendContract(t *testing.T) {
	tests := []struct {
		name string
		wait *WaitRequest
	}{
		{"missing", nil},
		{"invalid", &WaitRequest{}},
	}
	for _, test := range tests {
		node := &functionNode{id: "review", execute: func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
			return NodeResult[runnerData]{State: input.State, Directive: DirectiveSuspend, Wait: test.wait}, nil
		}}
		collector := NewMemoryCollector()
		runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Observer: collector})
		result, err := runner.Run(context.Background(), runnerState(runnerData{}))
		if !IsCode(err, CodeInvalidContract) || result.Status != RunFailed || result.State.Control.PendingWait != nil {
			t.Fatalf("%s: Run() = %#v, %v", test.name, result, err)
		}
		assertEventTypes(t, collector.Events(), EventNodeStarted, EventNodeFailed)
	}
}

func TestResumeRejectionsPreserveSuspendedState(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	node := &functionNode{id: "review", execute: func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
		wait := runnerWait(now, "review")
		return NodeResult[runnerData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
	}}
	runner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Now: func() time.Time { return now }})
	suspended, err := runner.Run(context.Background(), runnerStateAt(runnerData{}, now))
	if err != nil {
		t.Fatal(err)
	}
	point := suspended.State.Control.PendingWait
	valid := ResumeCommand{RunID: point.RunID, WaitID: point.ID, Version: point.Version, ContentHash: point.ContentHash, Action: ActionApprove}
	tests := []struct {
		name    string
		command ResumeCommand
		code    ErrorCode
	}{
		{"run", func() ResumeCommand { v := valid; v.RunID = "other"; return v }(), CodeInvalidResume},
		{"wait", func() ResumeCommand { v := valid; v.WaitID = "other"; return v }(), CodeInvalidResume},
		{"version", func() ResumeCommand { v := valid; v.Version++; return v }(), CodeInvalidResume},
		{"hash", func() ResumeCommand { v := valid; v.ContentHash = "other"; return v }(), CodeInvalidResume},
		{"action", func() ResumeCommand { v := valid; v.Action = ActionSubmitEdit; return v }(), CodeInvalidResume},
	}
	for _, test := range tests {
		before := suspended.State
		result, resumeErr := runner.Resume(context.Background(), before, test.command)
		if !IsCode(resumeErr, test.code) || !reflect.DeepEqual(result.State, before) || node.calls != 1 {
			t.Fatalf("%s: Resume() = %#v, %v, calls=%d", test.name, result, resumeErr, node.calls)
		}
	}

	expiredRunner := mustRunner(t, []Node[runnerData]{node}, RunnerOptions{Now: func() time.Time { return point.ExpiresAt }})
	before := suspended.State
	result, resumeErr := expiredRunner.Resume(context.Background(), before, valid)
	if !IsCode(resumeErr, CodeWaitExpired) || !reflect.DeepEqual(result.State, before) || node.calls != 1 {
		t.Fatalf("expired Resume() = %#v, %v, calls=%d", result, resumeErr, node.calls)
	}

	deadlineState := suspended.State
	deadlineState.Budget.Deadline = now
	result, resumeErr = runner.Resume(context.Background(), deadlineState, valid)
	if !IsCode(resumeErr, CodeTimeout) || !reflect.DeepEqual(result.State, deadlineState) || node.calls != 1 {
		t.Fatalf("deadline Resume() = %#v, %v, calls=%d", result, resumeErr, node.calls)
	}
}

func TestRunnerEnforcesStepAndResumeBudgets(t *testing.T) {
	first := &functionNode{id: "first", execute: continueNode("first", 0)}
	second := &functionNode{id: "second", execute: continueNode("second", 0)}
	runner := mustRunner(t, []Node[runnerData]{first, second}, RunnerOptions{})
	state := runnerState(runnerData{})
	state.Budget.MaxSteps = 1
	result, err := runner.Run(context.Background(), state)
	if !IsCode(err, CodeStepBudget) || result.Status != RunFailed || first.calls != 1 || second.calls != 0 {
		t.Fatalf("step budget Run() = %#v, %v, calls=%d/%d", result, err, first.calls, second.calls)
	}

	now := time.Now().UTC()
	wait := WaitPoint(runnerWait(now, "first"))
	suspended := runnerStateAt(runnerData{}, now)
	suspended.Control = ControlState{Status: RunSuspended, CurrentNode: "first", PendingWait: &wait, ResumeCount: 1, StepsExecuted: 1}
	suspended.Budget.MaxResumes = 1
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	before := suspended
	result, err = runner.Resume(context.Background(), suspended, command)
	if !IsCode(err, CodeResumeBudget) || !reflect.DeepEqual(result.State, before) || first.calls != 1 {
		t.Fatalf("resume budget Resume() = %#v, %v, calls=%d", result, err, first.calls)
	}

	suspended.Budget.MaxResumes = 2
	suspended.Budget.MaxSteps = 1
	before = suspended
	result, err = runner.Resume(context.Background(), suspended, command)
	if !IsCode(err, CodeStepBudget) || !reflect.DeepEqual(result.State, before) || first.calls != 1 {
		t.Fatalf("resume step budget Resume() = %#v, %v, calls=%d", result, err, first.calls)
	}
}

func continueNode(name string, increment int) func(context.Context, NodeInput[runnerData]) (NodeResult[runnerData], error) {
	return func(_ context.Context, input NodeInput[runnerData]) (NodeResult[runnerData], error) {
		input.State.Data.Value += increment
		if name != "" {
			input.State.Data.Order = append(input.State.Data.Order, name)
		}
		return NodeResult[runnerData]{State: input.State, Directive: DirectiveContinue}, nil
	}
}

func runnerState(data runnerData) WorkflowState[runnerData] {
	return runnerStateAt(data, time.Now().UTC())
}

func runnerStateAt(data runnerData, now time.Time) WorkflowState[runnerData] {
	return WorkflowState[runnerData]{
		Meta:    RunMetadata{WorkflowID: "workflow", DefinitionVersion: "v1", RunID: "run-1", StartedAt: now},
		Control: ControlState{Status: RunPending},
		Data:    data,
	}
}

func runnerWait(now time.Time, nodeID NodeID) WaitRequest {
	return WaitRequest{
		ID: "wait-1", RunID: "run-1", NodeID: nodeID, Kind: WaitApproval, Version: 1,
		ContentHash: "hash", AllowedActions: []HumanAction{ActionApprove, ActionReject}, ExpiresAt: now.Add(time.Hour),
	}
}

func mustRunner(t *testing.T, nodes []Node[runnerData], options RunnerOptions) *Runner[runnerData] {
	t.Helper()
	runner, err := NewRunner(nodes, options)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}
	return runner
}

func assertEventTypes(t *testing.T, events []NodeEvent, want ...NodeEventType) {
	t.Helper()
	got := make([]NodeEventType, len(events))
	for index, event := range events {
		got[index] = event.Type
		if event.Sequence != int64(index+1) {
			t.Fatalf("event sequence at %d = %d", index, event.Sequence)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
}
