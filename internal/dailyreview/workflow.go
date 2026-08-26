package dailyreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

var (
	ErrSourceChanging = errors.New("daily_review_source_changing")
	errSourceChanged  = errors.New("daily review source changed")
)

type MemoryContext struct {
	Refs           []MemoryRef
	Bodies         map[string]string
	EarliestExpiry *time.Time
}
type MemoryContextSource interface {
	Recall(context.Context, skill.Owner, Window) (MemoryContext, error)
	ValidatePinned(context.Context, skill.Owner, []MemoryRef, time.Time) error
}

type WorkflowConfig struct {
	Options                                  Options
	CacheTTL, CacheLease, CacheWait          time.Duration
	MaxSteps, MaxModelCalls, MaxToolCalls    int
	OutputSchemaVersion, PromptPolicyVersion string
}

type DailyReviewState struct {
	Owner          skill.Owner         `json:"owner"`
	Plan           PlanV1              `json:"plan"`
	Window         Window              `json:"window"`
	Snapshot       SourceSnapshot      `json:"snapshot"`
	CacheIdentity  CacheIdentity       `json:"cache_identity"`
	Cache          CacheRecord         `json:"cache"`
	CacheHit       bool                `json:"cache_hit"`
	OwnsCacheClaim bool                `json:"owns_cache_claim"`
	Evidence       Evidence            `json:"-"`
	MemoryRefs     []MemoryRef         `json:"memory_refs"`
	MemoryBodies   map[string]string   `json:"-"`
	MemoryExpiry   *time.Time          `json:"memory_expiry,omitempty"`
	Report         DailyReviewReportV1 `json:"report"`
	Rendered       string              `json:"rendered"`
}

type ReviewWorkflow struct {
	Reader    ActivityReader
	Cache     CacheRepository
	Memory    MemoryContextSource
	Generator StructuredGenerator
	Config    WorkflowConfig
	Observer  workflow.Observer
	Harness   runtime.Observer
	Now       func() time.Time
}

func (w ReviewWorkflow) Run(ctx context.Context, request skill.Request) (skill.Result, error) {
	if err := w.validate(); err != nil {
		return skill.Result{}, err
	}
	if w.Now == nil {
		w.Now = time.Now
	}
	var plan PlanV1
	if err := json.Unmarshal(request.Invocation.Arguments, &plan); err != nil {
		return skill.Result{}, ErrInvalidPlan
	}
	now := w.Now().UTC()
	for rebuild := 0; rebuild < 2; rebuild++ {
		result, steps, err := w.runOnce(ctx, request.Invocation, plan, now, rebuild)
		if errors.Is(err, errSourceChanged) {
			continue
		}
		if err != nil {
			return skill.Result{}, err
		}
		candidate, _ := json.Marshal(result.Report)
		cacheState := "miss"
		if result.CacheHit {
			cacheState = "hit"
		}
		return skill.Result{Text: result.Rendered, Candidate: candidate, CacheState: cacheState, Steps: steps}, nil
	}
	return skill.Result{}, ErrSourceChanging
}
func (w ReviewWorkflow) validate() error {
	if w.Reader.Chat == nil || w.Reader.Notes == nil || w.Reader.Memory == nil || w.Cache == nil || w.Memory == nil || w.Generator.Runner == nil || w.Config.CacheTTL <= 0 || w.Config.CacheLease <= 0 || w.Config.CacheWait <= 0 || w.Config.MaxSteps < 9 || w.Config.MaxModelCalls < 1 || w.Config.MaxToolCalls < 1 || w.Config.OutputSchemaVersion == "" || w.Config.PromptPolicyVersion == "" {
		return skill.ErrUnavailable
	}
	return nil
}

