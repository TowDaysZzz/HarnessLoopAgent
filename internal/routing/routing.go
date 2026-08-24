package routing

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type DomainIntent string

const (
	IntentNoteCreate     DomainIntent = "note.create"
	IntentNoteDelete     DomainIntent = "note.delete"
	IntentNoteQuery      DomainIntent = "note.query"
	IntentMemoryCapture  DomainIntent = "memory.capture"
	IntentMemoryRecall   DomainIntent = "memory.recall"
	IntentReminderCreate DomainIntent = "reminder.create"
	IntentReminderQuery  DomainIntent = "reminder.query"
	IntentReminderUpdate DomainIntent = "reminder.update"
	IntentReminderCancel DomainIntent = "reminder.cancel"
	IntentSkillInvoke    DomainIntent = "skill.invoke"
	IntentChat           DomainIntent = "chat"
	IntentUnclear        DomainIntent = "intent.unclear"
)

type TargetKind string

const (
	TargetBuiltin TargetKind = "builtin"
	TargetSkill   TargetKind = "skill"
)

type SkillTarget struct {
	ID            skill.ID        `json:"id"`
	Version       skill.Version   `json:"version"`
	Arguments     json.RawMessage `json:"-"`
	ArgumentsHash string          `json:"arguments_hash"`
}

type Complexity string

const (
	ComplexitySimple  Complexity = "simple"
	ComplexityComplex Complexity = "complex"
)

type RouteDecision struct {
	Target        TargetKind   `json:"target"`
	Skill         *SkillTarget `json:"skill,omitempty"`
	Intent        DomainIntent `json:"intent"`
	Complexity    Complexity   `json:"complexity"`
	Deterministic bool         `json:"deterministic"`
	NeedsRAG      bool         `json:"needs_rag"`
	NeedsModel    bool         `json:"needs_model"`
	Confidence    float64      `json:"confidence"`
	Reason        string       `json:"reason"`
}

func (d RouteDecision) IsSkill() bool { return d.Target == TargetSkill && d.Skill != nil }

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
	if containsAny(text, "忽略之前", "忽略系统", "绕过权限", "伪造身份", "修改tenant", "修改 tenant") && containsAny(text, "帮我记住", "记一笔", "保存笔记", "记录一下", "记下来", "删除笔记", "帮我删", "提醒我", "取消提醒") {
		return RouteDecision{Intent: IntentUnclear, Complexity: complexity, Deterministic: true, Confidence: 1, Reason: "prompt_injection_write"}
	}
	if containsAny(text, "取消提醒", "删除提醒", "不要提醒") || containsAny(text, "取消", "删除", "停止") && strings.Contains(text, "提醒") {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentReminderCancel, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "explicit_reminder_cancel"})
	}
	if containsAny(text, "提醒") && containsAny(text, "改到", "改成", "修改为", "推迟到", "提前到") {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentReminderUpdate, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "explicit_reminder_update"})
	}
	if containsAny(text, "有哪些提醒", "有什么提醒", "查看提醒", "查询提醒", "待办提醒") {
		return RouteDecision{Intent: IntentReminderQuery, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "explicit_reminder_query"}
	}
	if isMemoryRecall(text) {
		return RouteDecision{Intent: IntentMemoryRecall, Complexity: complexity, NeedsModel: true, Confidence: .96, Reason: "explicit_memory_recall"}
	}
	if isReminderCreate(text) {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentReminderCreate, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "future_reminder_write"})
	}
	if containsAny(text, "删除笔记", "删除这条笔记", "删掉笔记", "删除记录", "帮我删") {
		return RouteDecision{Intent: IntentNoteDelete, Complexity: complexity, Deterministic: true, Confidence: .98, Reason: "explicit_delete"}
	}
	// A request to turn conversation/history content into a note is a write
	// intent even when it also contains broad query words such as "历史记录".
	if isHistorySummaryWrite(text) {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentNoteCreate, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "history_summary_write"})
	}
	if containsAny(text, "帮我记住", "记住我", "请记得我") {
		return c.enforceWriteConfidence(RouteDecision{Intent: IntentMemoryCapture, Complexity: complexity, Deterministic: true, NeedsModel: true, Confidence: .98, Reason: "explicit_memory_capture"})
	}
	if containsAny(text, "记一笔", "保存笔记", "记录一下", "记下来", "总结刚才") {
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

func isMemoryRecall(text string) bool {
	if containsAny(text, "之前说过", "之前喜欢", "以前说过", "还记得", "我的偏好是什么", "记忆里") {
		return containsAny(text, "提醒我", "告诉我", "查询", "什么", "吗", "记得")
	}
	return false
}

func isReminderCreate(text string) bool {
	if !containsAny(text, "提醒我", "请提醒", "记得提醒") {
		return false
	}
	return containsAny(text, "今天", "明天", "后天", "下周", "下个月", "点", "时", "分钟后", "小时后", "日期", "上午", "下午", "晚上")
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
	SkillInvocation  *skill.Invocation
}

type Executor interface {
	Execute(ctx context.Context, run RunContext, input string) (Result, error)
}

type Result struct {
	Text              string
	Citations         []string
	NeedConfirm       bool
	Candidate         *NoteCandidate
	WorkflowCandidate *WorkflowCandidate
}

type WorkflowCandidate struct {
	Kind           string                   `json:"kind"`
	RunID          string                   `json:"run_id"`
	Status         string                   `json:"status"`
	WaitID         string                   `json:"wait_id,omitempty"`
	Version        uint64                   `json:"version,omitempty"`
	ContentHash    string                   `json:"content_hash,omitempty"`
	ExpiresAt      *time.Time               `json:"expires_at,omitempty"`
	AllowedActions []workflow.HumanAction   `json:"allowed_actions,omitempty"`
	Action         reminder.Action          `json:"action,omitempty"`
	Content        string                   `json:"content,omitempty"`
	ScheduledAt    *time.Time               `json:"scheduled_at,omitempty"`
	Timezone       string                   `json:"timezone,omitempty"`
	Target         *reminder.ReminderRef    `json:"target,omitempty"`
	TargetChoices  []reminder.ReminderRef   `json:"target_choices,omitempty"`
	MemorySummary  []reminder.MemorySummary `json:"memory_summary,omitempty"`
	Clarification  *reminder.Clarification  `json:"clarification,omitempty"`
}

type NoteCandidate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	ExpiresAt   string `json:"expires_at"`
}
