package mysqlstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WorkflowStore struct{ db *gorm.DB }

func (s *Store) WorkflowStore() *WorkflowStore { return &WorkflowStore{db: s.db} }

func (s *WorkflowStore) CreateRun(ctx context.Context, input workflow.CreateStoredRun) (workflow.StoredWorkflow, bool, error) {
	if err := input.Run.Validate(); err != nil {
		return workflow.StoredWorkflow{}, false, err
	}
	row, err := workflowStoredToRow(input.Run)
	if err != nil {
		return workflow.StoredWorkflow{}, false, err
	}
	err = s.db.WithContext(ctx).Create(&row).Error
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
	var row workflowRunRow
	err := s.db.WithContext(ctx).Where("id=? AND tenant_id=? AND owner_id=?", runID, owner.TenantID, owner.OwnerID).First(&row).Error
	return storedWorkflowResult(row, err)
}
func (s *WorkflowStore) getRunByIdempotency(ctx context.Context, owner workflow.WorkflowOwner, workflowID workflow.WorkflowID, key string) (workflow.StoredWorkflow, error) {
	var row workflowRunRow
	err := s.db.WithContext(ctx).Where("tenant_id=? AND owner_id=? AND workflow_id=? AND idempotency_key=?", owner.TenantID, owner.OwnerID, workflowID, key).First(&row).Error
	return storedWorkflowResult(row, err)
}

func (s *WorkflowStore) GetCurrentWait(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (workflow.StoredWait, error) {
	var row workflowWaitRow
	result := s.db.WithContext(ctx).Table("workflow_waits w").Select("w.*").Joins("JOIN workflow_runs r ON r.id=w.run_id").Where("w.run_id=? AND r.tenant_id=? AND r.owner_id=? AND w.status IN ?", runID, owner.TenantID, owner.OwnerID, []string{"pending", "processing"}).Order("w.created_at DESC").Limit(1).Scan(&row)
	if result.Error != nil {
		return workflow.StoredWait{}, result.Error
	}
	if result.RowsAffected == 0 {
		return workflow.StoredWait{}, workflowNotFound("workflow wait not found")
	}
	return workflowWaitFromRow(row)
}

func (s *WorkflowStore) ClaimRun(ctx context.Context, request workflow.ClaimRunRequest) (workflow.StoredWorkflow, error) {
	if err := request.Claim.Validate(request.Now); err != nil {
		return workflow.StoredWorkflow{}, err
	}
	result := s.db.WithContext(ctx).Model(&workflowRunRow{}).Where("id=? AND tenant_id=? AND owner_id=? AND status='pending' AND state_version=? AND (claim_token IS NULL OR lease_until<=?)", request.RunID, request.Owner.TenantID, request.Owner.OwnerID, request.ExpectedStateVersion, request.Now).Updates(map[string]any{"claim_token": request.Claim.Token, "lease_until": request.Claim.LeaseUntil, "updated_at": request.Now})
	if result.Error != nil {
		return workflow.StoredWorkflow{}, result.Error
	}
	if result.RowsAffected != 1 {
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runRow workflowRunRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND owner_id=?", request.RunID, request.Owner.TenantID, request.Owner.OwnerID).First(&runRow).Error
		run, err := storedWorkflowResult(runRow, err)
		if err != nil {
			return err
		}
		if run.Checkpoint.Control.Status != workflow.RunSuspended || run.Checkpoint.Control.StateVersion != request.ExpectedStateVersion {
			return &workflow.Error{Code: workflow.CodeStateConflict, Message: "workflow state changed before resume claim"}
		}
		var waitRow workflowWaitRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id=? AND status IN ?", request.RunID, []string{"pending", "processing"}).Order("created_at DESC").First(&waitRow).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return workflowNotFound("workflow wait not found")
		}
		if err != nil {
			return err
		}
		wait, err := workflowWaitFromRow(waitRow)
		if err != nil {
			return err
		}
		if wait.Status == workflow.WaitProcessing && wait.Claim != nil && wait.Claim.LeaseUntil.After(request.Now) {
			return &workflow.Error{Code: workflow.CodeClaimConflict, Message: "workflow wait is already claimed"}
		}
		if err := wait.Point.ValidateResume(request.Command, request.Now); err != nil {
			return err
		}
		if err := tx.Model(&workflowWaitRow{}).Where("wait_id=?", wait.Point.ID).Updates(map[string]any{"status": "processing", "record_version": gorm.Expr("record_version+1"), "claim_token": request.Claim.Token, "lease_until": request.Claim.LeaseUntil, "updated_at": request.Now}).Error; err != nil {
			return err
		}
		return tx.Model(&workflowRunRow{}).Where("id=?", request.RunID).Updates(map[string]any{"claim_token": request.Claim.Token, "lease_until": request.Claim.LeaseUntil, "updated_at": request.Now}).Error
	})
	if err != nil {
		return workflow.StoredWorkflow{}, err
	}
	return s.GetRun(ctx, request.Owner, request.RunID)
}

