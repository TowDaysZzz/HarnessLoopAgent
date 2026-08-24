package reminderworkflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type ServiceConfig struct {
	DefinitionVersion    workflow.DefinitionVersion
	MaxSteps, MaxResumes int
	RunTTL               time.Duration
	Now                  func() time.Time
}

type Service struct {
	runtime *workflow.DurableRuntime[CommandData]
	edits   *EditPayloadService
	config  ServiceConfig
}

func NewService(runtime *workflow.DurableRuntime[CommandData], edits *EditPayloadService, config ServiceConfig) (*Service, error) {
	if runtime == nil || config.DefinitionVersion == "" || config.MaxSteps < 6 || config.MaxResumes < 1 || config.RunTTL <= 0 {
		return nil, ErrInvalidCommandData
	}
	return &Service{runtime: runtime, edits: edits, config: config}, nil
}

type StartInput struct {
	Owner          workflow.WorkflowOwner
	Query          string
	IdempotencyKey string
	TrustedTarget  *reminder.ReminderRef
}

type ResumeInput struct {
	Owner       workflow.WorkflowOwner
	Actor       workflow.ActorRef
	RunID       workflow.WorkflowRunID
	WaitID      workflow.WaitID
	Version     uint64
	ContentHash string
	Action      workflow.HumanAction
	EditText    string
}

type ReviewDTO struct {
	WaitID         string                   `json:"wait_id"`
	Version        uint64                   `json:"version"`
	ContentHash    string                   `json:"content_hash"`
	ExpiresAt      time.Time                `json:"expires_at"`
	AllowedActions []workflow.HumanAction   `json:"allowed_actions"`
	Action         reminder.Action          `json:"action"`
	Content        string                   `json:"content,omitempty"`
	ScheduledAt    *time.Time               `json:"scheduled_at,omitempty"`
	Timezone       string                   `json:"timezone,omitempty"`
	Target         *reminder.ReminderRef    `json:"target,omitempty"`
	TargetChoices  []reminder.ReminderRef   `json:"target_choices,omitempty"`
	MemorySummary  []reminder.MemorySummary `json:"memory_summary,omitempty"`
	Clarification  *reminder.Clarification  `json:"clarification,omitempty"`
}

type CommandDTO struct {
	RunID     string             `json:"run_id"`
	Status    string             `json:"status"`
	Review    *ReviewDTO         `json:"review,omitempty"`
	Committed *reminder.Reminder `json:"committed,omitempty"`
}

func (s *Service) Start(ctx context.Context, input StartInput) (CommandDTO, error) {
	query := strings.TrimSpace(input.Query)
	if s == nil || input.Owner.Validate() != nil || query == "" || len(query) > 4096 || strings.TrimSpace(input.IdempotencyKey) == "" || input.TrustedTarget != nil && input.TrustedTarget.Validate() != nil {
		return CommandDTO{}, ErrInvalidCommandData
	}
	now := s.now()
	runID := workflow.WorkflowRunID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("reminder-command:%d:%d:%s", input.Owner.TenantID, input.Owner.OwnerID, input.IdempotencyKey))).String())
	if existing, err := s.runtime.Get(ctx, input.Owner, runID); err == nil {
		if existing.State.Data.Query != query || !sameTarget(existing.State.Data.TrustedTarget, input.TrustedTarget) {
			return CommandDTO{}, workflow.ErrIdempotencyConflict
		}
		if existing.Status != workflow.RunPending {
			return toDTO(existing), nil
		}
		result, startErr := s.runtime.Start(ctx, workflow.StartWorkflowInput[CommandData]{Owner: input.Owner, IdempotencyKey: input.IdempotencyKey, State: existing.State})
		if startErr != nil {
			return CommandDTO{}, startErr
		}
		return toDTO(result), nil
	} else if !workflow.IsCode(err, workflow.CodeNotFound) {
		return CommandDTO{}, err
	}
	data := CommandData{Owner: reminder.Owner{TenantID: input.Owner.TenantID, UserID: input.Owner.OwnerID}, Query: query, ReceivedAt: now, TrustedTarget: cloneTarget(input.TrustedTarget)}
	state := workflow.WorkflowState[CommandData]{
		Meta:    workflow.RunMetadata{WorkflowID: "reminder-command", DefinitionVersion: s.config.DefinitionVersion, RunID: runID, StartedAt: now},
		Control: workflow.ControlState{Status: workflow.RunPending},
		Budget:  workflow.BudgetState{MaxSteps: s.config.MaxSteps, MaxResumes: s.config.MaxResumes, Deadline: now.Add(s.config.RunTTL)}, Data: data,
	}
	result, err := s.runtime.Start(ctx, workflow.StartWorkflowInput[CommandData]{Owner: input.Owner, IdempotencyKey: input.IdempotencyKey, State: state})
	if err != nil {
		return CommandDTO{}, err
	}
	return toDTO(result), nil
}

