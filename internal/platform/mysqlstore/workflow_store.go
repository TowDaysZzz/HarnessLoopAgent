package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type WorkflowStore struct{ db *sql.DB }

func (s *Store) WorkflowStore() *WorkflowStore { return &WorkflowStore{db: s.db} }

func (s *WorkflowStore) CreateRun(ctx context.Context, input workflow.CreateStoredRun) (workflow.StoredWorkflow, bool, error) {
	if err := input.Run.Validate(); err != nil {
		return workflow.StoredWorkflow{}, false, err
	}
	checkpoint, err := json.Marshal(input.Run.Checkpoint)
	if err != nil {
		return workflow.StoredWorkflow{}, false, err
	}
	deadline := nullableTime(input.Run.Checkpoint.Budget.Deadline)
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_runs
		(id,tenant_id,owner_id,workflow_id,definition_version,source_type,source_id,idempotency_key,status,state_version,checkpoint_schema_id,checkpoint_schema_version,checkpoint,steps_executed,resume_count,event_sequence,max_steps,max_resumes,deadline,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.Run.Checkpoint.Meta.RunID, input.Run.Owner.TenantID, input.Run.Owner.OwnerID, input.Run.Checkpoint.Meta.WorkflowID,
		input.Run.Checkpoint.DefinitionVersion, nullString(input.Run.Checkpoint.Meta.Source.Type), nullString(input.Run.Checkpoint.Meta.Source.ID), input.Run.IdempotencyKey,
		input.Run.Checkpoint.Control.Status, input.Run.Checkpoint.Control.StateVersion, input.Run.Checkpoint.SchemaID, input.Run.Checkpoint.SchemaVersion, checkpoint,
		input.Run.Checkpoint.Control.StepsExecuted, input.Run.Checkpoint.Control.ResumeCount, input.Run.Checkpoint.Control.EventSequence,
		input.Run.Checkpoint.Budget.MaxSteps, input.Run.Checkpoint.Budget.MaxResumes, deadline, input.Run.CreatedAt, input.Run.UpdatedAt)
	if err == nil {
		return input.Run, true, nil
	}
	if !duplicateKey(err) {
		return workflow.StoredWorkflow{}, false, err
	}
	existing, loadErr := s.getRunByIdempotency(ctx, input.Run.Owner, input.Run.Checkpoint.Meta.WorkflowID, input.Run.IdempotencyKey)
	if loadErr != nil {
		return workflow.StoredWorkflow{}, false, loadErr
	}
	if !sameStoredStart(existing, input.Run) {
		return workflow.StoredWorkflow{}, false, &workflow.Error{Code: workflow.CodeIdempotencyConflict, Message: "workflow idempotency key was reused with different input"}
	}
	return existing, false, nil
}