func (w ReviewWorkflow) runOnce(ctx context.Context, invocation skill.Invocation, plan PlanV1, now time.Time, rebuild int) (DailyReviewState, []string, error) {
	observer := w.Observer
	if observer == nil {
		observer = workflow.NoopObserver{}
	}
	harness := w.Harness
	if harness == nil {
		harness = runtime.NoopObserver{}
	}
	ctx, cancel, _ := runtime.Start(runtime.WithRunID(ctx, invocation.ChatRunID), runtime.Budget{RunTimeout: time.Minute, MaxModelCalls: w.Config.MaxModelCalls, MaxToolCalls: w.Config.MaxToolCalls}, harness)
	defer cancel()
	state := DailyReviewState{Owner: invocation.Owner, Plan: plan}
	nodes := []workflow.Node[DailyReviewState]{reviewNode{"resolve_window", func(ctx context.Context, s *DailyReviewState) error {
		window, err := ResolveWindow(s.Plan.Date, s.Plan.Timezone)
		s.Window = window
		return err
	}}, reviewNode{"snapshot_sources", func(ctx context.Context, s *DailyReviewState) error {
		snapshot, err := w.Reader.Snapshot(ctx, s.Owner, s.Window, w.Config.Options)
		s.Snapshot = snapshot
		return err
	}}, reviewNode{"lookup_or_claim_cache", func(ctx context.Context, s *DailyReviewState) error { return w.lookupOrClaim(ctx, s, invocation, now) }}, reviewNode{"load_daily_evidence", func(ctx context.Context, s *DailyReviewState) error {
		if s.CacheHit || len(s.Snapshot.Chat)+len(s.Snapshot.Notes) == 0 {
			return nil
		}
		if err := runtime.ConsumeToolCall(ctx); err != nil {
			return err
		}
		evidence, err := w.Reader.LoadPinned(ctx, s.Snapshot)
		s.Evidence = evidence
		return err
	}}, reviewNode{"recall_memory_context", func(ctx context.Context, s *DailyReviewState) error {
		if s.CacheHit || len(s.Snapshot.Chat)+len(s.Snapshot.Notes) == 0 || !w.Config.Options.IncludeMemory {
			return nil
		}
		if err := runtime.ConsumeToolCall(ctx); err != nil {
			return err
		}
		memoryContext, err := w.Memory.Recall(ctx, s.Owner, s.Window)
		if err == nil {
			s.MemoryRefs, s.MemoryBodies, s.MemoryExpiry = memoryContext.Refs, memoryContext.Bodies, memoryContext.EarliestExpiry
		}
		return err
	}}, reviewNode{"generate_structured_review", func(ctx context.Context, s *DailyReviewState) error {
		if s.CacheHit {
			return nil
		}
		if len(s.Snapshot.Chat)+len(s.Snapshot.Notes) == 0 {
			s.Report = EmptyReport(s.Window, s.Snapshot.Warnings)
			return nil
		}
		report, err := w.Generator.Generate(ctx, s.Snapshot, s.Evidence, s.MemoryBodies)
		s.Report = report
		return err
	}}, reviewNode{"validate_evidence", func(ctx context.Context, s *DailyReviewState) error {
		if s.CacheHit {
			return nil
		}
		report, err := ValidateEvidence(s.Report, s.Snapshot, s.MemoryRefs)
		s.Report = report
		runtime.Emit(ctx, runtime.Event{Stage: runtime.StageValidation, Name: "daily_review"})
		return err
	}}, reviewNode{"recheck_snapshot_commit_cache", func(ctx context.Context, s *DailyReviewState) error { return w.recheckAndCommit(ctx, s, now) }}, reviewNode{"render", func(_ context.Context, s *DailyReviewState) error {
		if s.CacheHit {
			return nil
		}
		s.Rendered = RenderReport(s.Report)
		return nil
	}}}
	collector := workflow.NewMemoryCollector()
	runner, err := workflow.NewRunner(nodes, workflow.RunnerOptions{Observer: correlatingObserver{next: workflowObserverFanout{collector, observer}, harness: harness, chatRunID: invocation.ChatRunID, invocationID: invocation.ID}, Now: w.Now})
	if err != nil {
		return state, nil, err
	}
	wfState := workflow.WorkflowState[DailyReviewState]{Meta: workflow.RunMetadata{WorkflowID: "daily_review", DefinitionVersion: "v1", RunID: workflow.WorkflowRunID(fmt.Sprintf("w%sr%d", strings.ReplaceAll(invocation.ID, "-", ""), rebuild)), Source: workflow.SourceRef{Type: "skill_invocation", ID: invocation.ID}, StartedAt: now}, Control: workflow.ControlState{Status: workflow.RunPending}, Budget: workflow.BudgetState{MaxSteps: w.Config.MaxSteps, Deadline: now.Add(time.Minute)}, Data: state}
	result, err := runner.Run(ctx, wfState)
	steps := make([]string, 0, 9)
	for _, event := range collector.Events() {
		if event.Type == workflow.EventNodeCompleted {
			steps = append(steps, string(event.NodeID))
		}
	}
	if err != nil {
		w.releaseFailedClaim(ctx, &result.State.Data, err)
	}
	return result.State.Data, steps, err
}

