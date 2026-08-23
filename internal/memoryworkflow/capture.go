package memoryworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

var (
	ErrInvalidCaptureData  = errors.New("invalid memory capture data")
	ErrPinnedMemoryChanged = errors.New("pinned memory changed")
)

type Draft struct {
	Layer           memory.Layer           `json:"layer"`
	Kind            memory.Kind            `json:"kind"`
	Scope           memory.Scope           `json:"scope"`
	Namespace       string                 `json:"namespace"`
	SlotKey         string                 `json:"slot_key,omitempty"`
	Entity          memory.EntityRef       `json:"entity,omitempty"`
	CanonicalText   string                 `json:"canonical_text"`
	StructuredValue memory.StructuredValue `json:"structured_value"`
	ContentHash     string                 `json:"content_hash"`
	Authority       memory.Authority       `json:"authority"`
	Confidence      float64                `json:"confidence"`
	Salience        float64                `json:"salience"`
	Source          memory.SourceRef       `json:"source"`
	ExpiresAt       *time.Time             `json:"expires_at,omitempty"`
}

func (d *Draft) Normalize() error {
	if d == nil {
		return ErrInvalidCaptureData
	}
	text, value, hash, err := memory.NormalizeContent(d.CanonicalText, d.StructuredValue)
	if err != nil {
		return err
	}
	d.CanonicalText, d.StructuredValue, d.ContentHash = text, value, hash
	if d.Namespace == "" || d.Authority.Rank() == 0 || d.Confidence < 0 || d.Confidence > 1 || d.Salience < 0 || d.Salience > 1 {
		return ErrInvalidCaptureData
	}
	return nil
}

type PolicyResult struct {
	Action         memory.PolicyAction `json:"action"`
	TargetMemoryID string              `json:"target_memory_id,omitempty"`
	NeedsReview    bool                `json:"needs_review"`
	ReasonCode     string              `json:"reason_code"`
}

type ReviewState struct {
	Candidate           memory.MemoryRef `json:"candidate"`
	CandidateRowVersion uint64           `json:"candidate_row_version"`
	WaitVersion         uint64           `json:"wait_version"`
	Decision            string           `json:"decision,omitempty"`
	ActorType           string           `json:"actor_type,omitempty"`
	ActorID             string           `json:"actor_id,omitempty"`
	PayloadRef          string           `json:"payload_ref,omitempty"`
}

type CaptureData struct {
	Owner       memory.Owner       `json:"owner"`
	Query       string             `json:"query"`
	Pinned      []memory.MemoryRef `json:"pinned,omitempty"`
	Draft       *Draft             `json:"draft,omitempty"`
	Policy      *PolicyResult      `json:"policy,omitempty"`
	Review      *ReviewState       `json:"review,omitempty"`
	Committed   *memory.MemoryRef  `json:"committed,omitempty"`
	RecallStats *RecallStats       `json:"recall_stats,omitempty"`
}

type RecallStats struct {
	Scanned, ObsoleteFiltered int
	DegradationReason         string
}

func (d CaptureData) Validate() error {
	if !d.Owner.Valid() || len(d.Query) > 4096 || len(d.Pinned) > 20 {
		return ErrInvalidCaptureData
	}
	for _, ref := range d.Pinned {
		if ref.ID == "" || ref.LineageVersion == 0 || len(ref.ContentHash) != 64 {
			return ErrInvalidCaptureData
		}
	}
	if d.Draft != nil {
		copy := *d.Draft
		if err := copy.Normalize(); err != nil || copy.ContentHash != d.Draft.ContentHash {
			return ErrInvalidCaptureData
		}
	}
	if d.Policy != nil && (len(d.Policy.ReasonCode) > 64 || len(d.Policy.TargetMemoryID) > 64) {
		return ErrInvalidCaptureData
	}
	if d.Review != nil && (d.Review.CandidateRowVersion == 0 || d.Review.WaitVersion == 0 || len(d.Review.PayloadRef) > 500 || len(d.Review.ActorID) > 128) {
		return ErrInvalidCaptureData
	}
	return nil
}

type CaptureCodec struct {
	ID       string
	Version  uint64
	MaxBytes int
}

