package memoryworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type CaptureServiceConfig struct {
	DefinitionVersion                   workflow.DefinitionVersion
	MaxSteps, MaxResumes, MaxDraftChars int
	RunTTL                              time.Duration
	Now                                 func() time.Time
	Telemetry                           CaptureTelemetry
}
type CaptureTelemetry interface{ ObserveCapture(string, string) }
type CaptureService struct {
	runtime *workflow.DurableRuntime[CaptureData]
	edits   *EditPayloadService
	config  CaptureServiceConfig
}

func NewCaptureService(runtime *workflow.DurableRuntime[CaptureData], edits *EditPayloadService, config CaptureServiceConfig) (*CaptureService, error) {
	if runtime == nil || config.DefinitionVersion == "" || config.MaxSteps < 5 || config.MaxResumes < 1 || config.MaxDraftChars < 64 || config.RunTTL <= 0 {
		return nil, ErrInvalidCaptureData
	}
	return &CaptureService{runtime: runtime, edits: edits, config: config}, nil
}

type StartCaptureInput struct {
	Owner                 workflow.WorkflowOwner
	Query, IdempotencyKey string
	Intent                memory.IntentAuthority
}
type ResumeCaptureInput struct {
	Owner       workflow.WorkflowOwner
	Actor       workflow.ActorRef
	RunID       workflow.WorkflowRunID
	WaitID      workflow.WaitID
	Version     uint64
	ContentHash string
	Action      workflow.HumanAction
	EditText    string
}
type CaptureDTO struct {
	RunID     string            `json:"run_id"`
	Status    string            `json:"status"`
	Draft     *DraftDTO         `json:"draft,omitempty"`
	Policy    *PolicyResult     `json:"policy,omitempty"`
	Review    *ReviewDTO        `json:"review,omitempty"`
	Committed *memory.MemoryRef `json:"committed,omitempty"`
}
type DraftDTO struct {
	Layer         memory.Layer     `json:"layer"`
	Kind          memory.Kind      `json:"kind"`
	Scope         memory.Scope     `json:"scope"`
	Namespace     string           `json:"namespace"`
	SlotKey       string           `json:"slot_key,omitempty"`
	Entity        memory.EntityRef `json:"entity,omitempty"`
	CanonicalText string           `json:"canonical_text"`
	ContentHash   string           `json:"content_hash"`
}
type ReviewDTO struct {
	WaitID              string                 `json:"wait_id"`
	Version             uint64                 `json:"version"`
	ContentHash         string                 `json:"content_hash"`
	ExpiresAt           time.Time              `json:"expires_at"`
	AllowedActions      []workflow.HumanAction `json:"allowed_actions"`
	Candidate           memory.MemoryRef       `json:"candidate"`
	CandidateRowVersion uint64                 `json:"candidate_row_version"`
}

func (s *CaptureService) Start(ctx context.Context, input StartCaptureInput) (CaptureDTO, error) {
	if s == nil || input.Owner.Validate() != nil || strings.TrimSpace(input.Query) == "" || len(input.Query) > 4096 || strings.TrimSpace(input.IdempotencyKey) == "" {
		return CaptureDTO{}, ErrInvalidCaptureData
	}
	now := time.Now().UTC()
	if s.config.Now != nil {
		now = s.config.Now().UTC()
	}
	intent := input.Intent
	if intent == "" {
		intent = memory.IntentUserStatement
	}
	query := strings.TrimSpace(input.Query)
	runID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("memory-capture:%d:%d:%s", input.Owner.TenantID, input.Owner.OwnerID, input.IdempotencyKey))).String()
	if existing, err := s.runtime.Get(ctx, input.Owner, workflow.WorkflowRunID(runID)); err == nil {
		if existing.State.Data.Query != query || existing.State.Data.Intent != intent {
			return CaptureDTO{}, workflow.ErrIdempotencyConflict
		}
		return s.toDTO(existing)
	} else if !workflow.IsCode(err, workflow.CodeNotFound) {
		return CaptureDTO{}, err
	}
	s.observe("started", "")
	data := CaptureData{Owner: memory.Owner{TenantID: input.Owner.TenantID, UserID: input.Owner.OwnerID}, Query: query, Intent: intent}
	state := workflow.WorkflowState[CaptureData]{Meta: workflow.RunMetadata{WorkflowID: "memory-capture", DefinitionVersion: s.config.DefinitionVersion, RunID: workflow.WorkflowRunID(runID), StartedAt: now}, Control: workflow.ControlState{Status: workflow.RunPending}, Budget: workflow.BudgetState{MaxSteps: s.config.MaxSteps, MaxResumes: s.config.MaxResumes, Deadline: now.Add(s.config.RunTTL)}, Data: data}
	result, err := s.runtime.Start(ctx, workflow.StartWorkflowInput[CaptureData]{Owner: input.Owner, IdempotencyKey: input.IdempotencyKey, State: state})
	if err != nil {
		s.observe("failed", captureErrorCode(err))
		return CaptureDTO{}, err
	}
	dto, err := s.toDTO(result)
	s.observeResult(dto, err)
	return dto, err
}

