package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type durableData struct {
	Order      []string `json:"order,omitempty"`
	DocumentID string   `json:"document_id,omitempty"`
	Version    uint64   `json:"version,omitempty"`
	Hash       string   `json:"content_hash,omitempty"`
	PayloadRef string   `json:"payload_ref,omitempty"`
	Password   string   `json:"password,omitempty"`
}

type durableNode struct {
	id           NodeID
	calls        atomic.Int64
	executionIDs []string
	mu           sync.Mutex
	execute      func(NodeInput[durableData]) (NodeResult[durableData], error)
}

func (n *durableNode) ID() NodeID { return n.id }
func (n *durableNode) Execute(_ context.Context, input NodeInput[durableData]) (NodeResult[durableData], error) {
	n.calls.Add(1)
	n.mu.Lock()
	n.executionIDs = append(n.executionIDs, input.ExecutionID)
	n.mu.Unlock()
	return n.execute(input)
}

func TestDurableIdentitiesClaimsAndWaitTransitions(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := (WorkflowOwner{TenantID: 1, OwnerID: 2}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []any{WorkflowOwner{}, WorkflowOwner{TenantID: 1}, ActorRef{}, ActorRef{Type: "user"}} {
		var err error
		switch value := invalid.(type) {
		case WorkflowOwner:
			err = value.Validate()
		case ActorRef:
			err = value.Validate()
		}
		if !IsCode(err, CodeInvalidContract) {
			t.Fatalf("Validate(%#v) = %v", invalid, err)
		}
	}
	for _, claim := range []Claim{{}, {Token: "x", LeaseUntil: now}, {Token: "x", LeaseUntil: now.Add(MaxLeaseDuration + time.Second)}} {
		if !IsCode(claim.Validate(now), CodeInvalidContract) {
			t.Fatalf("Claim.Validate(%#v) was accepted", claim)
		}
	}
	wait := StoredWait{Status: WaitPending, RecordVersion: 2}
	if err := wait.Transition(WaitProcessing); err != nil || wait.RecordVersion != 3 {
		t.Fatalf("processing = %#v, %v", wait, err)
	}
	wait.Claim = &Claim{Token: "claim", LeaseUntil: now.Add(time.Minute)}
	if err := wait.Transition(WaitResolved); err != nil || wait.RecordVersion != 4 || wait.Claim != nil {
		t.Fatalf("resolved = %#v, %v", wait, err)
	}
	if err := wait.Transition(WaitPending); !IsCode(err, CodeInvalidState) {
		t.Fatalf("terminal transition = %v", err)
	}
}

func TestDurableErrorCodesSupportErrorsIs(t *testing.T) {
	tests := []struct {
		err, target error
		code        ErrorCode
	}{
		{&Error{Code: CodeNotFound}, ErrNotFound, CodeNotFound},
		{&Error{Code: CodeIdempotencyConflict}, ErrIdempotencyConflict, CodeIdempotencyConflict},
		{&Error{Code: CodeClaimConflict}, ErrClaimConflict, CodeClaimConflict},
		{&Error{Code: CodeLeaseLost}, ErrLeaseLost, CodeLeaseLost},
		{&Error{Code: CodeStateConflict}, ErrStateConflict, CodeStateConflict},
		{&Error{Code: CodeCodecIncompatible}, ErrCodecIncompatible, CodeCodecIncompatible},
		{&Error{Code: CodeCheckpointTooLarge}, ErrCheckpointTooLarge, CodeCheckpointTooLarge},
	}
	for _, test := range tests {
		if !errors.Is(test.err, test.target) || !IsCode(test.err, test.code) {
			t.Fatalf("error %v was not identifiable", test.err)
		}
	}
}

func TestCheckpointCodecRoundTripCompatibilityAndSafety(t *testing.T) {
	state := durableState(durableData{DocumentID: "doc-1", Version: 3, Hash: "hash", PayloadRef: "object:7"})
	codec := JSONStateCodec[durableData]{ID: "review-state", Version: 1, ForbidSecrets: true}
	registry := NewCodecRegistry[durableData]()
	if err := registry.Register(codec); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("review-state", 1); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(codec); !IsCode(err, CodeCodecIncompatible) {
		t.Fatalf("duplicate codec = %v", err)
	}
	if _, err := registry.Resolve("unknown", 1); !IsCode(err, CodeCodecIncompatible) {
		t.Fatalf("unknown codec = %v", err)
	}
	envelope, err := EncodeCheckpoint(state, codec, 1024)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCheckpoint(envelope, "v1", codec)
	if err != nil || !reflect.DeepEqual(decoded, state) {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
	for name, mutate := range map[string]func(*CheckpointEnvelope){
		"schema":         func(v *CheckpointEnvelope) { v.SchemaID = "other" },
		"schema-version": func(v *CheckpointEnvelope) { v.SchemaVersion++ },
		"definition":     func(v *CheckpointEnvelope) { v.DefinitionVersion = "v2"; v.Meta.DefinitionVersion = "v2" },
		"damaged":        func(v *CheckpointEnvelope) { v.Data = []byte("{") },
	} {
		changed := cloneEnvelope(envelope)
		mutate(&changed)
		if _, err := DecodeCheckpoint(changed, "v1", codec); !IsCode(err, CodeCodecIncompatible) {
			t.Fatalf("%s decode = %v", name, err)
		}
	}
	secret := durableState(durableData{Password: "secret"})
	if _, err := EncodeCheckpoint(secret, codec, 1024); !IsCode(err, CodeCodecIncompatible) {
		t.Fatalf("secret encode = %v", err)
	}
	if _, err := EncodeCheckpoint(state, codec, 4); !IsCode(err, CodeCheckpointTooLarge) {
		t.Fatalf("large encode = %v", err)
	}
	if len(envelope.Data) >= 1024 || string(envelope.Data) == "" {
		t.Fatalf("reference checkpoint = %q", envelope.Data)
	}
}

func TestDurableRuntimeStartSuspendResumeAndReplay(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	clock := now
	tokens := 0
	review := &durableNode{id: "review", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		if input.Resume == nil {
			wait := durableWait(clock, input.State.Meta.RunID, "review", "wait-1")
			return NodeResult[durableData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
		}
		input.State.Data.Order = append(input.State.Data.Order, string(input.Resume.Action))
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	finish := &durableNode{id: "finish", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		input.State.Data.Order = append(input.State.Data.Order, "finish")
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	store := NewMemoryDurableStore()
	runtime := durableRuntime(t, store, []*durableNode{review, finish}, func() time.Time { return clock }, func() string { tokens++; return "claim-" + string(rune('0'+tokens)) })
	state := durableState(durableData{})
	start, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "start-1", State: state})
	if err != nil || start.Status != RunSuspended || review.calls.Load() != 1 || finish.calls.Load() != 0 {
		t.Fatalf("Start = %#v, %v calls=%d/%d", start, err, review.calls.Load(), finish.calls.Load())
	}
	replay, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "start-1", State: state})
	if err != nil || replay.Status != RunSuspended || review.calls.Load() != 1 {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	changed := state
	changed.Data.DocumentID = "different"
	if _, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "start-1", State: changed}); !IsCode(err, CodeIdempotencyConflict) {
		t.Fatalf("changed idempotency replay = %v", err)
	}
	wait := start.State.Control.PendingWait
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	completed, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "42"}, state.Meta.RunID, command)
	if err != nil || completed.Status != RunCompleted || !reflect.DeepEqual(completed.State.Data.Order, []string{"approve", "finish"}) {
		t.Fatalf("Resume = %#v, %v", completed, err)
	}
	events, err := store.ListNodeEvents(context.Background(), durableOwner(), state.Meta.RunID)
	if err != nil || len(events) != 7 || events[0].Type != EventNodeStarted || events[2].Type != EventNodeResumed || events[6].Type != EventNodeCompleted {
		t.Fatalf("events = %#v, %v", events, err)
	}
	if _, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "42"}, state.Meta.RunID, command); !IsCode(err, CodeInvalidState) {
		t.Fatalf("repeated resume = %v", err)
	}
	review.mu.Lock()
	identities := append([]string(nil), review.executionIDs...)
	review.mu.Unlock()
	if !reflect.DeepEqual(identities, []string{"durable-run:review:1", "durable-run:review:2"}) {
		t.Fatalf("execution identities = %#v", identities)
	}
}