func (s *Service) Get(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (CommandDTO, error) {
	if s == nil || owner.Validate() != nil {
		return CommandDTO{}, ErrInvalidCommandData
	}
	result, err := s.runtime.Get(ctx, owner, runID)
	if err != nil {
		return CommandDTO{}, err
	}
	return toDTO(result), nil
}

func (s *Service) Resume(ctx context.Context, input ResumeInput) (CommandDTO, error) {
	if s == nil || input.Owner.Validate() != nil || input.Actor.Validate() != nil {
		return CommandDTO{}, ErrInvalidCommandData
	}
	payloadRef := ""
	if input.Action == workflow.ActionSubmitEdit {
		if s.edits == nil || strings.TrimSpace(input.EditText) == "" {
			return CommandDTO{}, ErrInvalidEditPayload
		}
		ref, err := s.edits.Create(ctx, reminder.Owner{TenantID: input.Owner.TenantID, UserID: input.Owner.OwnerID}, input.EditText)
		if err != nil {
			return CommandDTO{}, err
		}
		payloadRef = ref
	} else if strings.TrimSpace(input.EditText) != "" {
		return CommandDTO{}, ErrInvalidEditPayload
	}
	result, err := s.runtime.Resume(ctx, input.Owner, input.Actor, input.RunID, workflow.ResumeCommand{RunID: input.RunID, WaitID: input.WaitID, Version: input.Version, ContentHash: input.ContentHash, Action: input.Action, PayloadRef: payloadRef})
	if err != nil {
		return CommandDTO{}, err
	}
	return toDTO(result), nil
}

func (s *Service) now() time.Time {
	if s.config.Now != nil {
		return s.config.Now().UTC()
	}
	return time.Now().UTC()
}

func toDTO(result workflow.RunResult[CommandData]) CommandDTO {
	dto := CommandDTO{RunID: string(result.State.Meta.RunID), Status: string(result.Status), Committed: result.State.Data.Committed}
	data := result.State.Data
	if data.Review != nil && result.State.Control.PendingWait != nil && data.Plan != nil {
		wait := result.State.Control.PendingWait
		dto.Review = &ReviewDTO{WaitID: string(wait.ID), Version: wait.Version, ContentHash: wait.ContentHash, ExpiresAt: wait.ExpiresAt, AllowedActions: append([]workflow.HumanAction(nil), wait.AllowedActions...), Action: data.Plan.Action, Content: data.Plan.Content, ScheduledAt: data.ScheduledAt, Timezone: reminder.DefaultTimezone, Target: cloneTarget(data.Target), TargetChoices: append([]reminder.ReminderRef(nil), data.TargetChoices...), MemorySummary: append([]reminder.MemorySummary(nil), data.MemorySummary...), Clarification: data.Clarification}
	}
	return dto
}

func sameTarget(a, b *reminder.ReminderRef) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func cloneTarget(value *reminder.ReminderRef) *reminder.ReminderRef {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