func (s *CaptureService) Get(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (CaptureDTO, error) {
	result, err := s.runtime.Get(ctx, owner, runID)
	if err != nil {
		return CaptureDTO{}, err
	}
	return s.toDTO(result)
}
func (s *CaptureService) GetReview(ctx context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (ReviewDTO, error) {
	result, err := s.runtime.Get(ctx, owner, runID)
	if err != nil {
		return ReviewDTO{}, err
	}
	if result.Status != workflow.RunSuspended || result.State.Data.Review == nil {
		return ReviewDTO{}, workflow.ErrNotFound
	}
	wait, err := s.runtime.GetCurrentWait(ctx, owner, runID)
	if err != nil {
		return ReviewDTO{}, err
	}
	return reviewDTO(result.State.Data.Review, wait.Point), nil
}

func (s *CaptureService) Resume(ctx context.Context, input ResumeCaptureInput) (CaptureDTO, error) {
	if s == nil || input.Owner.Validate() != nil || input.Actor.Validate() != nil {
		return CaptureDTO{}, ErrInvalidCaptureData
	}
	payloadRef := ""
	if input.Action == workflow.ActionSubmitEdit {
		if s.edits == nil || strings.TrimSpace(input.EditText) == "" {
			return CaptureDTO{}, ErrInvalidEditPayload
		}
		ref, err := s.edits.Create(ctx, memory.Owner{TenantID: input.Owner.TenantID, UserID: input.Owner.OwnerID}, input.EditText)
		if err != nil {
			return CaptureDTO{}, err
		}
		payloadRef = ref
	} else if strings.TrimSpace(input.EditText) != "" {
		return CaptureDTO{}, ErrInvalidEditPayload
	}
	command := workflow.ResumeCommand{RunID: input.RunID, WaitID: input.WaitID, Version: input.Version, ContentHash: input.ContentHash, Action: input.Action, PayloadRef: payloadRef}
	result, err := s.runtime.Resume(ctx, input.Owner, input.Actor, input.RunID, command)
	if err != nil {
		s.observe("failed", captureErrorCode(err))
		return CaptureDTO{}, err
	}
	switch input.Action {
	case workflow.ActionApprove:
		s.observe("approved", "")
	case workflow.ActionReject:
		s.observe("rejected", "")
	case workflow.ActionSubmitEdit:
		s.observe("edited", "")
	}
	dto, err := s.toDTO(result)
	s.observeResult(dto, err)
	return dto, err
}

func (s *CaptureService) observeResult(dto CaptureDTO, err error) {
	if err != nil {
		s.observe("failed", captureErrorCode(err))
		return
	}
	switch dto.Status {
	case string(workflow.RunSuspended):
		s.observe("suspended", "")
	case string(workflow.RunCompleted):
		s.observe("completed", "")
	}
}
func (s *CaptureService) observe(event, code string) {
	if s != nil && s.config.Telemetry != nil {
		s.config.Telemetry.ObserveCapture(event, code)
	}
}
func captureErrorCode(err error) string {
	switch workflow.CodeOf(err) {
	case workflow.CodeNotFound:
		return "not_found"
	case workflow.CodeInvalidResume:
		return "invalid_resume"
	case workflow.CodeWaitExpired:
		return "wait_expired"
	case workflow.CodeStateConflict:
		return "state_conflict"
	case workflow.CodeClaimConflict:
		return "claim_conflict"
	case workflow.CodeIdempotencyConflict:
		return "idempotency_conflict"
	}
	if errors.Is(err, ErrInvalidCaptureData) || errors.Is(err, ErrInvalidEditPayload) {
		return "invalid_request"
	}
	return "internal"
}

func (s *CaptureService) toDTO(result workflow.RunResult[CaptureData]) (CaptureDTO, error) {
	dto := CaptureDTO{RunID: string(result.State.Meta.RunID), Status: string(result.Status), Policy: result.State.Data.Policy, Committed: result.State.Data.Committed}
	if draft := result.State.Data.Draft; draft != nil {
		dto.Draft = &DraftDTO{Layer: draft.Layer, Kind: draft.Kind, Scope: draft.Scope, Namespace: draft.Namespace, SlotKey: draft.SlotKey, Entity: draft.Entity, CanonicalText: truncateRunes(draft.CanonicalText, s.config.MaxDraftChars), ContentHash: draft.ContentHash}
	}
	if result.State.Data.Review != nil && result.State.Control.PendingWait != nil {
		dto.ReviewPtr(result.State.Data.Review, *result.State.Control.PendingWait)
	}
	return dto, nil
}

func (d *CaptureDTO) ReviewPtr(review *ReviewState, wait workflow.WaitPoint) {
	value := reviewDTO(review, wait)
	d.Review = &value
}
func reviewDTO(review *ReviewState, wait workflow.WaitPoint) ReviewDTO {
	return ReviewDTO{WaitID: string(wait.ID), Version: wait.Version, ContentHash: wait.ContentHash, ExpiresAt: wait.ExpiresAt, AllowedActions: append([]workflow.HumanAction(nil), wait.AllowedActions...), Candidate: review.Candidate, CandidateRowVersion: review.CandidateRowVersion}
}
func truncateRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}