func TestDurableRuntimeRecoversPersistedPendingStart(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	state := durableState(durableData{})
	store := NewMemoryDurableStore()
	envelope, err := EncodeCheckpoint(state, JSONStateCodec[durableData]{ID: "durable", Version: 1}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateRun(context.Background(), CreateStoredRun{Run: StoredWorkflow{Owner: durableOwner(), IdempotencyKey: "pending-recovery", Checkpoint: envelope, CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	node := &durableNode{id: "finish", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return now }, func() string { return "recovered-claim" })
	result, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "pending-recovery", State: state})
	if err != nil || result.Status != RunCompleted || node.calls.Load() != 1 {
		t.Fatalf("pending recovery = %#v, %v calls=%d", result, err, node.calls.Load())
	}
}

func TestMemoryStoreClaimsLeasesAndConcurrentResume(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	clock := now
	entered := make(chan struct{})
	release := make(chan struct{})
	node := &durableNode{id: "review", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		if input.Resume == nil {
			wait := durableWait(clock, input.State.Meta.RunID, "review", "wait-c")
			return NodeResult[durableData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
		}
		close(entered)
		<-release
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	store := NewMemoryDurableStore()
	var token atomic.Int64
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return clock }, func() string { return "claim-" + time.Unix(token.Add(1), 0).Format("05") })
	state := durableState(durableData{})
	suspended, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "concurrent", State: state})
	if err != nil {
		t.Fatal(err)
	}
	wait := suspended.State.Control.PendingWait
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	done := make(chan error, 1)
	go func() {
		_, resumeErr := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "1"}, state.Meta.RunID, command)
		done <- resumeErr
	}()
	<-entered
	_, conflict := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "2"}, state.Meta.RunID, command)
	if !IsCode(conflict, CodeClaimConflict) || node.calls.Load() != 2 {
		t.Fatalf("conflict = %v calls=%d", conflict, node.calls.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if events, _ := store.ListNodeEvents(context.Background(), durableOwner(), state.Meta.RunID); len(events) != 5 {
		t.Fatalf("events = %#v", events)
	}
}

func TestMemoryStoreLeaseRecoveryAndOldCommitRejection(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	store := NewMemoryDurableStore()
	state := durableState(durableData{})
	envelope, _ := EncodeCheckpoint(state, JSONStateCodec[durableData]{ID: "durable", Version: 1}, 1024)
	run := StoredWorkflow{Owner: durableOwner(), IdempotencyKey: "lease", Checkpoint: envelope, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateRun(context.Background(), CreateStoredRun{Run: run}); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimRun(context.Background(), ClaimRunRequest{Owner: durableOwner(), RunID: state.Meta.RunID, ExpectedStateVersion: 0, Claim: Claim{Token: "old", LeaseUntil: now.Add(time.Minute)}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Minute)
	second, err := store.ClaimRun(context.Background(), ClaimRunRequest{Owner: durableOwner(), RunID: state.Meta.RunID, ExpectedStateVersion: 0, Claim: Claim{Token: "new", LeaseUntil: later.Add(time.Minute)}, Now: later})
	if err != nil || first.Checkpoint.Control.StateVersion != second.Checkpoint.Control.StateVersion {
		t.Fatalf("reclaim = %#v, %v", second, err)
	}
	advanced := cloneEnvelope(envelope)
	advanced.Control.Status = RunRunning
	advanced.Control.StateVersion = 1
	if err := store.CommitExecution(context.Background(), CommitExecutionRequest{Owner: durableOwner(), RunID: state.Meta.RunID, Token: "old", ExpectedStateVersion: 0, Checkpoint: advanced, Now: later}); !IsCode(err, CodeLeaseLost) {
		t.Fatalf("old commit = %v", err)
	}
	if err := store.RenewClaim(context.Background(), RenewClaimRequest{Owner: durableOwner(), RunID: state.Meta.RunID, Token: "old", Now: later, Until: later.Add(time.Minute)}); !IsCode(err, CodeLeaseLost) {
		t.Fatalf("old renew = %v", err)
	}
}

func TestMemoryStoreRejectsInvalidEventBatches(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for name, events := range map[string][]NodeEvent{
		"missing":   nil,
		"wrong-run": {{Sequence: 1, RunID: "other", WorkflowID: "durable-flow", NodeID: "node", Type: EventNodeStarted}},
		"gap":       {{Sequence: 2, RunID: "durable-run", WorkflowID: "durable-flow", NodeID: "node", Type: EventNodeStarted}},
	} {
		store := NewMemoryDurableStore()
		state := durableState(durableData{})
		envelope, _ := EncodeCheckpoint(state, JSONStateCodec[durableData]{ID: "durable", Version: 1}, 1024)
		run := StoredWorkflow{Owner: durableOwner(), IdempotencyKey: name, Checkpoint: envelope, CreatedAt: now, UpdatedAt: now}
		_, _, _ = store.CreateRun(context.Background(), CreateStoredRun{Run: run})
		_, _ = store.ClaimRun(context.Background(), ClaimRunRequest{Owner: durableOwner(), RunID: state.Meta.RunID, ExpectedStateVersion: 0, Claim: Claim{Token: "claim", LeaseUntil: now.Add(time.Minute)}, Now: now})
		advanced := cloneEnvelope(envelope)
		advanced.Control.Status = RunRunning
		advanced.Control.StateVersion = 1
		advanced.Control.EventSequence = 1
		err := store.CommitExecution(context.Background(), CommitExecutionRequest{Owner: durableOwner(), RunID: state.Meta.RunID, Token: "claim", ExpectedStateVersion: 0, Checkpoint: advanced, Events: events, Now: now})
		if !IsCode(err, CodeStateConflict) {
			t.Fatalf("%s commit = %v", name, err)
		}
	}
}

func TestDurableRuntimeCanSuspendAgainOnResume(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	stage := 0
	node := &durableNode{id: "review", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		stage++
		waitID := WaitID("wait-1")
		if input.Resume != nil {
			waitID = "wait-2"
		}
		wait := durableWait(now, input.State.Meta.RunID, "review", waitID)
		return NodeResult[durableData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
	}}
	store := NewMemoryDurableStore()
	token := 0
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return now }, func() string { token++; return fmt.Sprintf("claim-%d", token) })
	state := durableState(durableData{})
	first, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "repeat-wait", State: state})
	if err != nil {
		t.Fatal(err)
	}
	wait := first.State.Control.PendingWait
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	second, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "1"}, state.Meta.RunID, command)
	if err != nil || second.Status != RunSuspended || second.State.Control.PendingWait.ID != "wait-2" || stage != 2 {
		t.Fatalf("second suspend = %#v, %v", second, err)
	}
	current, err := store.GetCurrentWait(context.Background(), durableOwner(), state.Meta.RunID)
	if err != nil || current.Point.ID != "wait-2" || current.Status != WaitPending {
		t.Fatalf("current wait = %#v, %v", current, err)
	}
}

