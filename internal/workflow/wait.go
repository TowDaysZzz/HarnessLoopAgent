package workflow

import "time"

type WaitKind string

const (
	WaitApproval WaitKind = "approval"
	WaitReview   WaitKind = "review"
	WaitEdit     WaitKind = "edit"
	WaitInput    WaitKind = "input"
)

func (k WaitKind) Valid() bool {
	return k == WaitApproval || k == WaitReview || k == WaitEdit || k == WaitInput
}

type HumanAction string

const (
	ActionApprove    HumanAction = "approve"
	ActionReject     HumanAction = "reject"
	ActionSubmitEdit HumanAction = "submit_edit"
	ActionCancel     HumanAction = "cancel"
)

func (a HumanAction) Valid() bool {
	return a == ActionApprove || a == ActionReject || a == ActionSubmitEdit || a == ActionCancel
}

type WaitRequest struct {
	ID             WaitID        `json:"wait_id"`
	RunID          WorkflowRunID `json:"run_id"`
	NodeID         NodeID        `json:"node_id"`
	Kind           WaitKind      `json:"kind"`
	Version        uint64        `json:"version"`
	ContentHash    string        `json:"content_hash"`
	AllowedActions []HumanAction `json:"allowed_actions"`
	PayloadRef     string        `json:"payload_ref,omitempty"`
	ExpiresAt      time.Time     `json:"expires_at"`
}

func (r WaitRequest) Validate(now time.Time) error {
	if !validIdentifier(string(r.ID)) || !validIdentifier(string(r.RunID)) || !validIdentifier(string(r.NodeID)) || r.Version == 0 || !validIdentifier(r.ContentHash) {
		return contractError("wait request identity, version, and content hash are required", nil)
	}
	if !r.Kind.Valid() {
		return contractError("invalid wait kind", nil)
	}
	if len(r.AllowedActions) == 0 {
		return contractError("wait request requires at least one action", nil)
	}
	seen := make(map[HumanAction]struct{}, len(r.AllowedActions))
	for _, action := range r.AllowedActions {
		if !action.Valid() {
			return contractError("invalid human action", nil)
		}
		if _, exists := seen[action]; exists {
			return contractError("duplicate human action", nil)
		}
		seen[action] = struct{}{}
	}
	if r.ExpiresAt.IsZero() || !r.ExpiresAt.After(now) {
		return contractError("wait request expiry must be in the future", nil)
	}
	return nil
}

type WaitPoint WaitRequest

func (p WaitPoint) Validate(now time.Time) error { return WaitRequest(p).Validate(now) }

type ResumeCommand struct {
	RunID       WorkflowRunID `json:"run_id"`
	WaitID      WaitID        `json:"wait_id"`
	Version     uint64        `json:"version"`
	ContentHash string        `json:"content_hash"`
	Action      HumanAction   `json:"action"`
	PayloadRef  string        `json:"payload_ref,omitempty"`
}

func (p WaitPoint) ValidateResume(command ResumeCommand, now time.Time) error {
	if !p.ExpiresAt.After(now) {
		return &Error{Code: CodeWaitExpired, Message: "workflow wait point expired"}
	}
	if err := p.Validate(now); err != nil {
		return &Error{Code: CodeInvalidResume, Message: "pending wait point is invalid", Err: err}
	}
	if command.RunID != p.RunID || command.WaitID != p.ID || command.Version != p.Version || command.ContentHash != p.ContentHash {
		return &Error{Code: CodeInvalidResume, Message: "resume command does not match the pending wait point"}
	}
	if !command.Action.Valid() || !p.allows(command.Action) {
		return &Error{Code: CodeInvalidResume, Message: "resume action is not allowed"}
	}
	return nil
}

func (p WaitPoint) allows(action HumanAction) bool {
	for _, allowed := range p.AllowedActions {
		if action == allowed {
			return true
		}
	}
	return false
}