func (s *WorkflowStore) GetRun(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (workflow.StoredWorkflow, error) {
	return scanStoredWorkflow(s.db.QueryRowContext(ctx, workflowRunSelect+` WHERE id=? AND tenant_id=? AND owner_id=?`, runID, owner.TenantID, owner.OwnerID))
}

func (s *WorkflowStore) getRunByIdempotency(ctx context.Context, owner workflow.WorkflowOwner, workflowID workflow.WorkflowID, key string) (workflow.StoredWorkflow, error) {
	return scanStoredWorkflow(s.db.QueryRowContext(ctx, workflowRunSelect+` WHERE tenant_id=? AND owner_id=? AND workflow_id=? AND idempotency_key=?`, owner.TenantID, owner.OwnerID, workflowID, key))
}

func (s *WorkflowStore) GetCurrentWait(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (workflow.StoredWait, error) {
	return scanStoredWait(s.db.QueryRowContext(ctx, workflowWaitSelect+` JOIN workflow_runs r ON r.id=w.run_id WHERE w.run_id=? AND r.tenant_id=? AND r.owner_id=? AND w.status IN ('pending','processing') ORDER BY w.created_at DESC LIMIT 1`, runID, owner.TenantID, owner.OwnerID))
}

func (s *WorkflowStore) ClaimRun(ctx context.Context, request workflow.ClaimRunRequest) (workflow.StoredWorkflow, error) {
	if err := request.Claim.Validate(request.Now); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET claim_token=?,lease_until=?,updated_at=? WHERE id=? AND tenant_id=? AND owner_id=? AND status='pending' AND state_version=? AND (claim_token IS NULL OR lease_until<=?)`, request.Claim.Token, request.Claim.LeaseUntil, request.Now, request.RunID, request.Owner.TenantID, request.Owner.OwnerID, request.ExpectedStateVersion, request.Now)
	if err != nil {
		return workflow.StoredWorkflow{}, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return workflow.StoredWorkflow{}, s.classifyRunClaim(ctx, request.Owner, request.RunID)
	}
	return s.GetRun(ctx, request.Owner, request.RunID)
}

func (s *WorkflowStore) ClaimWait(ctx context.Context, request workflow.ClaimWaitRequest) (workflow.StoredWorkflow, error) {
	if err := request.Claim.Validate(request.Now); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	if err := request.Actor.Validate(); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.StoredWorkflow{}, err
	}
	defer tx.Rollback()
	run, err := scanStoredWorkflow(tx.QueryRowContext(ctx, workflowRunSelect+` WHERE id=? AND tenant_id=? AND owner_id=? FOR UPDATE`, request.RunID, request.Owner.TenantID, request.Owner.OwnerID))
	if err != nil {
		return workflow.StoredWorkflow{}, err
	}
	if run.Checkpoint.Control.Status != workflow.RunSuspended || run.Checkpoint.Control.StateVersion != request.ExpectedStateVersion {
		return workflow.StoredWorkflow{}, &workflow.Error{Code: workflow.CodeStateConflict, Message: "workflow state changed before resume claim"}
	}
	wait, err := scanStoredWait(tx.QueryRowContext(ctx, workflowWaitSelect+` WHERE w.run_id=? AND w.status IN ('pending','processing') ORDER BY w.created_at DESC LIMIT 1 FOR UPDATE`, request.RunID))
	if err != nil {
		return workflow.StoredWorkflow{}, err
	}
	if wait.Status == workflow.WaitProcessing && wait.Claim != nil && wait.Claim.LeaseUntil.After(request.Now) {
		return workflow.StoredWorkflow{}, &workflow.Error{Code: workflow.CodeClaimConflict, Message: "workflow wait is already claimed"}
	}
	if err := wait.Point.ValidateResume(request.Command, request.Now); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_waits SET status='processing',record_version=record_version+1,claim_token=?,lease_until=?,updated_at=? WHERE wait_id=?`, request.Claim.Token, request.Claim.LeaseUntil, request.Now, wait.Point.ID); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET claim_token=?,lease_until=?,updated_at=? WHERE id=?`, request.Claim.Token, request.Claim.LeaseUntil, request.Now, request.RunID); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	return s.GetRun(ctx, request.Owner, request.RunID)
}

func (s *WorkflowStore) RenewClaim(ctx context.Context, request workflow.RenewClaimRequest) error {
	if !request.Until.After(request.Now) || request.Until.Sub(request.Now) > workflow.MaxLeaseDuration {
		return &workflow.Error{Code: workflow.CodeInvalidContract, Message: "invalid renewed lease"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET lease_until=?,updated_at=? WHERE id=? AND tenant_id=? AND owner_id=? AND claim_token=? AND lease_until>?`, request.Until, request.Now, request.RunID, request.Owner.TenantID, request.Owner.OwnerID, request.Token, request.Now)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return &workflow.Error{Code: workflow.CodeLeaseLost, Message: "workflow claim lease was lost"}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_waits SET lease_until=?,updated_at=? WHERE run_id=? AND status='processing' AND claim_token=?`, request.Until, request.Now, request.RunID, request.Token); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *WorkflowStore) CommitExecution(ctx context.Context, request workflow.CommitExecutionRequest) error {
	if err := request.Checkpoint.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := scanStoredWorkflow(tx.QueryRowContext(ctx, workflowRunSelect+` WHERE id=? AND tenant_id=? AND owner_id=? FOR UPDATE`, request.RunID, request.Owner.TenantID, request.Owner.OwnerID))
	if err != nil {
		return err
	}
	if run.Claim == nil || run.Claim.Token != request.Token || !run.Claim.LeaseUntil.After(request.Now) {
		return &workflow.Error{Code: workflow.CodeLeaseLost, Message: "workflow claim lease was lost"}
	}
	if run.Checkpoint.Control.StateVersion != request.ExpectedStateVersion || request.Checkpoint.Control.StateVersion <= request.ExpectedStateVersion {
		return &workflow.Error{Code: workflow.CodeStateConflict, Message: "workflow state changed before commit"}
	}
	if request.Checkpoint.Meta.RunID != request.RunID {
		return &workflow.Error{Code: workflow.CodeStateConflict, Message: "checkpoint run does not match claimed run"}
	}
	if err := validateWorkflowEvents(run.Checkpoint.Control.EventSequence, request.Checkpoint.Control.EventSequence, request.RunID, request.Events); err != nil {
		return err
	}
	if request.ResolvedWaitID != "" {
		if request.Actor == nil || request.Actor.Validate() != nil {
			return &workflow.Error{Code: workflow.CodeInvalidContract, Message: "resolved wait requires actor"}
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_waits SET status='resolved',record_version=record_version+1,claim_token=NULL,lease_until=NULL,resolved_action=?,resolved_actor_type=?,resolved_actor_id=?,resolved_at=?,updated_at=? WHERE wait_id=? AND run_id=? AND status='processing' AND claim_token=?`, request.ResolvedAction, request.Actor.Type, request.Actor.ID, request.Now, request.Now, request.ResolvedWaitID, request.RunID, request.Token)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return &workflow.Error{Code: workflow.CodeStateConflict, Message: "resolved wait does not match claim"}
		}
	}
	if request.Checkpoint.Control.Status == workflow.RunSuspended {
		point := request.Checkpoint.Control.PendingWait
		if point == nil || point.ID == request.ResolvedWaitID {
			return &workflow.Error{Code: workflow.CodeStateConflict, Message: "suspended checkpoint requires unique wait"}
		}
		actions, _ := json.Marshal(point.AllowedActions)
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_waits (wait_id,run_id,node_id,kind,wait_version,record_version,content_hash,allowed_actions,payload_ref,status,expires_at,created_at,updated_at) VALUES (?,?,?,?,?,1,?,?,?,'pending',?,?,?)`, point.ID, point.RunID, point.NodeID, point.Kind, point.Version, point.ContentHash, actions, nullString(point.PayloadRef), point.ExpiresAt, request.Now, request.Now); err != nil {
			return err
		}
	}
	for _, event := range request.Events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_node_events (run_id,sequence,workflow_id,node_id,event_type,run_status,attempt,resume_count,wait_id,error_code,duration_ns,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, event.RunID, event.Sequence, event.WorkflowID, event.NodeID, event.Type, event.Status, event.Attempt, event.ResumeCount, nullString(string(event.WaitID)), nullString(string(event.ErrorCode)), event.Duration.Nanoseconds(), event.OccurredAt); err != nil {
			return err
		}
	}
	checkpoint, err := json.Marshal(request.Checkpoint)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET definition_version=?,status=?,state_version=?,checkpoint_schema_id=?,checkpoint_schema_version=?,checkpoint=?,steps_executed=?,resume_count=?,event_sequence=?,max_steps=?,max_resumes=?,deadline=?,claim_token=NULL,lease_until=NULL,updated_at=? WHERE id=?`, request.Checkpoint.DefinitionVersion, request.Checkpoint.Control.Status, request.Checkpoint.Control.StateVersion, request.Checkpoint.SchemaID, request.Checkpoint.SchemaVersion, checkpoint, request.Checkpoint.Control.StepsExecuted, request.Checkpoint.Control.ResumeCount, request.Checkpoint.Control.EventSequence, request.Checkpoint.Budget.MaxSteps, request.Checkpoint.Budget.MaxResumes, nullableTime(request.Checkpoint.Budget.Deadline), request.Now, request.RunID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *WorkflowStore) ListNodeEvents(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) ([]workflow.NodeEvent, error) {
	if _, err := s.GetRun(ctx, owner, runID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,workflow_id,run_id,node_id,event_type,run_status,attempt,resume_count,COALESCE(wait_id,''),COALESCE(error_code,''),duration_ns,occurred_at FROM workflow_node_events WHERE run_id=? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []workflow.NodeEvent
	for rows.Next() {
		var event workflow.NodeEvent
		var duration int64
		if err := rows.Scan(&event.Sequence, &event.WorkflowID, &event.RunID, &event.NodeID, &event.Type, &event.Status, &event.Attempt, &event.ResumeCount, &event.WaitID, &event.ErrorCode, &duration, &event.OccurredAt); err != nil {
			return nil, err
		}
		event.Duration = time.Duration(duration)
		events = append(events, event)
	}
	return events, rows.Err()
}

const workflowRunSelect = `SELECT tenant_id,owner_id,idempotency_key,checkpoint,claim_token,lease_until,created_at,updated_at FROM workflow_runs`

func scanStoredWorkflow(row rowScanner) (workflow.StoredWorkflow, error) {
	var value workflow.StoredWorkflow
	var raw []byte
	var token sql.NullString
	var lease sql.NullTime
	if err := row.Scan(&value.Owner.TenantID, &value.Owner.OwnerID, &value.IdempotencyKey, &raw, &token, &lease, &value.CreatedAt, &value.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.StoredWorkflow{}, &workflow.Error{Code: workflow.CodeNotFound, Message: "workflow run not found"}
		}
		return workflow.StoredWorkflow{}, err
	}
	if err := json.Unmarshal(raw, &value.Checkpoint); err != nil {
		return workflow.StoredWorkflow{}, &workflow.Error{Code: workflow.CodeCodecIncompatible, Message: "decode workflow checkpoint", Err: err}
	}
	if token.Valid && lease.Valid {
		value.Claim = &workflow.Claim{Token: token.String, LeaseUntil: lease.Time}
	}
	return value, nil
}

