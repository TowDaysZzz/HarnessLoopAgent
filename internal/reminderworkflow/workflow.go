package reminderworkflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

var ErrInvalidCommandData = errors.New("invalid reminder command data")

const DefaultMaxCheckpointBytes = 32 * 1024

type ReviewState struct {
	WaitVersion uint64 `json:"wait_version"`
	ContentHash string `json:"content_hash"`
	Decision    string `json:"decision,omitempty"`
	ActorType   string `json:"actor_type,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	PayloadRef  string `json:"payload_ref,omitempty"`
}

type CommandData struct {
	Owner         reminder.Owner           `json:"owner"`
	Query         string                   `json:"query"`
	ReceivedAt    time.Time                `json:"received_at"`
	TrustedTarget *reminder.ReminderRef    `json:"trusted_target,omitempty"`
	Plan          *reminder.CommandPlan    `json:"plan,omitempty"`
	ScheduledAt   *time.Time               `json:"scheduled_at,omitempty"`
	Target        *reminder.ReminderRef    `json:"target,omitempty"`
	TargetChoices []reminder.ReminderRef   `json:"target_choices,omitempty"`
	MemoryRefs    []reminder.MemoryRef     `json:"memory_refs,omitempty"`
	MemorySummary []reminder.MemorySummary `json:"memory_summary,omitempty"`
	Clarification *reminder.Clarification  `json:"clarification,omitempty"`
	Review        *ReviewState             `json:"review,omitempty"`
	Committed     *reminder.Reminder       `json:"committed,omitempty"`
}

func (d CommandData) Validate() error {
	if !d.Owner.Valid() || strings.TrimSpace(d.Query) == "" || len(d.Query) > 4096 || d.ReceivedAt.IsZero() || len(d.MemoryRefs) > reminder.MaxMemoryRefs || len(d.MemorySummary) > reminder.MaxMemoryRefs || len(d.TargetChoices) > reminder.MaxPageSize {
		return ErrInvalidCommandData
	}
	if d.TrustedTarget != nil && d.TrustedTarget.Validate() != nil || d.Target != nil && d.Target.Validate() != nil {
		return ErrInvalidCommandData
	}
	for _, ref := range d.MemoryRefs {
		if ref.Validate() != nil {
			return ErrInvalidCommandData
		}
	}
	for _, summary := range d.MemorySummary {
		if strings.TrimSpace(summary.ID) == "" || summary.LineageVersion == 0 || len(summary.ContentHash) != reminder.ContentHashHexSize || len([]rune(summary.UntrustedText)) > reminder.MaxMemorySummaryChars+64 {
			return ErrInvalidCommandData
		}
	}
	if d.Plan != nil {
		copyPlan := *d.Plan
		if err := copyPlan.Normalize(0); err != nil {
			return ErrInvalidCommandData
		}
	}
	if d.Review != nil && (d.Review.WaitVersion == 0 || len(d.Review.ContentHash) != reminder.ContentHashHexSize || len(d.Review.ActorID) > 128 || len(d.Review.PayloadRef) > 500) {
		return ErrInvalidCommandData
	}
	return nil
}

type CommandCodec struct{ MaxBytes int }

func (CommandCodec) SchemaID() string      { return "reminder-command" }
func (CommandCodec) SchemaVersion() uint64 { return 1 }
func (c CommandCodec) limit() int {
	if c.MaxBytes <= 0 {
		return DefaultMaxCheckpointBytes
	}
	return c.MaxBytes
}
func (c CommandCodec) Encode(value CommandData) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > c.limit() || containsCredential(raw) {
		return nil, ErrInvalidCommandData
	}
	return raw, nil
}
func (c CommandCodec) Decode(raw []byte) (CommandData, error) {
	if len(raw) == 0 || len(raw) > c.limit() || containsCredential(raw) {
		return CommandData{}, ErrInvalidCommandData
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value CommandData
	if err := decoder.Decode(&value); err != nil {
		return CommandData{}, err
	}
	return value, value.Validate()
}
func containsCredential(raw []byte) bool {
	lower := strings.ToLower(string(raw))
	for _, term := range []string{"access_token", "refresh_token", "authorization", "cookie", "password", "passwd", "api_key", "private_key", "bearer "} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

type Planner interface {
	Plan(context.Context, string, time.Time) (reminder.CommandPlan, error)
}

type ParseNode struct{ Planner Planner }

func (ParseNode) ID() workflow.NodeID { return "reminder-parse" }
func (n ParseNode) Execute(ctx context.Context, input workflow.NodeInput[CommandData]) (workflow.NodeResult[CommandData], error) {
	if n.Planner == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	plan, err := n.Planner.Plan(ctx, input.State.Data.Query, input.State.Data.ReceivedAt)
	if err != nil {
		return workflow.NodeResult[CommandData]{}, err
	}
	if plan.Action == reminder.ActionQuery {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	input.State.Data.Plan = &plan
	input.State.Data.Clarification = plan.Clarification
	return continued(input.State), nil
}

type ResolveNode struct{ MaxHorizon time.Duration }

func (ResolveNode) ID() workflow.NodeID { return "reminder-resolve" }
func (n ResolveNode) Execute(_ context.Context, input workflow.NodeInput[CommandData]) (workflow.NodeResult[CommandData], error) {
	if input.State.Data.Plan == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	plan := input.State.Data.Plan
	if plan.Trigger != nil {
		resolved, err := reminder.ResolveTrigger(*plan.Trigger, input.State.Data.ReceivedAt, n.MaxHorizon)
		if err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
		input.State.Data.ScheduledAt = &resolved
	}
	return continued(input.State), nil
}

type RecallNode struct {
	Service reminder.MemoryRecallPort
}

func (RecallNode) ID() workflow.NodeID { return "reminder-recall" }
func (n RecallNode) Execute(ctx context.Context, input workflow.NodeInput[CommandData]) (workflow.NodeResult[CommandData], error) {
	if input.State.Data.Plan == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	selectors := input.State.Data.Plan.MemorySelectors
	if len(selectors) == 0 {
		return continued(input.State), nil
	}
	if n.Service == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	plan, err := reminder.BuildMemoryRecallPlan(selectors, nil)
	if err != nil {
		return workflow.NodeResult[CommandData]{}, err
	}
	association, err := reminder.ResolveMemoryAssociation(ctx, n.Service, input.State.Data.Owner, plan, input.State.Data.ReceivedAt)
	if err != nil {
		return workflow.NodeResult[CommandData]{}, err
	}
	input.State.Data.MemoryRefs, input.State.Data.MemorySummary = association.Refs, association.Summaries
	if association.Clarification != nil {
		input.State.Data.Clarification = association.Clarification
	}
	return continued(input.State), nil
}

type ConflictNode struct{ Repository reminder.Repository }

func (ConflictNode) ID() workflow.NodeID { return "reminder-conflict" }
func (n ConflictNode) Execute(ctx context.Context, input workflow.NodeInput[CommandData]) (workflow.NodeResult[CommandData], error) {
	if n.Repository == nil || input.State.Data.Plan == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	if input.State.Data.Plan.Action == reminder.ActionCreate {
		return continued(input.State), nil
	}
	if input.State.Data.TrustedTarget != nil {
		value, err := n.Repository.Get(ctx, input.State.Data.Owner, input.State.Data.TrustedTarget.ID)
		if err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
		if value.RowVersion != input.State.Data.TrustedTarget.RowVersion {
			return workflow.NodeResult[CommandData]{}, reminder.ErrStateConflict
		}
		ref := *input.State.Data.TrustedTarget
		input.State.Data.Target = &ref
		return continued(input.State), nil
	}
	query, err := targetQuery(input.State.Data.Owner, input.State.Data.Plan.Target)
	if err != nil {
		return workflow.NodeResult[CommandData]{}, err
	}
	page, err := n.Repository.List(ctx, query)
	if err != nil {
		return workflow.NodeResult[CommandData]{}, err
	}
	input.State.Data.TargetChoices = make([]reminder.ReminderRef, 0, len(page.Items))
	for _, item := range page.Items {
		input.State.Data.TargetChoices = append(input.State.Data.TargetChoices, reminder.ReminderRef{ID: item.ID, RowVersion: item.RowVersion})
	}
	if len(input.State.Data.TargetChoices) == 1 {
		ref := input.State.Data.TargetChoices[0]
		input.State.Data.Target = &ref
	} else {
		input.State.Data.Clarification = &reminder.Clarification{Needed: true, Reason: "reminder_target_not_unique", Question: "请从候选中选择一个明确的提醒。"}
	}
	return continued(input.State), nil
}

func targetQuery(owner reminder.Owner, target *reminder.TargetSelector) (reminder.Query, error) {
	if target == nil {
		return reminder.Query{}, ErrInvalidCommandData
	}
	query := reminder.Query{Owner: owner, Label: target.Label, Statuses: append([]reminder.Status(nil), target.Statuses...), Limit: reminder.MaxPageSize}
	if target.From != "" {
		value, err := time.Parse(time.RFC3339, target.From)
		if err != nil {
			return reminder.Query{}, err
		}
		value = value.UTC()
		query.From = &value
	}
	if target.Until != "" {
		value, err := time.Parse(time.RFC3339, target.Until)
		if err != nil {
			return reminder.Query{}, err
		}
		value = value.UTC()
		query.Until = &value
	}
	return query, query.Validate()
}

type ReviewNode struct {
	TTL        time.Duration
	Now        func() time.Time
	EditLoader EditLoader
	Evaluator  *Evaluator
}

func (ReviewNode) ID() workflow.NodeID { return "reminder-review" }
func (n ReviewNode) Execute(ctx context.Context, input workflow.NodeInput[CommandData]) (workflow.NodeResult[CommandData], error) {
	if input.State.Data.Plan == nil || n.TTL <= 0 {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	if input.Resume == nil {
		hash, err := candidateHash(input.State.Data)
		if err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
		input.State.Data.Review = &ReviewState{WaitVersion: 1, ContentHash: hash}
		return n.suspend(input.State, now)
	}
	actor, ok := workflow.ResolvedActorFromContext(ctx)
	if !ok || input.State.Data.Review == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	review := input.State.Data.Review
	review.ActorType, review.ActorID = actor.Type, actor.ID
	switch input.Resume.Action {
	case workflow.ActionApprove:
		if input.State.Data.Clarification != nil && input.State.Data.Clarification.Needed {
			return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
		}
		review.Decision = "approved"
		return continued(input.State), nil
	case workflow.ActionReject:
		review.Decision = "rejected"
		return continued(input.State), nil
	case workflow.ActionSubmitEdit:
		if n.EditLoader == nil || n.Evaluator == nil || input.Resume.PayloadRef == "" {
			return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
		}
		query, err := n.EditLoader.LoadReminderEdit(ctx, input.State.Data.Owner, input.Resume.PayloadRef)
		if err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
		data := CommandData{Owner: input.State.Data.Owner, Query: query, ReceivedAt: now, TrustedTarget: input.State.Data.TrustedTarget}
		data, err = n.Evaluator.Evaluate(ctx, data)
		if err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
		data.Review = &ReviewState{WaitVersion: review.WaitVersion + 1, PayloadRef: input.Resume.PayloadRef}
		data.Review.ContentHash, err = candidateHash(data)
		if err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
		input.State.Data = data
		return n.suspend(input.State, now)
	default:
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
}
func (n ReviewNode) suspend(state workflow.WorkflowState[CommandData], now time.Time) (workflow.NodeResult[CommandData], error) {
	actions := []workflow.HumanAction{workflow.ActionApprove, workflow.ActionReject, workflow.ActionSubmitEdit}
	if state.Data.Clarification != nil && state.Data.Clarification.Needed {
		actions = []workflow.HumanAction{workflow.ActionSubmitEdit, workflow.ActionReject}
	}
	wait := workflow.WaitRequest{ID: workflow.WaitID(uuid.NewString()), RunID: state.Meta.RunID, NodeID: n.ID(), Kind: workflow.WaitReview, Version: state.Data.Review.WaitVersion, ContentHash: state.Data.Review.ContentHash, AllowedActions: actions, PayloadRef: state.Data.Review.PayloadRef, ExpiresAt: now.Add(n.TTL)}
	return workflow.NodeResult[CommandData]{State: state, Directive: workflow.DirectiveSuspend, Wait: &wait}, nil
}

type CommitNode struct {
	Repository       reminder.Repository
	MemoryRepository memory.Repository
	Now              func() time.Time
}

func (CommitNode) ID() workflow.NodeID { return "reminder-commit" }
func (n CommitNode) Execute(ctx context.Context, input workflow.NodeInput[CommandData]) (workflow.NodeResult[CommandData], error) {
	data := &input.State.Data
	if n.Repository == nil || data.Plan == nil || data.Review == nil {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	if data.Review.Decision == "rejected" {
		return continued(input.State), nil
	}
	if data.Review.Decision != "approved" {
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	if len(data.MemoryRefs) > 0 {
		if err := reminder.ValidatePinnedMemoryRefs(ctx, n.MemoryRepository, data.Owner, data.MemoryRefs, now); err != nil {
			return workflow.NodeResult[CommandData]{}, err
		}
	}
	actor := data.Review.ActorType + ":" + data.Review.ActorID
	var result reminder.MutationResult
	var err error
	switch data.Plan.Action {
	case reminder.ActionCreate:
		if data.ScheduledAt == nil {
			return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
		}
		hash, hashErr := reminder.ComputeContentHash(data.Plan.Content, reminder.DefaultTimezone, *data.ScheduledAt, data.MemoryRefs)
		if hashErr != nil {
			return workflow.NodeResult[CommandData]{}, hashErr
		}
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(input.State.Meta.RunID+":reminder")).String()
		value := reminder.Reminder{ID: id, Owner: data.Owner, Content: data.Plan.Content, ContentHash: hash, Timezone: reminder.DefaultTimezone, NextFireAt: *data.ScheduledAt, Status: reminder.StatusScheduled, RowVersion: 1, MemoryRefs: data.MemoryRefs, Source: reminder.SourceRef{Type: "workflow", ID: string(input.State.Meta.RunID)}, CreatedAt: now, UpdatedAt: now}
		result, err = n.Repository.Create(ctx, reminder.CreateInput{Reminder: value, IdempotencyKey: input.ExecutionID + ":0", InputHash: hash, Actor: actor, ReasonCode: "user_review"})
	case reminder.ActionUpdate:
		if data.Target == nil || data.ScheduledAt == nil {
			return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
		}
		hash, hashErr := reminder.ComputeContentHash(data.Plan.Content, reminder.DefaultTimezone, *data.ScheduledAt, data.MemoryRefs)
		if hashErr != nil {
			return workflow.NodeResult[CommandData]{}, hashErr
		}
		result, err = n.Repository.Update(ctx, reminder.MutationInput{Owner: data.Owner, Target: *data.Target, Content: data.Plan.Content, Timezone: reminder.DefaultTimezone, NextFireAt: *data.ScheduledAt, MemoryRefs: data.MemoryRefs, IdempotencyKey: input.ExecutionID + ":0", InputHash: hash, ReplacementHash: hash, Actor: actor, ReasonCode: "user_review", OccurredAt: now})
	case reminder.ActionCancel:
		if data.Target == nil {
			return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
		}
		sum := sha256.Sum256([]byte(data.Target.ID + ":cancel"))
		hash := hex.EncodeToString(sum[:])
		result, err = n.Repository.Cancel(ctx, reminder.MutationInput{Owner: data.Owner, Target: *data.Target, IdempotencyKey: input.ExecutionID + ":0", InputHash: hash, Actor: actor, ReasonCode: "user_review", OccurredAt: now})
	default:
		return workflow.NodeResult[CommandData]{}, ErrInvalidCommandData
	}
	if err != nil {
		return workflow.NodeResult[CommandData]{}, err
	}
	data.Committed = &result.Reminder
	return continued(input.State), nil
}

type Evaluator struct {
	Planner    Planner
	Recall     reminder.MemoryRecallPort
	Repository reminder.Repository
	MaxHorizon time.Duration
}

func (e *Evaluator) Evaluate(ctx context.Context, data CommandData) (CommandData, error) {
	if e == nil {
		return CommandData{}, ErrInvalidCommandData
	}
	state := workflow.WorkflowState[CommandData]{Data: data}
	for _, node := range []workflow.Node[CommandData]{ParseNode{Planner: e.Planner}, ResolveNode{MaxHorizon: e.MaxHorizon}, RecallNode{Service: e.Recall}, ConflictNode{Repository: e.Repository}} {
		result, err := node.Execute(ctx, workflow.NodeInput[CommandData]{State: state})
		if err != nil {
			return CommandData{}, err
		}
		state = result.State
	}
	return state.Data, nil
}

func candidateHash(data CommandData) (string, error) {
	copyData := struct {
		Plan          *reminder.CommandPlan   `json:"plan"`
		ScheduledAt   *time.Time              `json:"scheduled_at"`
		Target        *reminder.ReminderRef   `json:"target"`
		MemoryRefs    []reminder.MemoryRef    `json:"memory_refs"`
		Clarification *reminder.Clarification `json:"clarification"`
	}{data.Plan, data.ScheduledAt, data.Target, data.MemoryRefs, data.Clarification}
	raw, err := json.Marshal(copyData)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func continued(state workflow.WorkflowState[CommandData]) workflow.NodeResult[CommandData] {
	return workflow.NodeResult[CommandData]{State: state, Directive: workflow.DirectiveContinue}
}

func NewNodes(evaluator *Evaluator, review ReviewNode, commit CommitNode) []workflow.Node[CommandData] {
	review.Evaluator = evaluator
	return []workflow.Node[CommandData]{ParseNode{Planner: evaluator.Planner}, ResolveNode{MaxHorizon: evaluator.MaxHorizon}, RecallNode{Service: evaluator.Recall}, ConflictNode{Repository: evaluator.Repository}, review, commit}
}

func ValidateNodeOrder(nodes []workflow.Node[CommandData]) error {
	want := []workflow.NodeID{"reminder-parse", "reminder-resolve", "reminder-recall", "reminder-conflict", "reminder-review", "reminder-commit"}
	if len(nodes) != len(want) {
		return fmt.Errorf("%w: node count", ErrInvalidCommandData)
	}
	for i := range want {
		if nodes[i] == nil || nodes[i].ID() != want[i] {
			return fmt.Errorf("%w: node order", ErrInvalidCommandData)
		}
	}
	return nil
}