type commitFailStore struct {
	DurableStore
	fail bool
}

func (s *commitFailStore) CommitExecution(ctx context.Context, request CommitExecutionRequest) error {
	if s.fail {
		return errors.New("commit failed")
	}
	return s.DurableStore.CommitExecution(ctx, request)
}

func TestDurableRuntimeRecoversClaimBeforeNodeAfterLease(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	clock := now
	node := &durableNode{id: "review", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		if input.Resume == nil {
			wait := durableWait(now, input.State.Meta.RunID, "review", "wait-recover")
			return NodeResult[durableData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
		}
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	store := NewMemoryDurableStore()
	token := 0
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return clock }, func() string { token++; return fmt.Sprintf("claim-%d", token) })
	state := durableState(durableData{})
	suspended, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "claim-before-node", State: state})
	if err != nil {
		t.Fatal(err)
	}
	wait := suspended.State.Control.PendingWait
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	if _, err := store.ClaimWait(context.Background(), ClaimWaitRequest{Owner: durableOwner(), RunID: state.Meta.RunID, ExpectedStateVersion: suspended.State.Control.StateVersion, Command: command, Actor: ActorRef{Type: "user", ID: "1"}, Claim: Claim{Token: "abandoned", LeaseUntil: now.Add(time.Minute)}, Now: now}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(2 * time.Minute)
	completed, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "2"}, state.Meta.RunID, command)
	if err != nil || completed.Status != RunCompleted || node.calls.Load() != 2 {
		t.Fatalf("recovered resume = %#v, %v calls=%d", completed, err, node.calls.Load())
	}
}

