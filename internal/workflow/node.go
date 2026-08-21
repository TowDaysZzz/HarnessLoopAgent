package workflow

import "context"

type Directive string

const (
	DirectiveContinue Directive = "continue"
	DirectiveSuspend  Directive = "suspend"
)

func (d Directive) Valid() bool { return d == DirectiveContinue || d == DirectiveSuspend }

type NodeInput[T any] struct {
	State  WorkflowState[T]
	Resume *ResumeCommand
}

type NodeResult[T any] struct {
	State     WorkflowState[T]
	Directive Directive
	Wait      *WaitRequest
}

type Node[T any] interface {
	ID() NodeID
	Execute(context.Context, NodeInput[T]) (NodeResult[T], error)
}

type RunResult[T any] struct {
	State  WorkflowState[T]
	Status RunStatus
}