func (s *WorkflowStore) RenewClaim(ctx context.Context, request workflow.RenewClaimRequest) error {
	if !request.Until.After(request.Now) || request.Until.Sub(request.Now) > workflow.MaxLeaseDuration {
		return &workflow.Error{Code: workflow.CodeInvalidContract, Message: "invalid renewed lease"}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&workflowRunRow{}).Where("id=? AND tenant_id=? AND owner_id=? AND claim_token=? AND lease_until>?", request.RunID, request.Owner.TenantID, request.Owner.OwnerID, request.Token, request.Now).Updates(map[string]any{"lease_until": request.Until, "updated_at": request.Now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return &workflow.Error{Code: workflow.CodeLeaseLost, Message: "workflow claim lease was lost"}
		}
		return tx.Model(&workflowWaitRow{}).Where("run_id=? AND status='processing' AND claim_token=?", request.RunID, request.Token).Updates(map[string]any{"lease_until": request.Until, "updated_at": request.Now}).Error
	})
}

func (s *WorkflowStore) CommitExecution(ctx context.Context, request workflow.CommitExecutionRequest) error {
	if err := request.Checkpoint.Validate(); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runRow workflowRunRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND owner_id=?", request.RunID, request.Owner.TenantID, request.Owner.OwnerID).First(&runRow).Error
		run, err := storedWorkflowResult(runRow, err)
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
			result := tx.Model(&workflowWaitRow{}).Where("wait_id=? AND run_id=? AND status='processing' AND claim_token=?", request.ResolvedWaitID, request.RunID, request.Token).Updates(map[string]any{"status": "resolved", "record_version": gorm.Expr("record_version+1"), "claim_token": nil, "lease_until": nil, "resolved_action": request.ResolvedAction, "resolved_actor_type": request.Actor.Type, "resolved_actor_id": request.Actor.ID, "resolved_at": request.Now, "updated_at": request.Now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return &workflow.Error{Code: workflow.CodeStateConflict, Message: "resolved wait does not match claim"}
			}
		}
		if request.Checkpoint.Control.Status == workflow.RunSuspended {
			point := request.Checkpoint.Control.PendingWait
			if point == nil || point.ID == request.ResolvedWaitID {
				return &workflow.Error{Code: workflow.CodeStateConflict, Message: "suspended checkpoint requires unique wait"}
			}
			actions, _ := json.Marshal(point.AllowedActions)
			if err := tx.Create(&workflowWaitRow{WaitID: string(point.ID), RunID: string(point.RunID), NodeID: string(point.NodeID), Kind: string(point.Kind), WaitVersion: point.Version, RecordVersion: 1, ContentHash: point.ContentHash, AllowedActions: actions, PayloadRef: stringPtr(point.PayloadRef), Status: "pending", ExpiresAt: point.ExpiresAt, CreatedAt: request.Now, UpdatedAt: request.Now}).Error; err != nil {
				return err
			}
		}
		for _, event := range request.Events {
			if err := tx.Create(&workflowNodeEventRow{RunID: string(event.RunID), Sequence: event.Sequence, WorkflowID: string(event.WorkflowID), NodeID: string(event.NodeID), EventType: string(event.Type), RunStatus: string(event.Status), Attempt: event.Attempt, ResumeCount: event.ResumeCount, WaitID: stringPtr(string(event.WaitID)), ErrorCode: stringPtr(string(event.ErrorCode)), DurationNS: event.Duration.Nanoseconds(), OccurredAt: event.OccurredAt}).Error; err != nil {
				return err
			}
		}
		checkpoint, err := json.Marshal(request.Checkpoint)
		if err != nil {
			return err
		}
		return tx.Model(&workflowRunRow{}).Where("id=?", request.RunID).Updates(map[string]any{"definition_version": request.Checkpoint.DefinitionVersion, "status": request.Checkpoint.Control.Status, "state_version": request.Checkpoint.Control.StateVersion, "checkpoint_schema_id": request.Checkpoint.SchemaID, "checkpoint_schema_version": request.Checkpoint.SchemaVersion, "checkpoint": checkpoint, "steps_executed": request.Checkpoint.Control.StepsExecuted, "resume_count": request.Checkpoint.Control.ResumeCount, "event_sequence": request.Checkpoint.Control.EventSequence, "max_steps": request.Checkpoint.Budget.MaxSteps, "max_resumes": request.Checkpoint.Budget.MaxResumes, "deadline": timePtr(request.Checkpoint.Budget.Deadline), "claim_token": nil, "lease_until": nil, "updated_at": request.Now}).Error
	})
}

