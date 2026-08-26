package chat

import (
	"errors"
	"testing"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type fixedTokenCounter struct{}

func (fixedTokenCounter) Count(text string) int { return len(text) }

func TestBoundedAssemblerKeepsMostRecentMessages(t *testing.T) {
	assembler := NewBoundedAssembler(15, 2, fixedTokenCounter{})
	result, err := assembler.Build([]agent.Message{
		{Role: "user", Content: "oldest"},
		{Role: "assistant", Content: "old"},
		{Role: "user", Content: "new"},
	})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if !result.Truncated || len(result.Messages) != 2 || result.Messages[0].Content != "old" {
		t.Fatalf("unexpected bounded context: %+v", result)
	}
}

func TestBoundedAssemblerRejectsNewestMessageOutsideBudget(t *testing.T) {
	assembler := NewBoundedAssembler(6, 1, fixedTokenCounter{})
	_, err := assembler.Build([]agent.Message{{Role: "user", Content: "new"}})
	if !errors.Is(err, ErrContextBudgetTooSmall) {
		t.Fatalf("expected budget error, got %v", err)
	}
}

func TestRuleRetrievalDecider(t *testing.T) {
	tests := []struct {
		name     string
		messages []agent.Message
		want     RetrievalDecision
	}{
		{
			name:     "explicit note question",
			messages: []agent.Message{{Role: "user", Content: "我的笔记里怎么描述重试策略？"}},
			want:     RetrievalDecision{Required: true, Reason: RetrievalReasonExplicitNoteReference},
		},
		{
			name: "contextual follow up",
			messages: []agent.Message{
				{Role: "user", Content: "总结我笔记里的重试策略"},
				{Role: "assistant", Content: "有三点。"},
				{Role: "user", Content: "第二点为什么这样设计？"},
			},
			want: RetrievalDecision{Required: true, Reason: RetrievalReasonContextualFollowUp},
		},
		{
			name: "chained contextual follow up",
			messages: []agent.Message{
				{Role: "user", Content: "总结我笔记里的重试策略"},
				{Role: "assistant", Content: "有三点。"},
				{Role: "user", Content: "第二点展开说说"},
				{Role: "assistant", Content: "它控制等待时间。"},
				{Role: "user", Content: "那为什么要这样做？"},
			},
			want: RetrievalDecision{Required: true, Reason: RetrievalReasonContextualFollowUp},
		},
		{
			name: "topic switch",
			messages: []agent.Message{
				{Role: "user", Content: "总结我笔记里的重试策略"},
				{Role: "assistant", Content: "有三点。"},
				{Role: "user", Content: "换个话题，为什么天空是蓝色？"},
			},
			want: RetrievalDecision{Reason: RetrievalReasonNotRequired},
		},
		{
			name:     "ordinary conversation",
			messages: []agent.Message{{Role: "user", Content: "你好，帮我解释一下指数退避"}},
			want:     RetrievalDecision{Reason: RetrievalReasonNotRequired},
		},
	}

	decider := RuleRetrievalDecider{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := decider.Decide(test.messages); got != test.want {
				t.Fatalf("decide() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRetrievalReasonsAreAllowListed(t *testing.T) {
	for _, reason := range []RetrievalReason{
		RetrievalReasonExplicitNoteReference,
		RetrievalReasonContextualFollowUp,
		RetrievalReasonNotRequired,
	} {
		if !reason.Valid() {
			t.Fatalf("reason %q is not allow-listed", reason)
		}
	}
	if RetrievalReason("raw prompt or secret").Valid() {
		t.Fatal("arbitrary reason unexpectedly accepted")
	}
}