func TestDurableRuntimeRetriesSameExecutionIdentityAfterCommitCrash(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	clock := now
	node := &durableNode{id: "review", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		if input.Resume == nil {
			wait := durableWait(now, input.State.Meta.RunID, "review", "wait-crash")
			return NodeResult[durableData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
		}
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	base := NewMemoryDurableStore()
	store := &commitFailStore{DurableStore: base}
	token := 0
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return clock }, func() string { token++; return fmt.Sprintf("claim-%d", token) })
	state := durableState(durableData{})
	suspended, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "commit-crash", State: state})
	if err != nil {
		t.Fatal(err)
	}
	wait := suspended.State.Control.PendingWait
	command := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	store.fail = true
	if _, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "1"}, state.Meta.RunID, command); err == nil {
		t.Fatal("resume commit unexpectedly succeeded")
	}
	store.fail = false
	clock = now.Add(2 * time.Minute)
	completed, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "2"}, state.Meta.RunID, command)
	if err != nil || completed.Status != RunCompleted {
		t.Fatalf("retry resume = %#v, %v", completed, err)
	}
	node.mu.Lock()
	identities := append([]string(nil), node.executionIDs...)
	node.mu.Unlock()
	if !reflect.DeepEqual(identities, []string{"durable-run:review:1", "durable-run:review:2", "durable-run:review:2"}) {
		t.Fatalf("retry execution identities = %#v", identities)
	}
}