const workflowWaitSelect = `SELECT w.wait_id,w.run_id,w.node_id,w.kind,w.wait_version,w.record_version,w.content_hash,w.allowed_actions,COALESCE(w.payload_ref,''),w.status,w.expires_at,w.claim_token,w.lease_until,COALESCE(w.resolved_action,''),w.resolved_actor_type,w.resolved_actor_id,w.resolved_at FROM workflow_waits w`

func scanStoredWait(row rowScanner) (workflow.StoredWait, error) {
	var value workflow.StoredWait
	var actions []byte
	var token, action, actorType, actorID sql.NullString
	var lease, resolved sql.NullTime
	if err := row.Scan(&value.Point.ID, &value.Point.RunID, &value.Point.NodeID, &value.Point.Kind, &value.Point.Version, &value.RecordVersion, &value.Point.ContentHash, &actions, &value.Point.PayloadRef, &value.Status, &value.Point.ExpiresAt, &token, &lease, &action, &actorType, &actorID, &resolved); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.StoredWait{}, &workflow.Error{Code: workflow.CodeNotFound, Message: "workflow wait not found"}
		}
		return workflow.StoredWait{}, err
	}
	if err := json.Unmarshal(actions, &value.Point.AllowedActions); err != nil {
		return workflow.StoredWait{}, err
	}
	if token.Valid && lease.Valid {
		value.Claim = &workflow.Claim{Token: token.String, LeaseUntil: lease.Time}
	}
	if action.Valid {
		value.ResolvedAction = workflow.HumanAction(action.String)
	}
	if actorType.Valid && actorID.Valid {
		value.ResolvedBy = &workflow.ActorRef{Type: actorType.String, ID: actorID.String}
	}
	if resolved.Valid {
		value.ResolvedAt = resolved.Time
	}
	return value, nil
}

