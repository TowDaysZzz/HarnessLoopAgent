package workflow

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"
)

type DurableRuntimeOptions struct {
	LeaseDuration      time.Duration
	MaxCheckpointBytes int
	Now                func() time.Time
	NewClaimToken      func() string
}

type DurableRuntime[T any] struct {
	store              DurableStore
	nodes              []Node[T]
	codec              StateCodec[T]
	definition         DefinitionVersion
	leaseDuration      time.Duration
	maxCheckpointBytes int
	now                func() time.Time
	newClaimToken      func() string
}

func NewDurableRuntime[T any](store DurableStore, nodes []Node[T], definition DefinitionVersion, codec StateCodec[T], options DurableRuntimeOptions) (*DurableRuntime[T], error) {
	if store == nil || !validIdentifier(string(definition)) || codec == nil {
		return nil, contractError("durable runtime requires store, definition, and codec", nil)
	}
	if _, err := NewRunner(nodes, RunnerOptions{}); err != nil {
		return nil, err
	}
	lease := options.LeaseDuration
	if lease < MinLeaseDuration || lease > MaxLeaseDuration {
		return nil, contractError("durable runtime lease is out of bounds", nil)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newToken := options.NewClaimToken
	if newToken == nil {
		newToken = randomClaimToken
	}
	return &DurableRuntime[T]{store: store, nodes: append([]Node[T](nil), nodes...), codec: codec, definition: definition, leaseDuration: lease, maxCheckpointBytes: options.MaxCheckpointBytes, now: now, newClaimToken: newToken}, nil
}

type StartWorkflowInput[T any] struct {
	Owner          WorkflowOwner
	IdempotencyKey string
	State          WorkflowState[T]
}

func (r *DurableRuntime[T]) Start(ctx context.Context, input StartWorkflowInput[T]) (RunResult[T], error) {
	if err := input.Owner.Validate(); err != nil {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, err
	}
	if !validIdentifier(input.IdempotencyKey) {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, contractError("workflow idempotency key is required", nil)
	}
	if input.State.Meta.DefinitionVersion != r.definition {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, &Error{Code: CodeCodecIncompatible, Message: "workflow definition version is incompatible"}
	}
	envelope, err := EncodeCheckpoint(input.State, r.codec, r.maxCheckpointBytes)
	if err != nil {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, err
	}
	now := r.now().UTC()
	stored := StoredWorkflow{Owner: input.Owner, IdempotencyKey: input.IdempotencyKey, Checkpoint: envelope, CreatedAt: now, UpdatedAt: now}
	existing, created, err := r.store.CreateRun(ctx, CreateStoredRun{Run: stored})
	if err != nil {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, err
	}
	if !created {
		state, decodeErr := DecodeCheckpoint(existing.Checkpoint, r.definition, r.codec)
		if decodeErr != nil || state.Control.Status != RunPending {
			return RunResult[T]{State: state, Status: state.Control.Status}, decodeErr
		}
		stored = existing
	}
	claim, err := r.newClaim(now)
	if err != nil {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, err
	}
	claimed, err := r.store.ClaimRun(ctx, ClaimRunRequest{Owner: input.Owner, RunID: stored.Checkpoint.Meta.RunID, ExpectedStateVersion: stored.Checkpoint.Control.StateVersion, Claim: claim, Now: now})
	if err != nil {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, err
	}
	state, err := DecodeCheckpoint(claimed.Checkpoint, r.definition, r.codec)
	if err != nil {
		return RunResult[T]{State: input.State, Status: input.State.Control.Status}, err
	}
	return r.execute(ctx, input.Owner, claimed, state, nil, ActorRef{})
}

func randomClaimToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", value[:])
}

func (r *DurableRuntime[T]) Resume(ctx context.Context, owner WorkflowOwner, actor ActorRef, runID WorkflowRunID, command ResumeCommand) (RunResult[T], error) {
	stored, err := r.store.GetRun(ctx, owner, runID)
	if err != nil {
		return RunResult[T]{}, err
	}
	state, err := DecodeCheckpoint(stored.Checkpoint, r.definition, r.codec)
	if err != nil {
		return RunResult[T]{Status: stored.Checkpoint.Control.Status}, err
	}
	if state.Control.Status != RunSuspended || state.Control.PendingWait == nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, stateError("workflow run is not suspended", nil)
	}
	now := r.now().UTC()
	if err := state.Control.PendingWait.ValidateResume(command, now); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	if err := actor.Validate(); err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	claim, err := r.newClaim(now)
	if err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	claimed, err := r.store.ClaimWait(ctx, ClaimWaitRequest{Owner: owner, RunID: runID, ExpectedStateVersion: state.Control.StateVersion, Command: command, Actor: actor, Claim: claim, Now: now})
	if err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	claimedState, err := DecodeCheckpoint(claimed.Checkpoint, r.definition, r.codec)
	if err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	return r.execute(ctx, owner, claimed, claimedState, &command, actor)
}

func (r *DurableRuntime[T]) Renew(ctx context.Context, owner WorkflowOwner, runID WorkflowRunID, token string) error {
	now := r.now().UTC()
	return r.store.RenewClaim(ctx, RenewClaimRequest{Owner: owner, RunID: runID, Token: token, Now: now, Until: now.Add(r.leaseDuration)})
}

func (r *DurableRuntime[T]) execute(ctx context.Context, owner WorkflowOwner, claimed StoredWorkflow, state WorkflowState[T], command *ResumeCommand, actor ActorRef) (RunResult[T], error) {
	if command != nil {
		ctx = WithResolvedActor(ctx, actor)
	}
	collector := NewMemoryCollector()
	runner, err := NewRunner(r.nodes, RunnerOptions{Observer: collector, Now: r.now})
	if err != nil {
		return RunResult[T]{State: state, Status: state.Control.Status}, err
	}
	var result RunResult[T]
	var runErr error
	if command == nil {
		result, runErr = runner.Run(ctx, state)
	} else {
		result, runErr = runner.Resume(ctx, state, *command)
	}
	envelope, encodeErr := EncodeCheckpoint(result.State, r.codec, r.maxCheckpointBytes)
	if encodeErr != nil {
		return result, encodeErr
	}
	request := CommitExecutionRequest{Owner: owner, RunID: state.Meta.RunID, Token: claimed.Claim.Token, ExpectedStateVersion: state.Control.StateVersion, Checkpoint: envelope, Events: collector.Events(), Now: r.now().UTC()}
	if command != nil {
		request.ResolvedWaitID = command.WaitID
		request.ResolvedAction = command.Action
		request.Actor = &actor
	}
	if commitErr := r.store.CommitExecution(ctx, request); commitErr != nil {
		return result, commitErr
	}
	return result, runErr
}

func (r *DurableRuntime[T]) newClaim(now time.Time) (Claim, error) {
	claim := Claim{Token: r.newClaimToken(), LeaseUntil: now.Add(r.leaseDuration)}
	if err := claim.Validate(now); err != nil {
		return Claim{}, err
	}
	return claim, nil
}
