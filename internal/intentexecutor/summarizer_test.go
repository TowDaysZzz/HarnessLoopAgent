package intentexecutor

import (
	"context"
	"strings"
	"testing"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type summarizerRunnerFake struct{ messages []agent.Message }

func (f *summarizerRunnerFake) StreamConversation(_ context.Context, request agent.ConversationRequest) <-chan agent.Event {
	messages := request.Messages
	f.messages = append([]agent.Message(nil), messages...)
	out := make(chan agent.Event, 2)
	out <- agent.Event{Type: agent.EventTextDelta, Delta: "Go GC\nGo GC 使用并发标记。"}
	out <- agent.Event{Type: agent.EventRunCompleted}
	close(out)
	return out
}

func TestRunnerSummarizerDoesNotTriggerNoteRetrievalPrompt(t *testing.T) {
	runner := &summarizerRunnerFake{}
	title, content, err := (RunnerSummarizer{Runner: runner}).Summarize(context.Background(), []agent.Message{
		{Role: "user", Content: "讨论 GC"}, {Role: "assistant", Content: "并发标记"},
	})
	if err != nil || title != "Go GC" || content != "Go GC 使用并发标记。" {
		t.Fatalf("Summarize() = %q, %q, %v", title, content, err)
	}
	last := runner.messages[len(runner.messages)-1].Content
	if strings.Contains(last, "笔记") || strings.Contains(last, "之前") || strings.Contains(last, "历史记录") {
		t.Fatalf("summary instruction may trigger protected retrieval: %q", last)
	}
}