func (s *WorkflowStore) classifyRunClaim(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) error {
	run, err := s.GetRun(ctx, owner, runID)
	if err != nil {
		return err
	}
	if run.Claim != nil {
		return &workflow.Error{Code: workflow.CodeClaimConflict, Message: "workflow run is already claimed"}
	}
	return &workflow.Error{Code: workflow.CodeStateConflict, Message: "workflow state does not allow claim"}
}

func validateWorkflowEvents(previous, final int64, runID workflow.WorkflowRunID, events []workflow.NodeEvent) error {
	if len(events) == 0 && final == previous {
		return nil
	}
	if int64(len(events)) != final-previous {
		return &workflow.Error{Code: workflow.CodeStateConflict, Message: "event batch does not cover checkpoint sequence"}
	}
	for index, event := range events {
		if event.RunID != runID || event.Sequence != previous+int64(index)+1 || !event.Type.Valid() || event.WorkflowID == "" || event.NodeID == "" {
			return &workflow.Error{Code: workflow.CodeStateConflict, Message: "event batch is not contiguous"}
		}
	}
	return nil
}

func sameStoredStart(left, right workflow.StoredWorkflow) bool {
	return left.Checkpoint.Meta.WorkflowID == right.Checkpoint.Meta.WorkflowID && left.Checkpoint.DefinitionVersion == right.Checkpoint.DefinitionVersion && left.Checkpoint.SchemaID == right.Checkpoint.SchemaID && left.Checkpoint.SchemaVersion == right.Checkpoint.SchemaVersion && jsonSemanticallyEqual(left.Checkpoint.Data, right.Checkpoint.Data) && reflect.DeepEqual(left.Checkpoint.Budget, right.Checkpoint.Budget) && reflect.DeepEqual(left.Checkpoint.Meta.Source, right.Checkpoint.Meta.Source)
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func duplicateKey(err error) bool {
	var value *mysqlDriver.MySQLError
	return errors.As(err, &value) && value.Number == 1062
}
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

var _ workflow.DurableStore = (*WorkflowStore)(nil)
