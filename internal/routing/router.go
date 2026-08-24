package routing

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

type PendingDraftLookup interface {
	HasPending(context.Context, uint64, uint64, string) (bool, error)
}

type RouteInput struct {
	UserID    uint64
	TenantID  uint64
	SessionID string
	Content   string
	Now       time.Time
}

type Router struct {
	Classifier         Classifier
	Drafts             PendingDraftLookup
	Skills             *skill.Registry
	MinSkillConfidence float64
}

func (r Router) Route(ctx context.Context, input RouteInput) RouteDecision {
	text := strings.TrimSpace(input.Content)
	action, ok := draftAction(text)
	if !ok {
		decision := r.Classifier.Classify(text)
		if decision.Intent != IntentChat || decision.Reason != "default_chat" || r.Skills == nil {
			if decision.Target == "" {
				decision.Target = TargetBuiltin
			}
			return decision
		}
		now := input.Now
		if now.IsZero() {
			now = time.Now()
		}
		matched, found, err := r.Skills.Match(ctx, skill.MatchInput{Content: text, SessionID: input.SessionID, NowUnix: now.Unix()})
		if err != nil {
			reason := "skill_match_failed"
			if errors.Is(err, skill.ErrAmbiguous) {
				reason = "skill_match_ambiguous"
			}
			return RouteDecision{Target: TargetBuiltin, Intent: IntentUnclear, Complexity: ComplexitySimple, Deterministic: true, Confidence: 0, Reason: reason}
		}
		if !found {
			decision.Target = TargetBuiltin
			return decision
		}
		threshold := r.MinSkillConfidence
		if threshold <= 0 {
			threshold = .9
		}
		if matched.Confidence < threshold {
			return RouteDecision{Target: TargetBuiltin, Intent: IntentUnclear, Complexity: ComplexitySimple, Deterministic: true, Confidence: matched.Confidence, Reason: matched.Reason}
		}
		return RouteDecision{Target: TargetSkill, Skill: &SkillTarget{ID: matched.Ref.ID, Version: matched.Ref.Version, Arguments: matched.Arguments, ArgumentsHash: matched.ArgumentsHash}, Intent: IntentSkillInvoke, Complexity: r.Classifier.classifyComplexity(text), Deterministic: true, NeedsModel: true, Confidence: matched.Confidence, Reason: matched.Reason}
	}
	hasPending := false
	if r.Drafts != nil {
		hasPending, _ = r.Drafts.HasPending(ctx, input.UserID, input.TenantID, input.SessionID)
	}
	if !hasPending {
		return RouteDecision{Target: TargetBuiltin, Intent: IntentUnclear, Complexity: ComplexitySimple, Deterministic: true, Confidence: 1, Reason: "missing_pending_draft"}
	}
	return RouteDecision{
		Target: TargetBuiltin, Intent: IntentNoteCreate, Complexity: r.Classifier.classifyComplexity(text), Deterministic: true,
		NeedsModel: action == "draft_modify", Confidence: 1, Reason: action,
	}
}

func draftAction(text string) (string, bool) {
	switch {
	case containsAny(text, "确认保存", "保存这条", "就保存它", "确认这条"):
		return "draft_confirm", true
	case containsAny(text, "取消保存", "不要保存", "放弃这条"):
		return "draft_cancel", true
	case containsAny(text, "修改为", "改成", "调整为"):
		return "draft_modify", true
	default:
		return "", false
	}
}
