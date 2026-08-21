package routing

import (
	"context"
	"strings"
)

type PendingDraftLookup interface {
	HasPending(context.Context, uint64, uint64, string) (bool, error)
}

type RouteInput struct {
	UserID    uint64
	TenantID  uint64
	SessionID string
	Content   string
}

type Router struct {
	Classifier Classifier
	Drafts     PendingDraftLookup
}

func (r Router) Route(ctx context.Context, input RouteInput) RouteDecision {
	text := strings.TrimSpace(input.Content)
	action, ok := draftAction(text)
	if !ok {
		return r.Classifier.Classify(text)
	}
	hasPending := false
	if r.Drafts != nil {
		hasPending, _ = r.Drafts.HasPending(ctx, input.UserID, input.TenantID, input.SessionID)
	}
	if !hasPending {
		return RouteDecision{Intent: IntentUnclear, Complexity: ComplexitySimple, Deterministic: true, Confidence: 1, Reason: "missing_pending_draft"}
	}
	return RouteDecision{
		Intent: IntentNoteCreate, Complexity: r.Classifier.classifyComplexity(text), Deterministic: true,
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
