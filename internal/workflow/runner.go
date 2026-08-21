package workflow

import (
	"context"
	"errors"
	"reflect"
	"time"
)

type RunnerOptions struct {
	Observer Observer
	Now      func() time.Time
}

type Runner[T any] struct {
	nodes    []Node[T]
	indexes  map[NodeID]int
	observer Observer
	now      func() time.Time
}

func NewRunner[T any](nodes []Node[T], options RunnerOptions) (*Runner[T], error) {
	if len(nodes) == 0 {
		return nil, contractError("workflow requires at least one node", nil)
	}
	indexes := make(map[NodeID]int, len(nodes))
	owned := make([]Node[T], len(nodes))
	copy(owned, nodes)
	for index, node := range owned {
		if nilInterface(node) {
			return nil, contractError("workflow contains nil node", nil)
		}
		id := node.ID()
		if !validIdentifier(string(id)) {
			return nil, contractError("workflow node id is required", nil)
		}
		if _, exists := indexes[id]; exists {
			return nil, contractError("workflow node ids must be unique", nil)
		}
		indexes[id] = index
	}
	observer := options.Observer
	if observer == nil {
		observer = NoopObserver{}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Runner[T]{nodes: owned, indexes: indexes, observer: observer, now: now}, nil
}

func (r *Runner[T]) Run(ctx context.Context, state WorkflowState[T]) (RunResult[T], error) {
	if r == nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, contractError("nil workflow runner", nil)
	}
	if err := state.Validate(); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	if state.Control.Status != RunPending || state.Control.PendingWait != nil || state.Control.CurrentNode != "" || len(state.Control.CompletedNodes) != 0 || state.Control.StepsExecuted != 0 || state.Control.ResumeCount != 0 {
		return RunResult[T]{State: state, Status: state.Control.Status}, stateError("new workflow run must be pending with empty control progress", nil)
	}
	if err := state.Control.Transition(RunRunning); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	return r.executeFrom(ctx, state, 0, nil)
}

func (r *Runner[T]) Resume(ctx context.Context, state WorkflowState[T], command ResumeCommand) (RunResult[T], error) {
	if r == nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, contractError("nil workflow runner", nil)
	}
	if err := state.Validate(); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	if state.Control.Status != RunSuspended || state.Control.PendingWait == nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, stateError("workflow run is not suspended", nil)
	}
	if err := r.contextOrDeadlineError(ctx, state); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	if state.Budget.MaxResumes > 0 && state.Control.ResumeCount >= state.Budget.MaxResumes {
		return RunResult[T]{State: state, Status: state.Control.Status}, &Error{Code: CodeResumeBudget, NodeID: state.Control.CurrentNode, Message: "workflow resume budget exceeded"}
	}
	if state.Budget.MaxSteps > 0 && state.Control.StepsExecuted >= state.Budget.MaxSteps {
		return RunResult[T]{State: state, Status: state.Control.Status}, &Error{Code: CodeStepBudget, NodeID: state.Control.CurrentNode, Message: "workflow step budget exceeded"}
	}
	wait := *state.Control.PendingWait
	if err := wait.ValidateResume(command, r.now().UTC()); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	index, exists := r.indexes[wait.NodeID]
	if !exists || wait.NodeID != state.Control.CurrentNode {
		return RunResult[T]{State: state, Status: state.Control.Status}, &Error{Code: CodeInvalidResume, NodeID: wait.NodeID, Message: "pending wait node is not part of this workflow"}
	}
	eventState := state
	if err := r.emit(ctx, &eventState, EventNodeResumed, wait.NodeID, wait.ID, "", 0); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, &Error{Code: CodeObserverFailed, NodeID: wait.NodeID, Message: "workflow observer failed", Err: err}
	}
	state = eventState
	state.Control.PendingWait = nil
	state.Control.ResumeCount++
	if err := state.Control.Transition(RunRunning); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	return r.executeFrom(ctx, state, index, &command)
}

func (r *Runner[T]) executeFrom(ctx context.Context, state WorkflowState[T], start int, resume *ResumeCommand) (RunResult[T], error) {
	for index := start; index < len(r.nodes); index++ {
		node := r.nodes[index]
		if err := r.contextOrDeadlineError(ctx, state); err != nil {
			return r.stopBeforeNode(state, err)
		}
		if state.Budget.MaxSteps > 0 && state.Control.StepsExecuted >= state.Budget.MaxSteps {
			return r.stopBeforeNode(state, &Error{Code: CodeStepBudget, NodeID: node.ID(), Message: "workflow step budget exceeded"})
		}

		if state.Control.CurrentNode != node.ID() {
			state.Control.CurrentAttempt = 0
		}
		state.Control.CurrentNode = node.ID()
		state.Control.CurrentAttempt++
		state.Control.StepsExecuted++
		state.Control.touch()
		startedAt := r.now().UTC()
		if err := r.emit(ctx, &state, EventNodeStarted, node.ID(), "", "", 0); err != nil {
			return r.observerFailure(state, node.ID(), err)
		}

		output, err := node.Execute(ctx, NodeInput[T]{State: state, Resume: resume})
		resume = nil
		duration := r.now().UTC().Sub(startedAt)
		if duration < 0 {
			duration = 0
		}
		if stopErr := r.contextOrDeadlineError(ctx, state); stopErr != nil {
			return r.nodeFailure(ctx, state, node.ID(), stopErr, duration)
		}
		if err != nil {
			return r.nodeFailure(ctx, state, node.ID(), err, duration)
		}
		if !output.Directive.Valid() {
			return r.nodeFailure(ctx, state, node.ID(), contractError("node returned invalid directive", nil), duration)
		}

		switch output.Directive {
		case DirectiveContinue:
			if output.Wait != nil {
				return r.nodeFailure(ctx, state, node.ID(), contractError("continue directive cannot include wait request", nil), duration)
			}
			state.Data = output.State.Data
			state.Control.CompletedNodes = append(state.Control.CompletedNodes, node.ID())
			state.Control.touch()
			if err := r.emit(ctx, &state, EventNodeCompleted, node.ID(), "", "", duration); err != nil {
				return r.observerFailure(state, node.ID(), err)
			}
		case DirectiveSuspend:
			if output.Wait == nil {
				return r.nodeFailure(ctx, state, node.ID(), contractError("suspend directive requires wait request", nil), duration)
			}
			if err := output.Wait.Validate(r.now().UTC()); err != nil {
				return r.nodeFailure(ctx, state, node.ID(), err, duration)
			}
			if output.Wait.RunID != state.Meta.RunID || output.Wait.NodeID != node.ID() {
				return r.nodeFailure(ctx, state, node.ID(), contractError("wait request must be bound to the current run and node", nil), duration)
			}
			state.Data = output.State.Data
			point := WaitPoint(*output.Wait)
			state.Control.PendingWait = &point
			if err := state.Control.Transition(RunSuspended); err != nil {
				return RunResult[T]{State: state, Status: state.Control.Status}, err
			}
			if err := r.emit(ctx, &state, EventNodeSuspended, node.ID(), point.ID, "", duration); err != nil {
				return r.observerFailure(state, node.ID(), err)
			}
			return RunResult[T]{State: state, Status: RunSuspended}, nil
		}
	}
	if err := state.Control.Transition(RunCompleted); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	return RunResult[T]{State: state, Status: RunCompleted}, nil
}

