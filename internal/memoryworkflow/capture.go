package memoryworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

type Draft = memory.MemoryDraft

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

type CandidateRef struct {
	Memory      memory.MemoryRef   `json:"memory"`
	MatchSource memory.MatchSource `json:"match_source"`
}

type CandidateStats struct {
	Matched  int `json:"matched"`
	Reloaded int `json:"reloaded"`
}

type CaptureData struct {
	Owner          memory.Owner           `json:"owner"`
	Query          string                 `json:"query"`
	Intent         memory.IntentAuthority `json:"intent,omitempty"`
	Candidates     []CandidateRef         `json:"candidates,omitempty"`
	CandidateStats *CandidateStats        `json:"candidate_stats,omitempty"`
	Draft          *Draft                 `json:"draft,omitempty"`
	Policy         *PolicyResult          `json:"policy,omitempty"`
	Review         *ReviewState           `json:"review,omitempty"`
	Committed      *memory.MemoryRef      `json:"committed,omitempty"`
}

func (d CaptureData) Validate() error {
	if !d.Owner.Valid() || len(d.Query) > 4096 || len(d.Candidates) > memory.MaxExactQuerySelectors {
		return ErrInvalidCaptureData
	}
	for _, candidate := range d.Candidates {
		ref := candidate.Memory
		if ref.ID == "" || ref.LineageVersion == 0 || len(ref.ContentHash) != 64 || candidate.MatchSource.Priority() == 0 {
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
	ResolveMemoryConflict(context.Context, memory.Owner, Draft, []memory.Record, memory.IntentAuthority) (PolicyResult, error)
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
	refs := make([]memory.MemoryRef, 0, len(input.State.Data.Candidates))
	for _, candidate := range input.State.Data.Candidates {
		refs = append(refs, candidate.Memory)
	}
	result, err := n.Service.Recall(ctx, memory.RecallRequest{Owner: input.State.Data.Owner, Query: input.State.Data.Query, Scope: memory.Scope{Type: memory.ScopeUser}, Pinned: refs}, now)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	returnedRefs := make([]memory.MemoryRef, 0, len(result.Items))
	for _, item := range result.Items {
		returnedRefs = append(returnedRefs, item.Memory.Ref())
	}
	input.State.Data.Candidates = make([]CandidateRef, 0, len(returnedRefs))
	for _, ref := range returnedRefs {
		input.State.Data.Candidates = append(input.State.Data.Candidates, CandidateRef{Memory: ref, MatchSource: memory.MatchPinned})
	}
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

type ExactCandidateLookupNode struct {
	Repository    memory.Repository
	Now           func() time.Time
	MaxCandidates int
}

func (ExactCandidateLookupNode) ID() workflow.NodeID { return "memory-exact-candidate-lookup" }
func (n ExactCandidateLookupNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Repository == nil || input.State.Data.Draft == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	limit := n.MaxCandidates
	if limit == 0 {
		limit = 40
	}
	if limit < 1 || limit > 200 {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	refs, err := lookupExactCandidates(ctx, n.Repository, input.State.Data.Owner, *input.State.Data.Draft, now, limit)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	input.State.Data.Candidates = refs
	input.State.Data.CandidateStats = &CandidateStats{Matched: len(refs)}
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

type ConflictNode struct {
	Resolver   ConflictResolver
	Repository memory.Repository
	Now        func() time.Time
}

func (ConflictNode) ID() workflow.NodeID { return "memory-conflict" }
func (n ConflictNode) Execute(ctx context.Context, input workflow.NodeInput[CaptureData]) (workflow.NodeResult[CaptureData], error) {
	if n.Resolver == nil || n.Repository == nil || input.State.Data.Draft == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	records, err := reloadCandidates(ctx, n.Repository, input.State.Data.Owner, input.State.Data.Candidates, now)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	intent := input.State.Data.Intent
	if intent == "" {
		intent = memory.IntentUserStatement
	}
	policy, err := n.Resolver.ResolveMemoryConflict(ctx, input.State.Data.Owner, *input.State.Data.Draft, records, intent)
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	input.State.Data.Policy = &policy
	if input.State.Data.CandidateStats == nil {
		input.State.Data.CandidateStats = &CandidateStats{}
	}
	input.State.Data.CandidateStats.Reloaded = len(records)
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

type ReviewNode struct {
	Repository    memory.Repository
	Resolver      ConflictResolver
	EditLoader    EditLoader
	Now           func() time.Time
	TTL           time.Duration
	MaxCandidates int
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
		if input.State.Data.Policy != nil && input.State.Data.Policy.Action == memory.ActionNoop {
			targetID := input.State.Data.Policy.TargetMemoryID
			values, err := n.Repository.BatchGet(ctx, input.State.Data.Owner, []string{targetID})
			if err != nil {
				return workflow.NodeResult[CaptureData]{}, err
			}
			if len(values) != 1 || !values[0].IsActiveAt(now) {
				return workflow.NodeResult[CaptureData]{}, ErrPinnedMemoryChanged
			}
			ref := values[0].Ref()
			input.State.Data.Committed = &ref
			return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
		}
		result, err := n.createCandidate(ctx, input, input.State.Data.Draft, nil, now, 0)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		input.State.Data.Review = &ReviewState{Candidate: result.Memory.Ref(), CandidateRowVersion: result.Memory.RowVersion, WaitVersion: 1}
		return n.suspend(input.State, now)
	}
	if _, err := reloadCandidates(ctx, n.Repository, input.State.Data.Owner, input.State.Data.Candidates, now); err != nil {
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
	reviewRecords, err := n.Repository.BatchGet(ctx, input.State.Data.Owner, []string{review.Candidate.ID})
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	if len(reviewRecords) != 1 || reviewRecords[0].LineageVersion != review.Candidate.LineageVersion || reviewRecords[0].ContentHash != review.Candidate.ContentHash {
		return workflow.NodeResult[CaptureData]{}, ErrPinnedMemoryChanged
	}
	reviewRecord := reviewRecords[0]
	alreadyResolved := (input.Resume.Action == workflow.ActionApprove && reviewRecord.Status == memory.StatusActive) || (input.Resume.Action == workflow.ActionReject && reviewRecord.Status == memory.StatusRejected)
	if !(reviewRecord.Status == memory.StatusCandidate && reviewRecord.RowVersion == review.CandidateRowVersion) && !(alreadyResolved && reviewRecord.RowVersion == review.CandidateRowVersion+1) {
		return workflow.NodeResult[CaptureData]{}, ErrPinnedMemoryChanged
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
		limit := n.MaxCandidates
		if limit == 0 {
			limit = 40
		}
		candidateRefs, err := lookupExactCandidates(ctx, n.Repository, input.State.Data.Owner, edited, now, limit)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		candidateRecords, err := reloadCandidates(ctx, n.Repository, input.State.Data.Owner, candidateRefs, now)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		intent := input.State.Data.Intent
		if intent == "" {
			intent = memory.IntentUserStatement
		}
		policy, err := n.Resolver.ResolveMemoryConflict(ctx, input.State.Data.Owner, edited, candidateRecords, intent)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		result, err := n.createCandidate(ctx, input, &edited, &reviewRecord, now, 1)
		if err != nil {
			return workflow.NodeResult[CaptureData]{}, err
		}
		input.State.Data.Draft = &edited
		input.State.Data.Policy = &policy
		input.State.Data.Candidates = candidateRefs
		input.State.Data.CandidateStats = &CandidateStats{Matched: len(candidateRefs), Reloaded: len(candidateRecords)}
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
	if input.State.Data.Policy != nil && input.State.Data.Policy.Action == memory.ActionSupersede {
		targetID := input.State.Data.Policy.TargetMemoryID
		values, err := n.Repository.BatchGet(ctx, input.State.Data.Owner, []string{targetID})
		if err != nil {
			return memory.MutationResult{}, err
		}
		if len(values) != 1 || !values[0].IsActiveAt(now) {
			return memory.MutationResult{}, ErrPinnedMemoryChanged
		}
		target := values[0]
		if old == nil {
			record.LineageID = target.LineageID
			record.LineageVersion = target.LineageVersion + 1
		}
		record.SupersedesID = target.ID
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
	if n.Repository == nil {
		return workflow.NodeResult[CaptureData]{}, ErrInvalidCaptureData
	}
	if input.State.Data.Review == nil && input.State.Data.Committed != nil && input.State.Data.Policy != nil && input.State.Data.Policy.Action == memory.ActionNoop {
		return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
	}
	if input.State.Data.Review == nil {
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
	var result memory.MutationResult
	var err error
	if status == memory.StatusActive && input.State.Data.Policy != nil && input.State.Data.Policy.Action == memory.ActionSupersede {
		targetID := input.State.Data.Policy.TargetMemoryID
		values, loadErr := n.Repository.BatchGet(ctx, input.State.Data.Owner, []string{targetID})
		if loadErr != nil {
			return workflow.NodeResult[CaptureData]{}, loadErr
		}
		if len(values) != 1 || !values[0].IsActiveAt(now) {
			return workflow.NodeResult[CaptureData]{}, ErrPinnedMemoryChanged
		}
		result, err = n.Repository.ActivateCandidateSuperseding(ctx, memory.CandidateActivation{Owner: input.State.Data.Owner, CandidateID: review.Candidate.ID, CandidateVersion: review.CandidateRowVersion, SupersededID: targetID, TargetVersion: values[0].RowVersion, Actor: review.ActorType + ":" + review.ActorID, ReasonCode: "user_correction", IdempotencyKey: input.ExecutionID + ":0", InputHash: review.Candidate.ContentHash, OccurredAt: now})
	} else {
		result, err = n.Repository.TransitionMemory(ctx, input.State.Data.Owner, review.Candidate.ID, review.CandidateRowVersion, status, review.ActorType+":"+review.ActorID, "user_review", input.ExecutionID+":0", review.Candidate.ContentHash, now)
	}
	if err != nil {
		return workflow.NodeResult[CaptureData]{}, err
	}
	ref := result.Memory.Ref()
	input.State.Data.Committed = &ref
	return workflow.NodeResult[CaptureData]{State: input.State, Directive: workflow.DirectiveContinue}, nil
}

func lookupExactCandidates(ctx context.Context, repository memory.Repository, owner memory.Owner, draft Draft, now time.Time, limit int) ([]CandidateRef, error) {
	if repository == nil || !owner.Valid() || limit < 1 || limit > 200 {
		return nil, ErrInvalidCaptureData
	}
	copyDraft := draft
	if err := copyDraft.Normalize(); err != nil {
		return nil, err
	}
	query := memory.ExactQuery{Owner: owner, Scope: copyDraft.Scope, Layers: []memory.Layer{copyDraft.Layer}, Kinds: []memory.Kind{copyDraft.Kind}, ActiveAt: &now, ContentHashes: []string{copyDraft.ContentHash}, Limit: limit}
	if copyDraft.SlotKey != "" {
		query.Slots = []memory.SlotSelector{{Namespace: copyDraft.Namespace, SlotKey: copyDraft.SlotKey}}
	}
	if !copyDraft.Entity.Empty() {
		query.Entities = []memory.EntityRef{copyDraft.Entity}
	}
	values, err := repository.FindExact(ctx, query)
	if err != nil {
		return nil, err
	}
	refs := make([]CandidateRef, 0, len(values))
	for _, value := range values {
		source := memory.MatchHash
		if value.Namespace == copyDraft.Namespace && value.SlotKey != "" && value.SlotKey == copyDraft.SlotKey {
			source = memory.MatchSlot
		}
		if !copyDraft.Entity.Empty() && value.Entity == copyDraft.Entity {
			source = memory.MatchEntity
		}
		refs = append(refs, CandidateRef{Memory: value.Ref(), MatchSource: source})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].MatchSource.Priority() != refs[j].MatchSource.Priority() {
			return refs[i].MatchSource.Priority() > refs[j].MatchSource.Priority()
		}
		return refs[i].Memory.ID < refs[j].Memory.ID
	})
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, nil
}

func reloadCandidates(ctx context.Context, repository memory.Repository, owner memory.Owner, refs []CandidateRef, now time.Time) ([]memory.Record, error) {
	if len(refs) == 0 {
		return []memory.Record{}, nil
	}
	if repository == nil || !owner.Valid() || len(refs) > memory.MaxExactQuerySelectors {
		return nil, ErrInvalidCaptureData
	}
	ids := make([]string, len(refs))
	for i, candidate := range refs {
		ids[i] = candidate.Memory.ID
	}
	values, err := repository.BatchGet(ctx, owner, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]memory.Record{}
	for _, value := range values {
		byID[value.ID] = value
	}
	result := make([]memory.Record, 0, len(refs))
	for _, candidate := range refs {
		ref := candidate.Memory
		value, ok := byID[ref.ID]
		if !ok || !value.IsActiveAt(now) || value.LineageVersion != ref.LineageVersion || value.ContentHash != ref.ContentHash {
			return nil, ErrPinnedMemoryChanged
		}
		result = append(result, value)
	}
	return result, nil
}

func draftRecord(owner memory.Owner, d Draft, now time.Time) memory.Record {
	return memory.Record{ID: uuid.NewString(), Owner: owner, Layer: d.Layer, Kind: d.Kind, Scope: d.Scope, Namespace: d.Namespace, SlotKey: d.SlotKey, Entity: d.Entity, LineageID: uuid.NewString(), LineageVersion: 1, RowVersion: 1, CanonicalText: d.CanonicalText, StructuredValue: d.StructuredValue, ContentHash: d.ContentHash, Authority: d.Authority, Confidence: d.Confidence, Salience: d.Salience, Source: d.Source, Status: memory.StatusCandidate, ExpiresAt: d.ExpiresAt, CreatedAt: now, UpdatedAt: now}
}

type Nodes struct {
	Extract              ExtractNode
	ExactCandidateLookup ExactCandidateLookupNode
	Conflict             ConflictNode
	Review               ReviewNode
	Commit               CommitNode
}

func (n Nodes) List() []workflow.Node[CaptureData] {
	return []workflow.Node[CaptureData]{n.Extract, n.ExactCandidateLookup, n.Conflict, n.Review, n.Commit}
}