func (c CaptureCodec) SchemaID() string {
	if c.ID == "" {
		return "memory-capture"
	}
	return c.ID
}
func (c CaptureCodec) SchemaVersion() uint64 {
	if c.Version == 0 {
		return 1
	}
	return c.Version
}
func (c CaptureCodec) Encode(value CaptureData) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	limit := c.MaxBytes
	if limit == 0 {
		limit = 32 * 1024
	}
	if len(raw) > limit {
		return nil, ErrInvalidCaptureData
	}
	lower := strings.ToLower(string(raw))
	for _, term := range []string{"access_token", "refresh_token", "authorization", "cookie", "password", "api_key", "bearer "} {
		if strings.Contains(lower, term) {
			return nil, ErrInvalidCaptureData
		}
	}
	return raw, nil
}
func (c CaptureCodec) Decode(raw []byte) (CaptureData, error) {
	limit := c.MaxBytes
	if limit == 0 {
		limit = 32 * 1024
	}
	if len(raw) == 0 || len(raw) > limit {
		return CaptureData{}, ErrInvalidCaptureData
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var value CaptureData
	if err := dec.Decode(&value); err != nil {
		return CaptureData{}, err
	}
	return value, value.Validate()
}

type RecallPort interface {
	Recall(context.Context, memory.RecallRequest, time.Time) (memory.RecallResult, error)
}
type DraftExtractor interface {
	ExtractMemoryDraft(context.Context, memory.Owner, string) (Draft, error)
}
type ConflictResolver interface {
	ResolveMemoryConflict(context.Context, memory.Owner, Draft) (PolicyResult, error)
}
type EditLoader interface {
	LoadEditedMemoryDraft(context.Context, memory.Owner, string) (Draft, error)
}

type RecallNode struct {
	Service RecallPort
	Now     func() time.Time
}

func (RecallNode) ID() workflow.NodeID { return "memory-recall" }
func (n RecallNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Service == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	if err := ValidatePinned(ctx, input.State.Data.Owner, input.State.Data.Pinned, nil); err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	result, err := n.Service.Recall(ctx, memory.RecallRequest{Owner: input.State.Data.Owner, Query: input.State.Data.Query, Scope: memory.Scope{Type: memory.ScopeUser}, Pinned: input.State.Data.Pinned}, now)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	refs := make([]memory.MemoryRef, 0, len(result.Items))
	for _, item := range result.Items {
		refs = append(refs, item.Memory.Ref())
	}
	input.State.Data.Pinned = refs
	input.State.Data.RecallStats = &RecallStats{Scanned: result.Scanned, ObsoleteFiltered: result.ObsoleteFiltered, DegradationReason: result.DegradationReason}
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

type ExtractNode struct{ Extractor DraftExtractor }

func (ExtractNode) ID() workflow.NodeID { return "memory-extract" }
func (n ExtractNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Extractor == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	draft, err := n.Extractor.ExtractMemoryDraft(ctx, input.State.Data.Owner, input.State.Data.Query)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	if err := draft.Normalize(); err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	input.State.Data.Draft = &draft
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

type ConflictNode struct{ Resolver ConflictResolver }

func (ConflictNode) ID() workflow.NodeID { return "memory-conflict" }
func (n ConflictNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Resolver == nil || input.State.Data.Draft == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	policy, err := n.Resolver.ResolveMemoryConflict(ctx, input.State.Data.Owner, *input.State.Data.Draft)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	input.State.Data.Policy = &policy
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

type ReviewNode struct {
	Repository memory.Repository
	Resolver   ConflictResolver
	EditLoader EditLoader
	Now        func() time.Time
	TTL        time.Duration
}

func (ReviewNode) ID() workflow.NodeID { return "memory-review" }
func (n ReviewNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Repository == nil || input.State.Data.Draft == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	if n.TTL <= 0 {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	if input.Resume == nil {
		result, err := n.createCandidate(ctx, input, input.State.Data.Draft, nil, now, 0)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		input.State.Data.Review = &ReviewState{Candidate: result.Memory.Ref(), CandidateRowVersion: result.Memory.RowVersion, WaitVersion: 1}
		return n.suspend(input.State, now)
	}
	if err := ValidatePinned(ctx, input.State.Data.Owner, input.State.Data.Pinned, n.Repository); err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	actor, ok := workflow.ResolvedActorFromContext(ctx)
	if !ok {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	review := input.State.Data.Review
	if review == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	review.ActorType, review.ActorID = actor.Type, actor.ID
	switch input.Resume.Action {
	case workflow.ActionApprove:
		review.Decision = "approved"
		return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
	case workflow.ActionReject:
		review.Decision = "rejected"
		return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
	case workflow.ActionSubmitEdit:
		if n.EditLoader == nil || n.Resolver == nil || input.Resume.PayloadRef == "" {
			return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
		}
		edited, err := n.EditLoader.LoadEditedMemoryDraft(ctx, input.State.Data.Owner, input.Resume.PayloadRef)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		if err := edited.Normalize(); err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		oldRecords, err := n.Repository.BatchGet(ctx, input.State.Data.Owner, []string{review.Candidate.ID})
		if err != nil || len(oldRecords) != 1 {
			return workflow.NodeResult[CaptureData]{}, ErrPinnedMemoryChanged
		}
		result, err := n.createCandidate(ctx, input, &edited, &oldRecords[0], now, 1)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		policy, err := n.Resolver.ResolveMemoryConflict(ctx, input.State.Data.Owner, edited)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		input.State.Data.Draft = &edited
		input.State.Data.Policy = &policy
		input.State.Data.Review = &ReviewState{Candidate: result.Memory.Ref(), CandidateRowVersion: result.Memory.RowVersion, WaitVersion: review.WaitVersion + 1, PayloadRef: input.Resume.PayloadRef}
		return n.suspend(input.State, now)
	default:
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
}
func (n ReviewNode) createCandidate(ctx context.Context, input workflow.NodeInput[CaptureData], draft *Draft, old *memory.Record, now time.Time, index int) (memory.MutationResult, error) {
	record := draftRecord(input.State.Data.Owner, *draft, now)
	targets := []memory.MutationTarget{}
	if old != nil {
		record.LineageID = old.LineageID
		record.LineageVersion = old.LineageVersion + 1
		record.SupersedesID = old.ID
		targets = append(targets, memory.MutationTarget{ID: old.ID, ExpectedRowVersion: old.RowVersion, NewStatus: memory.StatusRejected})
	}
	return n.Repository.CommitMutation(ctx, memory.Mutation{Owner: record.Owner, NewMemory: &record, Targets: targets, Actor: "workflow", ReasonCode: "review_candidate", IdempotencyKey: fmt.Sprintf("%s:%d", input.ExecutionID, index), InputHash: record.ContentHash, OccurredAt: now})
}
func (n ReviewNode) suspend(state workflow.WorkflowState[CaptureData], now time.Time) (workflow.NodeResult[CaptureData], error) {
	review := state.Data.Review
	wait := workflow.WaitRequest{ID: workflow.WaitID(uuid.NewString()), RunID: state.Meta.RunID, NodeID: n.ID(), Kind: workflow.WaitReview, Version: review.WaitVersion, ContentHash: review.Candidate.ContentHash, AllowedActions: []workflow.HumanAction{workflow.ActionApprove, workflow.ActionReject, workflow.ActionSubmitEdit}, PayloadRef: review.PayloadRef, ExpiresAt: now.Add(n.TTL)}
	return workflow.NodeResult[CaptureData]{State: state, Directive: workflow.DirectiveSuspend, Wait: &wait}, nil
}

type CommitNode struct {
	Repository memory.Repository
	Now        func() time.Time
}

func (CommitNode) ID() workflow.NodeID { return "memory-commit" }
func (n CommitNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Repository == nil || input.State.Data.Review == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	review := input.State.Data.Review
	var status memory.Status
	switch review.Decision {
	case "approved":
		status = memory.StatusActive
	case "rejected":
		status = memory.StatusRejected
	default:
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	result, err := n.Repository.TransitionMemory(ctx, input.State.Data.Owner, review.Candidate.ID, review.CandidateRowVersion, status, review.ActorType+":"+review.ActorID, "user_review", input.ExecutionID+":0", review.Candidate.ContentHash, now)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	ref := result.Memory.Ref()
	input.State.Data.Committed = &ref
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

func ValidatePinned(ctx context.Context, owner memory.Owner, refs []memory.MemoryRef, repository memory.Repository) error {
	if len(refs) == 0 {
		return nil
	}
	if repository == nil {
		return nil
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	values, err := repository.BatchGet(ctx, owner, ids)
	if err != nil {
		return err
	}
	byID := map[string]memory.Record{}
	for _, v := range values {
		byID[v.ID] = v
	}
	now := time.Now().UTC()
	for _, ref := range refs {
		v, ok := byID[ref.ID]
		if !ok || !v.IsActiveAt(now) || v.LineageVersion != ref.LineageVersion || v.ContentHash != ref.ContentHash {
			return ErrPinnedMemoryChanged
		}
	}
	return nil
}

func draftRecord(owner memory.Owner, d Draft, now time.Time) memory.Record {
	return memory.Record{ID: uuid.NewString(), Owner: owner, Layer: d.Layer, Kind: d.Kind, Scope: d.Scope, Namespace: d.Namespace, SlotKey: d.SlotKey, Entity: d.Entity, LineageID: uuid.NewString(), LineageVersion: 1, RowVersion: 1, CanonicalText: d.CanonicalText, StructuredValue: d.StructuredValue, ContentHash: d.ContentHash, Authority: d.Authority, Confidence: d.Confidence, Salience: d.Salience, Source: d.Source, Status: memory.StatusCandidate, ExpiresAt: d.ExpiresAt, CreatedAt: now, UpdatedAt: now}
}

type Nodes struct {
	Recall   RecallNode
	Extract  ExtractNode
	Conflict ConflictNode
	Review   ReviewNode
	Commit   CommitNode
}

func (n Nodes) List() []workflow.Node[CaptureData] {
	return []workflow.Node[CaptureData]{n.Recall, n.Extract, n.Conflict, n.Review, n.Commit}
}
