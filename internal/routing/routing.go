package routing

import (
	"context"
	"strings"
)

type Intent string

const (
	IntentNoteCreate Intent = "note.create"
	IntentNoteDelete Intent = "note.delete"
	IntentNoteQuery  Intent = "note.query"
	IntentChat       Intent = "chat.simple"
	IntentComplex    Intent = "task.complex"
	IntentUnclear    Intent = "intent.unclear"
)

type RouteDecision struct {
	Intent        Intent  `json:"intent"`
	Deterministic bool    `json:"deterministic"`
	NeedsRAG      bool    `json:"needs_rag"`
	NeedsModel    bool    `json:"needs_model"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
}

type Classifier struct {
	ComplexThreshold int
}

func (c Classifier) Classify(input string) RouteDecision {
	text := strings.TrimSpace(input)
	if text == "" {
		return RouteDecision{Intent: IntentUnclear, Deterministic: true, Confidence: 1, Reason: "empty_input"}
	}
	if containsAny(text, "删除笔记", "删除这条笔记", "删掉笔记", "删除记录", "帮我删") {
		return RouteDecision{Intent: IntentNoteDelete, Deterministic: true, Confidence: .98, Reason: "explicit_delete"}
	}
	if containsAny(text, "帮我记住", "记一笔", "保存笔记", "记录一下", "记下来") {
		return RouteDecision{Intent: IntentNoteCreate, Deterministic: true, Confidence: .98, Reason: "explicit_note_write"}
	}
	if containsAny(text, "之前的记录", "之前的", "以前的笔记", "以前的", "我记过", "我的笔记", "历史记录", "上次提到") {
		return RouteDecision{Intent: IntentNoteQuery, Deterministic: true, NeedsRAG: true, NeedsModel: true, Confidence: .95, Reason: "historical_note_query"}
	}
	threshold := c.ComplexThreshold
	if threshold < 1 {
		threshold = 120
	}
	if len([]rune(text)) >= threshold || containsAny(text, "分析并", "比较并", "制定方案", "综合") {
		return RouteDecision{Intent: IntentComplex, NeedsModel: true, Confidence: .65, Reason: "complex_language"}
	}
	return RouteDecision{Intent: IntentChat, NeedsModel: true, Confidence: .8, Reason: "default_chat"}
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

type RunContext struct {
	RunID       string
	ParentRunID string
	UserID      uint64
	TenantID    uint64
	Decision    RouteDecision
}

type Executor interface {
	Execute(ctx context.Context, run RunContext, input string) (Result, error)
}

type Result struct {
	Text        string
	Citations   []string
	NeedConfirm bool
}