func (w ReviewWorkflow) releaseFailedClaim(ctx context.Context, state *DailyReviewState, cause error) {
	if state == nil || !state.OwnsCacheClaim || state.Cache.ID == "" || state.Cache.ClaimToken == "" {
		return
	}
	code := string(workflow.CodeOf(cause))
	if code == "" {
		code = "workflow_failed"
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := w.Cache.FailClaim(cleanupCtx, state.Owner, state.Cache.ID, state.Cache.ClaimToken, code, w.Now().UTC()); err == nil {
		state.OwnsCacheClaim = false
	}
}

func (w ReviewWorkflow) lookupOrClaim(ctx context.Context, s *DailyReviewState, invocation skill.Invocation, now time.Time) error {
	optionsHash, _ := OptionsHash(w.Config.Options)
	identity := CacheIdentity{Owner: s.Owner, Window: s.Window, OptionsHash: optionsHash, SkillID: string(invocation.Skill.ID), SkillVersion: string(invocation.Skill.Version), SchemaVersion: w.Config.OutputSchemaVersion, PromptPolicyVersion: w.Config.PromptPolicyVersion}
	logical, err := identity.LogicalKey()
	if err != nil {
		return err
	}
	source, err := SourceFingerprint(s.Snapshot)
	if err != nil {
		return err
	}
	s.CacheIdentity = identity
	if cached, err := w.Cache.Lookup(ctx, s.Owner, logical, source, now); err == nil {
		return useCached(s, cached)
	}
	claim, err := w.Cache.Claim(ctx, s.Owner, logical, source, now, w.Config.CacheLease)
	if err != nil {
		return err
	}
	s.Cache = claim.Record
	if claim.Generator {
		s.OwnsCacheClaim = true
		return nil
	}
	deadline := time.Now().Add(w.Config.CacheWait)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			cached, err := w.Cache.Lookup(ctx, s.Owner, logical, source, time.Now().UTC())
			if err == nil {
				return useCached(s, cached)
			}
		}
	}
	return errors.New("daily review cache wait timeout")
}
func useCached(s *DailyReviewState, cached CacheRecord) error {
	if err := cached.Result.Validate(128 * 1024); err != nil {
		return err
	}
	report, err := DecodeReportV1(cached.Result.Structured)
	if err != nil {
		return err
	}
	s.Cache, s.CacheHit, s.Report, s.Rendered = cached, true, report, cached.Result.Rendered
	return nil
}
func (w ReviewWorkflow) recheckAndCommit(ctx context.Context, s *DailyReviewState, now time.Time) error {
	if s.CacheHit {
		return nil
	}
	if len(s.MemoryRefs) > 0 {
		if err := w.Memory.ValidatePinned(ctx, s.Owner, s.MemoryRefs, now); err != nil {
			_ = w.Cache.FailClaim(ctx, s.Owner, s.Cache.ID, s.Cache.ClaimToken, "stale_memory", now)
			s.OwnsCacheClaim = false
			return errSourceChanged
		}
	}
	latest, err := w.Reader.Snapshot(ctx, s.Owner, s.Window, w.Config.Options)
	if err != nil {
		return err
	}
	if latest.Digest != s.Snapshot.Digest {
		_ = w.Cache.FailClaim(ctx, s.Owner, s.Cache.ID, s.Cache.ClaimToken, "source_changed", now)
		s.OwnsCacheClaim = false
		return errSourceChanged
	}
	rendered := RenderReport(s.Report)
	structured, _ := json.Marshal(s.Report)
	result := CachedResult{Structured: structured, Rendered: rendered, EvidenceHash: evidenceListHash(s.Report.EvidenceRefs), ContentHash: ContentHash(rendered)}
	validUntil := ComputeValidUntil(now, w.Config.CacheTTL, s.MemoryExpiry, nil)
	record, err := w.Cache.CommitReady(ctx, s.Owner, s.Cache.ID, s.Cache.ClaimToken, result, validUntil, now)
	if err == nil {
		s.Cache, s.Rendered = record, rendered
		s.OwnsCacheClaim = false
	}
	return err
}
func evidenceListHash(refs []EvidenceRef) string {
	raw, _ := json.Marshal(refs)
	return ContentHash(string(raw))
}