func (s *WorkflowStore) ListNodeEvents(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) ([]workflow.NodeEvent, error) {
	if _, err := s.GetRun(ctx, owner, runID); err != nil {
		return nil, err
	}
	var rows []workflowNodeEventRow
	if err := s.db.WithContext(ctx).Where("run_id=?", runID).Order("sequence").Find(&rows).Error; err != nil {
		return nil, err
	}
	events := make([]workflow.NodeEvent, 0, len(rows))
	for _, r := range rows {
		events = append(events, workflow.NodeEvent{Sequence: r.Sequence, WorkflowID: workflow.WorkflowID(r.WorkflowID), RunID: workflow.WorkflowRunID(r.RunID), NodeID: workflow.NodeID(r.NodeID), Type: workflow.NodeEventType(r.EventType), Status: workflow.RunStatus(r.RunStatus), Attempt: r.Attempt, ResumeCount: r.ResumeCount, WaitID: workflow.WaitID(stringValue(r.WaitID)), ErrorCode: workflow.ErrorCode(stringValue(r.ErrorCode)), Duration: time.Duration(r.DurationNS), OccurredAt: r.OccurredAt})
	}
	return events, nil
}

func workflowStoredToRow(v workflow.StoredWorkflow) (workflowRunRow, error) {
	raw, err := json.Marshal(v.Checkpoint)
	if err != nil {
		return workflowRunRow{}, err
	}
	return workflowRunRow{ID: string(v.Checkpoint.Meta.RunID), TenantID: v.Owner.TenantID, OwnerID: v.Owner.OwnerID, WorkflowID: string(v.Checkpoint.Meta.WorkflowID), DefinitionVersion: string(v.Checkpoint.DefinitionVersion), SourceType: stringPtr(v.Checkpoint.Meta.Source.Type), SourceID: stringPtr(v.Checkpoint.Meta.Source.ID), IdempotencyKey: v.IdempotencyKey, Status: string(v.Checkpoint.Control.Status), StateVersion: v.Checkpoint.Control.StateVersion, CheckpointSchemaID: v.Checkpoint.SchemaID, CheckpointSchemaVersion: v.Checkpoint.SchemaVersion, Checkpoint: raw, StepsExecuted: v.Checkpoint.Control.StepsExecuted, ResumeCount: v.Checkpoint.Control.ResumeCount, EventSequence: v.Checkpoint.Control.EventSequence, MaxSteps: v.Checkpoint.Budget.MaxSteps, MaxResumes: v.Checkpoint.Budget.MaxResumes, Deadline: timePtr(v.Checkpoint.Budget.Deadline), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}, nil
}
func storedWorkflowResult(row workflowRunRow, err error) (workflow.StoredWorkflow, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workflow.StoredWorkflow{}, workflowNotFound("workflow run not found")
	}
	if err != nil {
		return workflow.StoredWorkflow{}, err
	}
	var checkpoint workflow.CheckpointEnvelope
	if err := json.Unmarshal(row.Checkpoint, &checkpoint); err != nil {
		return workflow.StoredWorkflow{}, &workflow.Error{Code: workflow.CodeCodecIncompatible, Message: "decode workflow checkpoint", Err: err}
	}
	value := workflow.StoredWorkflow{Owner: workflow.WorkflowOwner{TenantID: row.TenantID, OwnerID: row.OwnerID}, IdempotencyKey: row.IdempotencyKey, Checkpoint: checkpoint, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.ClaimToken != nil && row.LeaseUntil != nil {
		value.Claim = &workflow.Claim{Token: *row.ClaimToken, LeaseUntil: *row.LeaseUntil}
	}
	return value, nil
}
func workflowWaitFromRow(row workflowWaitRow) (workflow.StoredWait, error) {
	var actions []workflow.HumanAction
	if err := json.Unmarshal(row.AllowedActions, &actions); err != nil {
		return workflow.StoredWait{}, err
	}
	value := workflow.StoredWait{Point: workflow.WaitPoint{ID: workflow.WaitID(row.WaitID), RunID: workflow.WorkflowRunID(row.RunID), NodeID: workflow.NodeID(row.NodeID), Kind: workflow.WaitKind(row.Kind), Version: row.WaitVersion, ContentHash: row.ContentHash, AllowedActions: actions, PayloadRef: stringValue(row.PayloadRef), ExpiresAt: row.ExpiresAt}, Status: workflow.WaitStatus(row.Status), RecordVersion: row.RecordVersion}
	if row.ClaimToken != nil && row.LeaseUntil != nil {
		value.Claim = &workflow.Claim{Token: *row.ClaimToken, LeaseUntil: *row.LeaseUntil}
	}
	if row.ResolvedAction != nil {
		value.ResolvedAction = workflow.HumanAction(*row.ResolvedAction)
	}
	if row.ResolvedActorType != nil && row.ResolvedActorID != nil {
		value.ResolvedBy = &workflow.ActorRef{Type: *row.ResolvedActorType, ID: *row.ResolvedActorID}
	}
	if row.ResolvedAt != nil {
		value.ResolvedAt = *row.ResolvedAt
	}
	return value, nil
}
func workflowNotFound(message string) error {
	return &workflow.Error{Code: workflow.CodeNotFound, Message: message}
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
	for i, event := range events {
		if event.RunID != runID || event.Sequence != previous+int64(i)+1 || !event.Type.Valid() || event.WorkflowID == "" || event.NodeID == "" {
			return &workflow.Error{Code: workflow.CodeStateConflict, Message: "event batch is not contiguous"}
		}
	}
	return nil
}
func sameStoredStart(left, right workflow.StoredWorkflow) bool {
	return left.Checkpoint.Meta.WorkflowID == right.Checkpoint.Meta.WorkflowID && left.Checkpoint.DefinitionVersion == right.Checkpoint.DefinitionVersion && left.Checkpoint.SchemaID == right.Checkpoint.SchemaID && left.Checkpoint.SchemaVersion == right.Checkpoint.SchemaVersion && jsonSemanticallyEqual(left.Checkpoint.Data, right.Checkpoint.Data) && reflect.DeepEqual(left.Checkpoint.Budget, right.Checkpoint.Budget) && reflect.DeepEqual(left.Checkpoint.Meta.Source, right.Checkpoint.Meta.Source)
}
func jsonSemanticallyEqual(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}
func duplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var value *mysqlDriver.MySQLError
	return errors.As(err, &value) && value.Number == 1062
}
func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

var _ workflow.DurableStore = (*WorkflowStore)(nil)
