package chat

import (
	"strings"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type RetrievalReason string

const (
	RetrievalReasonExplicitNoteReference RetrievalReason = "explicit_note_reference"
	RetrievalReasonContextualFollowUp    RetrievalReason = "contextual_note_followup"
	RetrievalReasonNotRequired           RetrievalReason = "not_required"
)

func (r RetrievalReason) Valid() bool {
	switch r {
	case RetrievalReasonExplicitNoteReference, RetrievalReasonContextualFollowUp, RetrievalReasonNotRequired:
		return true
	default:
		return false
	}
}

type RetrievalDecision struct {
	Required bool
	Reason   RetrievalReason
}

type RetrievalDecider interface {
	Decide(messages []agent.Message) RetrievalDecision
}

type RuleRetrievalDecider struct{}

func (RuleRetrievalDecider) Decide(messages []agent.Message) RetrievalDecision {
	currentIndex, current := lastUserMessage(messages)
	if currentIndex < 0 {
		return RetrievalDecision{Reason: RetrievalReasonNotRequired}
	}
	if explicitlyReferencesNotes(current) {
		return RetrievalDecision{Required: true, Reason: RetrievalReasonExplicitNoteReference}
	}
	if switchesTopic(current) || !looksLikeFollowUp(current) {
		return RetrievalDecision{Reason: RetrievalReasonNotRequired}
	}

	previousUsers := 0
	for index := currentIndex - 1; index >= 0 && previousUsers < 3; index-- {
		if messages[index].Role != "user" {
			continue
		}
		previousUsers++
		previous := strings.TrimSpace(messages[index].Content)
		if switchesTopic(previous) {
			break
		}
		if explicitlyReferencesNotes(previous) {
			return RetrievalDecision{Required: true, Reason: RetrievalReasonContextualFollowUp}
		}
	}
	return RetrievalDecision{Reason: RetrievalReasonNotRequired}
}

func lastUserMessage(messages []agent.Message) (int, string) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return index, strings.TrimSpace(messages[index].Content)
		}
	}
	return -1, ""
}

func explicitlyReferencesNotes(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"笔记", "记录", "知识库", "文档", "我写过", "我之前写", "上次写",
		"note", "notes", "knowledge base", "document",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeFollowUp(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"其中", "里面", "第一点", "第二点", "第三点", "这点", "这些", "这个", "它们",
		"继续", "展开", "再详细", "具体", "为什么", "那",
		"it", "that", "those", "continue", "elaborate", "why",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func switchesTopic(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{"换个话题", "另一个问题", "不谈前面", "忽略前面", "new topic", "unrelated question"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
