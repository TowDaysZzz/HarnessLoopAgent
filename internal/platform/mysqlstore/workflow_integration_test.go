package mysqlstore

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

func TestIntegrationDurableWorkflowLifecycle(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run against a disposable MySQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := Open(ctx, Options{DSN: dsn, MaxOpenConns: 8, MaxIdleConns: 4, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	var chatRunsBefore int
	_ = base.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM agent_runs`).Scan(&chatRunsBefore).Error
	if err := base.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := base.Migrate(ctx); err != nil {
		t.Fatalf("idempotent Migrate() = %v", err)
	}
	var chatRunsAfter int
	if err := base.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM agent_runs`).Scan(&chatRunsAfter).Error; err != nil || chatRunsAfter != chatRunsBefore {
		t.Fatalf("chat runs changed during workflow migration: %d -> %d, %v", chatRunsBefore, chatRunsAfter, err)
	}
	for _, table := range []string{"workflow_runs", "workflow_waits", "workflow_node_events"} {
		var count int
		if err := base.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, table).Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}

	store := base.WorkflowStore()
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := workflow.WorkflowRunID(uuid.NewString())
	owner := workflow.WorkflowOwner{TenantID: 9001, OwnerID: 7001}
	state := integrationWorkflowState(runID, now)
	checkpoint := integrationEnvelope(t, state)
	record := workflow.StoredWorkflow{Owner: owner, IdempotencyKey: "integration-" + uuid.NewString(), Checkpoint: checkpoint, CreatedAt: now, UpdatedAt: now}
	created, wasCreated, err := store.CreateRun(ctx, workflow.CreateStoredRun{Run: record})
	if err != nil || !wasCreated || created.Checkpoint.Meta.RunID != runID {
		t.Fatalf("CreateRun = %#v, %v, %v", created, wasCreated, err)
	}
	replay, wasCreated, err := store.CreateRun(ctx, workflow.CreateStoredRun{Run: record})
	if err != nil || wasCreated || replay.Checkpoint.Meta.RunID != runID {
		t.Fatalf("idempotent CreateRun = %#v, %v, %v", replay, wasCreated, err)
	}
	changed := record
	changed.Checkpoint.Data = json.RawMessage(`{"value":"different"}`)
	if _, _, err := store.CreateRun(ctx, workflow.CreateStoredRun{Run: changed}); !workflow.IsCode(err, workflow.CodeIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	if _, err := store.GetRun(ctx, workflow.WorkflowOwner{TenantID: owner.TenantID, OwnerID: owner.OwnerID + 1}, runID); !workflow.IsCode(err, workflow.CodeNotFound) {
		t.Fatalf("cross-owner GetRun = %v", err)
	}

	claim := workflow.Claim{Token: "start-" + uuid.NewString(), LeaseUntil: now.Add(time.Minute)}
	claimed, err := store.ClaimRun(ctx, workflow.ClaimRunRequest{Owner: owner, RunID: runID, ExpectedStateVersion: 0, Claim: claim, Now: now})
	if err != nil || claimed.Claim == nil {
		t.Fatalf("ClaimRun = %#v, %v", claimed, err)
	}
	wait := workflow.WaitPoint{ID: workflow.WaitID(uuid.NewString()), RunID: runID, NodeID: "review", Kind: workflow.WaitApproval, Version: 1, ContentHash: "hash", AllowedActions: []workflow.HumanAction{workflow.ActionApprove, workflow.ActionReject}, PayloadRef: "object:1", ExpiresAt: now.Add(time.Hour)}
	suspended := state
	suspended.Control = workflow.ControlState{Status: workflow.RunSuspended, CurrentNode: "review", PendingWait: &wait, StateVersion: 4, StepsExecuted: 1, EventSequence: 2, CurrentAttempt: 1}
	suspendedCheckpoint := integrationEnvelope(t, suspended)
	startEvents := []workflow.NodeEvent{
		{Sequence: 1, WorkflowID: state.Meta.WorkflowID, RunID: runID, NodeID: "review", Type: workflow.EventNodeStarted, Status: workflow.RunRunning, Attempt: 1, OccurredAt: now},
		{Sequence: 2, WorkflowID: state.Meta.WorkflowID, RunID: runID, NodeID: "review", Type: workflow.EventNodeSuspended, Status: workflow.RunSuspended, Attempt: 1, WaitID: wait.ID, OccurredAt: now},
	}
	if err := store.CommitExecution(ctx, workflow.CommitExecutionRequest{Owner: owner, RunID: runID, Token: claim.Token, ExpectedStateVersion: 0, Checkpoint: suspendedCheckpoint, Events: startEvents, Now: now}); err != nil {
		t.Fatalf("commit suspended = %v", err)
	}
	storedWait, err := store.GetCurrentWait(ctx, owner, runID)
	if err != nil || storedWait.Status != workflow.WaitPending || storedWait.Point.ID != wait.ID {
		t.Fatalf("GetCurrentWait = %#v, %v", storedWait, err)
	}
	command := workflow.ResumeCommand{RunID: runID, WaitID: wait.ID, Version: wait.Version, ContentHash: wait.ContentHash, Action: workflow.ActionApprove}
	resumeClaim := workflow.Claim{Token: "resume-" + uuid.NewString(), LeaseUntil: now.Add(2 * time.Minute)}
	if _, err := store.ClaimWait(ctx, workflow.ClaimWaitRequest{Owner: owner, RunID: runID, ExpectedStateVersion: 4, Command: command, Actor: workflow.ActorRef{Type: "user", ID: "7001"}, Claim: resumeClaim, Now: now}); err != nil {
		t.Fatalf("ClaimWait = %v", err)
	}
	if _, err := store.ClaimWait(ctx, workflow.ClaimWaitRequest{Owner: owner, RunID: runID, ExpectedStateVersion: 4, Command: command, Actor: workflow.ActorRef{Type: "user", ID: "7002"}, Claim: workflow.Claim{Token: "other", LeaseUntil: now.Add(time.Minute)}, Now: now}); !workflow.IsCode(err, workflow.CodeClaimConflict) {
		t.Fatalf("concurrent ClaimWait = %v", err)
	}
	completed := suspended
	completed.Control = workflow.ControlState{Status: workflow.RunCompleted, CurrentNode: "review", CompletedNodes: []workflow.NodeID{"review"}, StateVersion: 9, StepsExecuted: 2, ResumeCount: 1, EventSequence: 5, CurrentAttempt: 2}
	completedCheckpoint := integrationEnvelope(t, completed)
	resumeEvents := []workflow.NodeEvent{
		{Sequence: 3, WorkflowID: state.Meta.WorkflowID, RunID: runID, NodeID: "review", Type: workflow.EventNodeResumed, Status: workflow.RunSuspended, Attempt: 1, WaitID: wait.ID, OccurredAt: now},
		{Sequence: 4, WorkflowID: state.Meta.WorkflowID, RunID: runID, NodeID: "review", Type: workflow.EventNodeStarted, Status: workflow.RunRunning, Attempt: 2, ResumeCount: 1, OccurredAt: now},
		{Sequence: 5, WorkflowID: state.Meta.WorkflowID, RunID: runID, NodeID: "review", Type: workflow.EventNodeCompleted, Status: workflow.RunRunning, Attempt: 2, ResumeCount: 1, OccurredAt: now},
	}
	actor := workflow.ActorRef{Type: "user", ID: "7001"}
	if err := base.db.WithContext(ctx).Exec(`INSERT INTO workflow_node_events (run_id,sequence,workflow_id,node_id,event_type,run_status,attempt,resume_count,duration_ns,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, runID, 5, state.Meta.WorkflowID, "injected", workflow.EventNodeFailed, workflow.RunFailed, 1, 0, 0, now).Error; err != nil {
		t.Fatalf("inject event conflict = %v", err)
	}
	commitRequest := workflow.CommitExecutionRequest{Owner: owner, RunID: runID, Token: resumeClaim.Token, ExpectedStateVersion: 4, Checkpoint: completedCheckpoint, Events: resumeEvents, ResolvedWaitID: wait.ID, ResolvedAction: workflow.ActionApprove, Actor: &actor, Now: now}
	if err := store.CommitExecution(ctx, commitRequest); err == nil {
		t.Fatal("commit with duplicate event unexpectedly succeeded")
	}
	afterRollback, err := store.GetRun(ctx, owner, runID)
	if err != nil || afterRollback.Checkpoint.Control.Status != workflow.RunSuspended || afterRollback.Claim == nil {
		t.Fatalf("run after rollback = %#v, %v", afterRollback, err)
	}
	waitAfterRollback, err := store.GetCurrentWait(ctx, owner, runID)
	if err != nil || waitAfterRollback.Status != workflow.WaitProcessing {
		t.Fatalf("wait after rollback = %#v, %v", waitAfterRollback, err)
	}
	var eventCount int
	if err := base.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM workflow_node_events WHERE run_id=? AND sequence IN (3,4)`, runID).Scan(&eventCount).Error; err != nil || eventCount != 0 {
		t.Fatalf("partial events after rollback = %d, %v", eventCount, err)
	}
	if err := base.db.WithContext(ctx).Exec(`DELETE FROM workflow_node_events WHERE run_id=? AND sequence=5`, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CommitExecution(ctx, commitRequest); err != nil {
		t.Fatalf("commit completed = %v", err)
	}
	events, err := store.ListNodeEvents(ctx, owner, runID)
	if err != nil || len(events) != 5 || events[4].Sequence != 5 {
		t.Fatalf("ListNodeEvents = %#v, %v", events, err)
	}
	if _, err := store.ClaimRun(ctx, workflow.ClaimRunRequest{Owner: owner, RunID: runID, ExpectedStateVersion: 9, Claim: workflow.Claim{Token: "late", LeaseUntil: now.Add(time.Minute)}, Now: now}); !workflow.IsCode(err, workflow.CodeStateConflict) {
		t.Fatalf("terminal ClaimRun = %v", err)
	}
}

func TestGORMWorkflowExpiredLeaseTakeoverAndLeaseLost(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run workflow lease integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := Open(ctx, Options{DSN: dsn, MaxOpenConns: 8, MaxIdleConns: 4, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err := base.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := base.WorkflowStore()
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := workflow.WorkflowRunID(uuid.NewString())
	owner := workflow.WorkflowOwner{TenantID: 9401, OwnerID: uint64(now.UnixNano()%500000000) + 7000000000}
	state := integrationWorkflowState(runID, now)
	record := workflow.StoredWorkflow{Owner: owner, IdempotencyKey: "lease-" + uuid.NewString(), Checkpoint: integrationEnvelope(t, state), CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateRun(ctx, workflow.CreateStoredRun{Run: record}); err != nil {
		t.Fatal(err)
	}
	first := workflow.Claim{Token: "first-" + uuid.NewString(), LeaseUntil: now.Add(time.Second)}
	if _, err := store.ClaimRun(ctx, workflow.ClaimRunRequest{Owner: owner, RunID: runID, ExpectedStateVersion: 0, Claim: first, Now: now}); err != nil {
		t.Fatal(err)
	}
	takeoverNow := now.Add(2 * time.Second)
	second := workflow.Claim{Token: "second-" + uuid.NewString(), LeaseUntil: takeoverNow.Add(time.Minute)}
	claimed, err := store.ClaimRun(ctx, workflow.ClaimRunRequest{Owner: owner, RunID: runID, ExpectedStateVersion: 0, Claim: second, Now: takeoverNow})
	if err != nil || claimed.Claim == nil || claimed.Claim.Token != second.Token {
		t.Fatalf("takeover=%+v err=%v", claimed, err)
	}
	if err := store.RenewClaim(ctx, workflow.RenewClaimRequest{Owner: owner, RunID: runID, Token: first.Token, Now: takeoverNow, Until: takeoverNow.Add(time.Minute)}); !workflow.IsCode(err, workflow.CodeLeaseLost) {
		t.Fatalf("stale renew=%v", err)
	}
}

func integrationWorkflowState(runID workflow.WorkflowRunID, now time.Time) workflow.WorkflowState[map[string]string] {
	return workflow.WorkflowState[map[string]string]{Meta: workflow.RunMetadata{WorkflowID: "integration-flow", DefinitionVersion: "v1", RunID: runID, StartedAt: now}, Control: workflow.ControlState{Status: workflow.RunPending}, Budget: workflow.BudgetState{MaxSteps: 10, MaxResumes: 3, Deadline: now.Add(24 * time.Hour)}, Data: map[string]string{"document_id": "doc-1"}}
}

func integrationEnvelope(t *testing.T, state workflow.WorkflowState[map[string]string]) workflow.CheckpointEnvelope {
	t.Helper()
	value, err := workflow.EncodeCheckpoint(state, workflow.JSONStateCodec[map[string]string]{ID: "integration-state", Version: 1, ForbidSecrets: true}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
