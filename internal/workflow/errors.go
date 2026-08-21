package workflow

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	CodeInvalidContract ErrorCode = "workflow_invalid_contract"
	CodeInvalidState    ErrorCode = "workflow_invalid_state"
	CodeStepBudget      ErrorCode = "workflow_step_budget_exceeded"
	CodeResumeBudget    ErrorCode = "workflow_resume_budget_exceeded"
	CodeTimeout         ErrorCode = "workflow_timeout"
	CodeCancelled       ErrorCode = "workflow_cancelled"
	CodeNodeFailed      ErrorCode = "workflow_node_failed"
	CodeObserverFailed  ErrorCode = "workflow_observer_failed"
	CodeInvalidResume   ErrorCode = "workflow_invalid_resume"
	CodeWaitExpired     ErrorCode = "workflow_wait_expired"
)

type Error struct {
	Code    ErrorCode
	NodeID  NodeID
	Message string
	Err     error
}

func (e *Error) Error() string {
	message := e.Message
	if message == "" {
		message = string(e.Code)
	}
	if e.NodeID != "" {
		message = fmt.Sprintf("node %s: %s", e.NodeID, message)
	}
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error { return e.Err }

func CodeOf(err error) ErrorCode {
	var workflowErr *Error
	if errors.As(err, &workflowErr) {
		return workflowErr.Code
	}
	return ""
}

func IsCode(err error, code ErrorCode) bool { return CodeOf(err) == code }

func contractError(message string, err error) error {
	return &Error{Code: CodeInvalidContract, Message: message, Err: err}
}

func stateError(message string, err error) error {
	return &Error{Code: CodeInvalidState, Message: message, Err: err}
}