func (r *Runner[T]) contextOrDeadlineError(ctx context.Context, state WorkflowState[T]) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Code: CodeTimeout, NodeID: state.Control.CurrentNode, Message: "workflow context deadline exceeded", Err: err}
		}
		return &Error{Code: CodeCancelled, NodeID: state.Control.CurrentNode, Message: "workflow context cancelled", Err: err}
	}
	if !state.Budget.Deadline.IsZero() && !r.now().UTC().Before(state.Budget.Deadline) {
		return &Error{Code: CodeTimeout, NodeID: state.Control.CurrentNode, Message: "workflow deadline exceeded", Err: context.DeadlineExceeded}
	}
	return nil
}

func (r *Runner[T]) stopBeforeNode(state WorkflowState[T], err error) (RunResult[T], error) {
	target := statusForError(err)
	if transitionErr := state.Control.Transition(target); transitionErr != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, errors.Join(err, transitionErr)
	}
	return RunResult[T]{State: state, Status: target}, err
}

func (r *Runner[T]) nodeFailure(ctx context.Context, state WorkflowState[T], nodeID NodeID, cause error, duration time.Duration) (RunResult[T], error) {
	err := normalizeNodeError(nodeID, cause)
	target := statusForError(err)
	if transitionErr := state.Control.Transition(target); transitionErr != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, errors.Join(err, transitionErr)
	}
	if observeErr := r.emit(ctx, &state, EventNodeFailed, nodeID, "", CodeOf(err), duration); observeErr != nil {
		return RunResult[T]{State: state, Status: target}, errors.Join(err, observeErr)
	}
	return RunResult[T]{State: state, Status: target}, err
}

func (r *Runner[T]) observerFailure(state WorkflowState[T], nodeID NodeID, cause error) (RunResult[T], error) {
	err := &Error{Code: CodeObserverFailed, NodeID: nodeID, Message: "workflow observer failed", Err: cause}
	if state.Control.Status == RunRunning || state.Control.Status == RunSuspended {
		_ = state.Control.Transition(RunFailed)
	}
	state.Control.PendingWait = nil
	return RunResult[T]{State: state, Status: state.Control.Status}, err
}

func (r *Runner[T]) emit(ctx context.Context, state *WorkflowState[T], eventType NodeEventType, nodeID NodeID, waitID WaitID, errorCode ErrorCode, duration time.Duration) error {
	state.Control.EventSequence++
	state.Control.touch()
	event := NodeEvent{
		Sequence: state.Control.EventSequence, WorkflowID: state.Meta.WorkflowID, RunID: state.Meta.RunID,
		NodeID: nodeID, Type: eventType, Status: state.Control.Status, Attempt: state.Control.CurrentAttempt,
		ResumeCount: state.Control.ResumeCount, WaitID: waitID, ErrorCode: errorCode, Duration: duration, OccurredAt: r.now().UTC(),
	}
	if err := r.observer.Observe(ctx, event); err != nil {
		return err
	}
	return nil
}

func normalizeNodeError(nodeID NodeID, err error) error {
	if err == nil {
		return &Error{Code: CodeNodeFailed, NodeID: nodeID, Message: "workflow node failed"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCancelled, NodeID: nodeID, Message: "workflow node cancelled", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, NodeID: nodeID, Message: "workflow node timed out", Err: err}
	}
	var workflowErr *Error
	if errors.As(err, &workflowErr) {
		if workflowErr.NodeID == "" {
			copy := *workflowErr
			copy.NodeID = nodeID
			return &copy
		}
		return err
	}
	return &Error{Code: CodeNodeFailed, NodeID: nodeID, Message: "workflow node failed", Err: err}
}

func statusForError(err error) RunStatus {
	switch CodeOf(err) {
	case CodeCancelled:
		return RunCancelled
	case CodeTimeout:
		return RunExpired
	default:
		return RunFailed
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	ref := reflect.ValueOf(value)
	switch ref.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return ref.IsNil()
	default:
		return false
	}
}
