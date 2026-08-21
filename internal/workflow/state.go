package workflow

import "time"

type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSuspended RunStatus = "suspended"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunExpired   RunStatus = "expired"
)

func (s RunStatus) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunSuspended, RunCompleted, RunFailed, RunCancelled, RunExpired:
		return true
	default:
		return false
	}
}

func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCancelled || s == RunExpired
}

type RunMetadata struct {
	WorkflowID        WorkflowID        `json:"workflow_id"`
	DefinitionVersion DefinitionVersion `json:"definition_version"`
	RunID             WorkflowRunID     `json:"run_id"`
	Source            SourceRef         `json:"source,omitempty"`
	StartedAt         time.Time         `json:"started_at"`
}

func (m RunMetadata) Validate() error {
	if !validIdentifier(string(m.WorkflowID)) || !validIdentifier(string(m.DefinitionVersion)) || !validIdentifier(string(m.RunID)) {
		return contractError("workflow id, definition version, and run id are required", nil)
	}
	if m.StartedAt.IsZero() {
		return contractError("workflow start time is required", nil)
	}
	return m.Source.Validate()
}

type BudgetState struct {
	MaxSteps   int       `json:"max_steps,omitempty"`
	MaxResumes int       `json:"max_resumes,omitempty"`
	Deadline   time.Time `json:"deadline,omitempty"`
}

func (b BudgetState) Validate() error {
	if b.MaxSteps < 0 || b.MaxResumes < 0 {
		return contractError("workflow budgets cannot be negative", nil)
	}
	return nil
}

type ControlState struct {
	Status         RunStatus  `json:"status"`
	CurrentNode    NodeID     `json:"current_node,omitempty"`
	CompletedNodes []NodeID   `json:"completed_nodes,omitempty"`
	PendingWait    *WaitPoint `json:"pending_wait,omitempty"`
	StateVersion   uint64     `json:"state_version"`
	StepsExecuted  int        `json:"steps_executed"`
	ResumeCount    int        `json:"resume_count"`
	EventSequence  int64      `json:"event_sequence"`
	CurrentAttempt int        `json:"current_attempt"`
}

func (s *ControlState) Transition(next RunStatus) error {
	if s == nil || !s.Status.Valid() || !next.Valid() || !allowedTransition(s.Status, next) {
		return stateError("invalid workflow state transition", nil)
	}
	s.Status = next
	s.StateVersion++
	return nil
}

func (s *ControlState) touch() { s.StateVersion++ }

func allowedTransition(from, to RunStatus) bool {
	switch from {
	case RunPending:
		return to == RunRunning || to == RunCancelled || to == RunExpired
	case RunRunning:
		return to == RunSuspended || to == RunCompleted || to == RunFailed || to == RunCancelled || to == RunExpired
	case RunSuspended:
		return to == RunRunning || to == RunFailed || to == RunCancelled || to == RunExpired
	default:
		return false
	}
}

type WorkflowState[T any] struct {
	Meta    RunMetadata  `json:"meta"`
	Control ControlState `json:"control"`
	Budget  BudgetState  `json:"budget"`
	Data    T            `json:"data"`
}

func (s WorkflowState[T]) Validate() error {
	if err := s.Meta.Validate(); err != nil {
		return err
	}
	if err := s.Budget.Validate(); err != nil {
		return err
	}
	if !s.Control.Status.Valid() {
		return stateError("invalid workflow status", nil)
	}
	if s.Control.StepsExecuted < 0 || s.Control.ResumeCount < 0 || s.Control.EventSequence < 0 || s.Control.CurrentAttempt < 0 {
		return stateError("workflow counters cannot be negative", nil)
	}
	if s.Control.Status == RunSuspended {
		if s.Control.PendingWait == nil || !validIdentifier(string(s.Control.CurrentNode)) {
			return stateError("suspended workflow requires a current node and pending wait point", nil)
		}
	} else if s.Control.PendingWait != nil {
		return stateError("only suspended workflow can contain a pending wait point", nil)
	}
	return nil
}
