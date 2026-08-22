package workflow

import (
	"context"
	"reflect"
	"sync"
)

type MemoryDurableStore struct {
	mu     sync.Mutex
	runs   map[WorkflowRunID]StoredWorkflow
	waits  map[WorkflowRunID][]StoredWait
	events map[WorkflowRunID][]NodeEvent
}

func NewMemoryDurableStore() *MemoryDurableStore {
	return &MemoryDurableStore{
		runs: make(map[WorkflowRunID]StoredWorkflow), waits: make(map[WorkflowRunID][]StoredWait), events: make(map[WorkflowRunID][]NodeEvent),
	}
}

func (s *MemoryDurableStore) CreateRun(_ context.Context, input CreateStoredRun) (StoredWorkflow, bool, error) {
	if s == nil {
		return StoredWorkflow{}, false, contractError("nil durable store", nil)
	}
	if err := input.Run.Validate(); err != nil {
		return StoredWorkflow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runs {
		if existing.Owner == input.Run.Owner && existing.Checkpoint.Meta.WorkflowID == input.Run.Checkpoint.Meta.WorkflowID && existing.IdempotencyKey == input.Run.IdempotencyKey {
			if sameStartRequest(existing, input.Run) {
				return cloneStoredWorkflow(existing), false, nil
			}
			return StoredWorkflow{}, false, &Error{Code: CodeIdempotencyConflict, Message: "workflow idempotency key was reused with different input"}
		}
	}
	runID := input.Run.Checkpoint.Meta.RunID
	if _, exists := s.runs[runID]; exists {
		return StoredWorkflow{}, false, &Error{Code: CodeIdempotencyConflict, Message: "workflow run id already exists"}
	}
	s.runs[runID] = cloneStoredWorkflow(input.Run)
	return cloneStoredWorkflow(input.Run), true, nil
}

func (s *MemoryDurableStore) GetRun(_ context.Context, owner WorkflowOwner, runID WorkflowRunID) (StoredWorkflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || run.Owner != owner {
		return StoredWorkflow{}, &Error{Code: CodeNotFound, Message: "workflow run not found"}
	}
	return cloneStoredWorkflow(run), nil
}

func (s *MemoryDurableStore) GetCurrentWait(_ context.Context, owner WorkflowOwner, runID WorkflowRunID) (StoredWait, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || run.Owner != owner {
		return StoredWait{}, &Error{Code: CodeNotFound, Message: "workflow wait not found"}
	}
	waits := s.waits[runID]
	for index := len(waits) - 1; index >= 0; index-- {
		if !waits[index].Status.Terminal() {
			return cloneStoredWait(waits[index]), nil
		}
	}
	return StoredWait{}, &Error{Code: CodeNotFound, Message: "workflow wait not found"}
}

func (s *MemoryDurableStore) ClaimRun(_ context.Context, request ClaimRunRequest) (StoredWorkflow, error) {
	if err := request.Owner.Validate(); err != nil {
		return StoredWorkflow{}, err
	}
	if request.Now.IsZero() {
		return StoredWorkflow{}, contractError("claim time is required", nil)
	}
	if err := request.Claim.Validate(request.Now); err != nil {
		return StoredWorkflow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[request.RunID]
	if !ok || run.Owner != request.Owner {
		return StoredWorkflow{}, &Error{Code: CodeNotFound, Message: "workflow run not found"}
	}
	if run.Checkpoint.Control.Status != RunPending || run.Checkpoint.Control.StateVersion != request.ExpectedStateVersion {
		return StoredWorkflow{}, &Error{Code: CodeStateConflict, Message: "workflow state changed before claim"}
	}
	if run.Claim != nil && run.Claim.LeaseUntil.After(request.Now) {
		return StoredWorkflow{}, &Error{Code: CodeClaimConflict, Message: "workflow run is already claimed"}
	}
	run.Claim = cloneClaim(&request.Claim)
	run.UpdatedAt = request.Now
	s.runs[request.RunID] = run
	return cloneStoredWorkflow(run), nil
}

func (s *MemoryDurableStore) ClaimWait(_ context.Context, request ClaimWaitRequest) (StoredWorkflow, error) {
	if err := request.Owner.Validate(); err != nil {
		return StoredWorkflow{}, err
	}
	if err := request.Actor.Validate(); err != nil {
		return StoredWorkflow{}, err
	}
	if request.Now.IsZero() {
		return StoredWorkflow{}, contractError("claim time is required", nil)
	}
	if err := request.Claim.Validate(request.Now); err != nil {
		return StoredWorkflow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[request.RunID]
	if !ok || run.Owner != request.Owner {
		return StoredWorkflow{}, &Error{Code: CodeNotFound, Message: "workflow run not found"}
	}
	if run.Checkpoint.Control.Status != RunSuspended || run.Checkpoint.Control.StateVersion != request.ExpectedStateVersion {
		return StoredWorkflow{}, &Error{Code: CodeStateConflict, Message: "workflow state changed before resume claim"}
	}
	waits := s.waits[request.RunID]
	if len(waits) == 0 {
		return StoredWorkflow{}, &Error{Code: CodeNotFound, Message: "workflow wait not found"}
	}
	index := len(waits) - 1
	wait := waits[index]
	if wait.Status.Terminal() {
		return StoredWorkflow{}, &Error{Code: CodeStateConflict, Message: "workflow wait is already resolved"}
	}
	if wait.Status == WaitProcessing && wait.Claim != nil && wait.Claim.LeaseUntil.After(request.Now) {
		return StoredWorkflow{}, &Error{Code: CodeClaimConflict, Message: "workflow wait is already claimed"}
	}
	if err := wait.Point.ValidateResume(request.Command, request.Now); err != nil {
		return StoredWorkflow{}, err
	}
	wait.Status = WaitProcessing
	wait.RecordVersion++
	wait.Claim = cloneClaim(&request.Claim)
	waits[index] = wait
	s.waits[request.RunID] = waits
	run.Claim = cloneClaim(&request.Claim)
	run.UpdatedAt = request.Now
	s.runs[request.RunID] = run
	return cloneStoredWorkflow(run), nil
}

func (s *MemoryDurableStore) RenewClaim(_ context.Context, request RenewClaimRequest) error {
	if !request.Until.After(request.Now) || request.Until.Sub(request.Now) > MaxLeaseDuration {
		return contractError("renewed lease must be bounded and in the future", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[request.RunID]
	if !ok || run.Owner != request.Owner {
		return &Error{Code: CodeNotFound, Message: "workflow run not found"}
	}
	if run.Claim == nil || run.Claim.Token != request.Token || !run.Claim.LeaseUntil.After(request.Now) {
		return &Error{Code: CodeLeaseLost, Message: "workflow claim lease was lost"}
	}
	run.Claim.LeaseUntil = request.Until
	run.UpdatedAt = request.Now
	s.runs[request.RunID] = run
	waits := s.waits[request.RunID]
	if len(waits) > 0 {
		index := len(waits) - 1
		if waits[index].Status == WaitProcessing && waits[index].Claim != nil && waits[index].Claim.Token == request.Token {
			waits[index].Claim.LeaseUntil = request.Until
			s.waits[request.RunID] = waits
		}
	}
	return nil
}

func (s *MemoryDurableStore) CommitExecution(_ context.Context, request CommitExecutionRequest) error {
	if request.Now.IsZero() || !validIdentifier(request.Token) {
		return contractError("commit token and time are required", nil)
	}
	if err := request.Checkpoint.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[request.RunID]
	if !ok || run.Owner != request.Owner {
		return &Error{Code: CodeNotFound, Message: "workflow run not found"}
	}
	if run.Claim == nil || run.Claim.Token != request.Token || !run.Claim.LeaseUntil.After(request.Now) {
		return &Error{Code: CodeLeaseLost, Message: "workflow claim lease was lost"}
	}
	if run.Checkpoint.Control.StateVersion != request.ExpectedStateVersion {
		return &Error{Code: CodeStateConflict, Message: "workflow state changed before commit"}
	}
	if request.Checkpoint.Meta.RunID != request.RunID || request.Checkpoint.Control.StateVersion <= request.ExpectedStateVersion {
		return &Error{Code: CodeStateConflict, Message: "committed checkpoint must advance the claimed run"}
	}
	if err := validateCommittedEvents(run.Checkpoint.Control.EventSequence, request.Checkpoint.Control.EventSequence, request.RunID, request.Events); err != nil {
		return err
	}
	waits := append([]StoredWait(nil), s.waits[request.RunID]...)
	if request.ResolvedWaitID != "" {
		if len(waits) == 0 || waits[len(waits)-1].Point.ID != request.ResolvedWaitID || waits[len(waits)-1].Status != WaitProcessing || waits[len(waits)-1].Claim == nil || waits[len(waits)-1].Claim.Token != request.Token {
			return &Error{Code: CodeStateConflict, Message: "resolved wait does not match claimed wait"}
		}
		if request.Actor == nil || request.Actor.Validate() != nil {
			return contractError("resolved wait requires actor", nil)
		}
		wait := waits[len(waits)-1]
		_ = wait.Transition(WaitResolved)
		wait.ResolvedAction, wait.ResolvedAt = request.ResolvedAction, request.Now
		actor := *request.Actor
		wait.ResolvedBy = &actor
		waits[len(waits)-1] = wait
	}
	if request.Checkpoint.Control.Status == RunSuspended {
		point := request.Checkpoint.Control.PendingWait
		if point == nil {
			return &Error{Code: CodeStateConflict, Message: "suspended checkpoint requires wait"}
		}
		if len(waits) > 0 && waits[len(waits)-1].Point.ID == point.ID {
			return &Error{Code: CodeStateConflict, Message: "new wait id must be unique"}
		}
		waits = append(waits, StoredWait{Point: *point, Status: WaitPending, RecordVersion: 1})
	}
	run.Checkpoint = cloneEnvelope(request.Checkpoint)
	run.Claim = nil
	run.UpdatedAt = request.Now
	s.runs[request.RunID] = run
	s.waits[request.RunID] = waits
	s.events[request.RunID] = append(s.events[request.RunID], append([]NodeEvent(nil), request.Events...)...)
	return nil
}

func (s *MemoryDurableStore) ListNodeEvents(_ context.Context, owner WorkflowOwner, runID WorkflowRunID) ([]NodeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || run.Owner != owner {
		return nil, &Error{Code: CodeNotFound, Message: "workflow run not found"}
	}
	return append([]NodeEvent(nil), s.events[runID]...), nil
}

func validateCommittedEvents(previous, final int64, runID WorkflowRunID, events []NodeEvent) error {
	if len(events) == 0 && final == previous {
		return nil
	}
	if len(events) == 0 || int64(len(events)) != final-previous {
		return &Error{Code: CodeStateConflict, Message: "event batch does not cover checkpoint sequence"}
	}
	for index, event := range events {
		if event.RunID != runID || event.Sequence != previous+int64(index)+1 || !event.Type.Valid() || !validIdentifier(string(event.WorkflowID)) || !validIdentifier(string(event.NodeID)) {
			return &Error{Code: CodeStateConflict, Message: "event batch is not contiguous"}
		}
	}
	return nil
}

func sameStartRequest(left, right StoredWorkflow) bool {
	return left.Checkpoint.Meta.WorkflowID == right.Checkpoint.Meta.WorkflowID &&
		left.Checkpoint.DefinitionVersion == right.Checkpoint.DefinitionVersion &&
		left.Checkpoint.SchemaID == right.Checkpoint.SchemaID && left.Checkpoint.SchemaVersion == right.Checkpoint.SchemaVersion &&
		reflect.DeepEqual(left.Checkpoint.Data, right.Checkpoint.Data) && reflect.DeepEqual(left.Checkpoint.Budget, right.Checkpoint.Budget) && reflect.DeepEqual(left.Checkpoint.Meta.Source, right.Checkpoint.Meta.Source)
}

func cloneClaim(value *Claim) *Claim {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneEnvelope(value CheckpointEnvelope) CheckpointEnvelope {
	value.Data = append([]byte(nil), value.Data...)
	value.Control.CompletedNodes = append([]NodeID(nil), value.Control.CompletedNodes...)
	if value.Control.PendingWait != nil {
		wait := *value.Control.PendingWait
		wait.AllowedActions = append([]HumanAction(nil), wait.AllowedActions...)
		value.Control.PendingWait = &wait
	}
	return value
}

func cloneStoredWorkflow(value StoredWorkflow) StoredWorkflow {
	value.Checkpoint = cloneEnvelope(value.Checkpoint)
	value.Claim = cloneClaim(value.Claim)
	return value
}

func cloneStoredWait(value StoredWait) StoredWait {
	value.Point.AllowedActions = append([]HumanAction(nil), value.Point.AllowedActions...)
	value.Claim = cloneClaim(value.Claim)
	if value.ResolvedBy != nil {
		actor := *value.ResolvedBy
		value.ResolvedBy = &actor
	}
	return value
}

var _ DurableStore = (*MemoryDurableStore)(nil)
