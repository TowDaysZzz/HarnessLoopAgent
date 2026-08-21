package routing

import (
	"context"
	"strings"
)

type DomainIntent string

const (
	IntentNoteCreate DomainIntent = "note.create"
	IntentNoteDelete DomainIntent = "note.delete"
	IntentNoteQuery  DomainIntent = "note.query"
	IntentChat       DomainIntent = "chat"
	IntentUnclear    DomainIntent = "intent.unclear"
)

type Complexity string

const (
	ComplexitySimple  Complexity = "simple"
	ComplexityComplex Complexity = "complex"
)

type RouteDecision struct {
	Intent        DomainIntent `json:"intent"`
	Complexity    Complexity   `json:"complexity"`
	Deterministic bool         `json:"deterministic"`
	NeedsRAG      bool         `json:"needs_rag"`
	NeedsModel    bool         `json:"needs_model"`
	Confidence    float64      `json:"confidence"`
	Reason        string       `json:"reason"`
}

type Classifier struct {
	ComplexThreshold   int
	MinWriteConfidence float64
}

func (c Classifier) Classify(input string) RouteDecision {
	text := strings.TrimSpace(input)
	complexity := c.classifyComplexity(text)
	if text == "" {
		return RouteDecision{Intent: IntentUnclear, Complexity: ComplexitySimple, Deterministic: true, Confidence: 1, Reason: "empty_input"}
	}
	if containsAny(text, "忽略之前", "忽略系统", "绕过权限", "伪造身份", "修改tenant", "修改 tenant") && containsAny(text, "帮我记住", "记一笔", "保存笔记", "记录一下", "记下来", "删除笔记", "帮我删") {
		return RouteDecision{Intent: IntentUnclear, Complexity: complexity, Deterministic: true, Confidence: 1, Reason: "prompt_injection_write"}
	}
	if containsAny(text, "删除笔记", "删除这条笔记", "删掉笔记", "删除记录", "帮我删") {
		return RouteDecision{Intent: IntentNoteDelete, Complexity: complexity, Deterministic: true, Confidence: .98, Reason: "explicit_delete"}
	}
	// A request to turn conversation/history content into a note is a write
	// intent even when it also contains broad query words such as "历史记录".
	if isHistorySummaryWrite(text) {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentNoteCreate, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "history_summary_write"})
	}
	if containsAny(text, "帮我记住", "记一笔", "保存笔记", "记录一下", "记下来", "总结刚才") {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentNoteCreate, Complexity: complexity, Deterministic: true, NeedsModel: containsAny(text, "总结刚才", "总结以上", "从聊天历史"), Confidence: .98, Reason: "explicit_note_write"})
	}
	if containsAny(text, "也许可以记录", "可能要记", "或许记下") {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentNoteCreate, Complexity: complexity, Deterministic: true, Confidence: .7, Reason: "implicit_note_write"})
	}
	if containsAny(text, "查询我之前", "请查询我之前", "之前关于", "之前的记录", "之前的", "以前的笔记", "以前的", "我记过", "我的笔记", "历史记录", "上次提到") {
		return RouteDecision{Intent: IntentNoteQuery, Complexity: complexity, NeedsRAG: true, NeedsModel: true, Confidence: .95, Reason: "historical_note_query"}
	}
	return RouteDecision{Intent: IntentChat, Complexity: complexity, NeedsModel: true, Confidence: .8, Reason: "default_chat"}
}

func isHistorySummaryWrite(text string) bool {
	if !containsAny(text, "总结", "整理", "归纳", "提取") || !containsAny(text, "笔记", "记一笔") {
		return false
	}
	return containsAny(text,
		"总结刚才", "总结以上", "聊天历史", "对话历史", "当前对话", "本次对话",
		"记成", "整理成", "归纳成", "生成一条笔记", "总结一条笔记", "提取一条笔记",
		"并记录", "并保存",
	)
}

func (c Classifier) enforceWriteConfidence(decision RouteDecision) RouteDecision {
	threshold := c.MinWriteConfidence
	if threshold <= 0 {
		threshold = .95
	}
	if decision.Confidence >= threshold {
		return decision
	}
	return RouteDecision{Intent: IntentUnclear, Complexity: decision.Complexity, Deterministic: true, Confidence: decision.Confidence, Reason: "low_write_confidence"}
}

func (c Classifier) classifyComplexity(text string) Complexity {
	threshold := c.ComplexThreshold
	if threshold < 1 {
		threshold = 120
	}
	if len([]rune(text)) >= threshold || containsAny(text, "分析并", "比较并", "制定方案", "综合", "对比分析") {
		return ComplexityComplex
	}
	return ComplexitySimple
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
	RunID            string
	ParentRunID      string
	SessionID        string
	UserID           uint64
	TenantID         uint64
	AccessToken      string
	KnowledgeBaseIDs []uint64
	Decision         RouteDecision
}

type Executor interface {
	Execute(ctx context.Context, run RunContext, input string) (Result, error)
}

type Result struct {
	Text        string
	Citations   []string
	NeedConfirm bool
	Candidate   *NoteCandidate
}

type NoteCandidate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	ExpiresAt   string `json:"expires_at"`
}