type reviewNode struct {
	id  workflow.NodeID
	run func(context.Context, *DailyReviewState) error
}

func (n reviewNode) ID() workflow.NodeID { return n.id }
func (n reviewNode) Execute(ctx context.Context, input workflow.NodeInput[DailyReviewState]) (workflow.NodeResult[DailyReviewState], error) {
	state := input.State
	if err := n.run(ctx, &state.Data); err != nil {
		return workflow.NodeResult[DailyReviewState]{State: state}, err
	}
	return workflow.NodeResult[DailyReviewState]{State: state, Directive: workflow.DirectiveContinue}, nil
}

type correlatingObserver struct {
	next                    workflow.Observer
	harness                 runtime.Observer
	chatRunID, invocationID string
}

type workflowObserverFanout []workflow.Observer

func (o workflowObserverFanout) Observe(ctx context.Context, event workflow.NodeEvent) error {
	for _, next := range o {
		if next != nil {
			if err := next.Observe(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o correlatingObserver) Observe(ctx context.Context, event workflow.NodeEvent) error {
	o.harness.Observe(ctx, runtime.Event{RunID: o.chatRunID, Stage: runtime.Stage("skill.step." + strings.TrimPrefix(string(event.Type), "node.")), Name: string(event.NodeID), Attempt: event.Attempt, Duration: event.Duration, Fields: map[string]any{"skill_invocation_id": o.invocationID, "workflow_run_id": event.RunID}})
	return o.next.Observe(ctx, event)
}

type RecallAdapter struct {
	Service interface {
		Recall(context.Context, memory.RecallRequest, time.Time) (memory.RecallResult, error)
	}
	Candidates      memory.ContextRefSource
	Target          int
	MaxContextChars int
}

func (a RecallAdapter) Recall(ctx context.Context, owner skill.Owner, window Window) (MemoryContext, error) {
	memoryOwner := memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID}
	var pinned []memory.MemoryRef
	if a.Candidates != nil {
		refs, err := a.Candidates.ListActiveContextRefs(ctx, memoryOwner, []memory.Kind{memory.KindGoal, memory.KindPreference, memory.KindConstraint, memory.KindSummary, memory.KindOutcome}, window.End, a.Target)
		if err != nil {
			return MemoryContext{}, err
		}
		pinned = refs
	}
	result, err := a.Service.Recall(ctx, memory.RecallRequest{Owner: memoryOwner, Query: "每日回顾相关的目标、偏好、约束、摘要与结果", Scope: memory.Scope{Type: memory.ScopeUser}, Pinned: pinned, Target: a.Target, MaxContextChars: a.MaxContextChars}, window.End)
	if err != nil {
		return MemoryContext{}, err
	}
	out := MemoryContext{Bodies: map[string]string{}}
	for _, item := range result.Items {
		record := item.Memory
		out.Refs = append(out.Refs, MemoryRef{ID: record.ID, LineageVersion: record.LineageVersion, ContentHash: record.ContentHash, ExpiresAt: record.ExpiresAt})
		out.Bodies[record.ID] = record.CanonicalText
		if record.ExpiresAt != nil && (out.EarliestExpiry == nil || record.ExpiresAt.Before(*out.EarliestExpiry)) {
			value := record.ExpiresAt.UTC()
			out.EarliestExpiry = &value
		}
	}
	return out, nil
}
func (a RecallAdapter) ValidatePinned(ctx context.Context, owner skill.Owner, refs []MemoryRef, now time.Time) error {
	pinned := make([]memory.MemoryRef, 0, len(refs))
	for _, ref := range refs {
		pinned = append(pinned, memory.MemoryRef{ID: ref.ID, LineageVersion: ref.LineageVersion, ContentHash: ref.ContentHash})
	}
	result, err := a.Service.Recall(ctx, memory.RecallRequest{Owner: memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID}, Query: "validate pinned daily review memory", Scope: memory.Scope{Type: memory.ScopeUser}, Pinned: pinned, Target: len(pinned), MaxContextChars: a.MaxContextChars}, now)
	if err != nil {
		return err
	}
	if len(result.Items) != len(pinned) {
		return ErrStaleSnapshot
	}
	return nil
}

var _ skill.WorkflowHandler = ReviewWorkflow{}