func TestDurableRuntimeRejectsInvalidResumeWithoutSideEffects(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	clock := now
	node := &durableNode{id: "review", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		wait := durableWait(now, input.State.Meta.RunID, "review", "wait-invalid")
		return NodeResult[durableData]{State: input.State, Directive: DirectiveSuspend, Wait: &wait}, nil
	}}
	store := NewMemoryDurableStore()
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return clock }, func() string { return "claim" })
	state := durableState(durableData{})
	suspended, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "invalid-resume", State: state})
	if err != nil {
		t.Fatal(err)
	}
	wait := suspended.State.Control.PendingWait
	valid := ResumeCommand{RunID: wait.RunID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: ActionApprove}
	stale := valid
	stale.Version++
	if _, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "1"}, state.Meta.RunID, stale); !IsCode(err, CodeInvalidResume) {
		t.Fatalf("stale resume = %v", err)
	}
	if _, err := runtime.Resume(context.Background(), WorkflowOwner{TenantID: 99, OwnerID: 99}, ActorRef{Type: "user", ID: "1"}, state.Meta.RunID, valid); !IsCode(err, CodeNotFound) {
		t.Fatalf("cross-owner resume = %v", err)
	}
	clock = wait.ExpiresAt
	if _, err := runtime.Resume(context.Background(), durableOwner(), ActorRef{Type: "user", ID: "1"}, state.Meta.RunID, valid); !IsCode(err, CodeWaitExpired) {
		t.Fatalf("expired resume = %v", err)
	}
	if node.calls.Load() != 1 {
		t.Fatalf("invalid resumes called node %d times", node.calls.Load())
	}
	if events, _ := store.ListNodeEvents(context.Background(), durableOwner(), state.Meta.RunID); len(events) != 2 {
		t.Fatalf("invalid resumes appended events: %#v", events)
	}
}

func TestDurableRuntimeDoesNotReportSuccessWhenCommitFails(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	node := &durableNode{id: "finish", execute: func(input NodeInput[durableData]) (NodeResult[durableData], error) {
		return NodeResult[durableData]{State: input.State, Directive: DirectiveContinue}, nil
	}}
	base := NewMemoryDurableStore()
	store := &commitFailStore{DurableStore: base, fail: true}
	runtime := durableRuntime(t, store, []*durableNode{node}, func() time.Time { return now }, func() string { return "claim" })
	state := durableState(durableData{})
	result, err := runtime.Start(context.Background(), StartWorkflowInput[durableData]{Owner: durableOwner(), IdempotencyKey: "commit-fail", State: state})
	if err == nil || result.Status != RunCompleted {
		t.Fatalf("Start = %#v, %v", result, err)
	}
	stored, loadErr := base.GetRun(context.Background(), durableOwner(), state.Meta.RunID)
	if loadErr != nil || stored.Checkpoint.Control.Status != RunPending || stored.Claim == nil {
		t.Fatalf("stored = %#v, %v", stored, loadErr)
	}
}

func durableRuntime(t *testing.T, store DurableStore, nodes []*durableNode, now func() time.Time, token func() string) *DurableRuntime[durableData] {
	t.Helper()
	values := make([]Node[durableData], len(nodes))
	for i := range nodes {
		values[i] = nodes[i]
	}
	runtime, err := NewDurableRuntime(store, values, DefinitionVersion("v1"), JSONStateCodec[durableData]{ID: "durable", Version: 1, ForbidSecrets: true}, DurableRuntimeOptions{LeaseDuration: time.Minute, MaxCheckpointBytes: 4096, Now: now, NewClaimToken: token})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func durableState(data durableData) WorkflowState[durableData] {
	return WorkflowState[durableData]{Meta: RunMetadata{WorkflowID: "durable-flow", DefinitionVersion: "v1", RunID: "durable-run", StartedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}, Control: ControlState{Status: RunPending}, Budget: BudgetState{MaxSteps: 10, MaxResumes: 3}, Data: data}
}

func durableOwner() WorkflowOwner { return WorkflowOwner{TenantID: 9, OwnerID: 7} }

func durableWait(now time.Time, runID WorkflowRunID, nodeID NodeID, waitID WaitID) WaitRequest {
	return WaitRequest{ID: waitID, RunID: runID, NodeID: nodeID, Kind: WaitApproval, Version: 1, ContentHash: "hash", AllowedActions: []HumanAction{ActionApprove, ActionReject}, PayloadRef: "payload:1", ExpiresAt: now.Add(time.Hour)}
}
