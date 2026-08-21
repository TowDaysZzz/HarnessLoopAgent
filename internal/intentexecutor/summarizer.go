package intentexecutor

import (
	"context"
	"errors"
	"strings"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type RunnerSummarizer struct {
	Runner agent.ConversationRunner
}

func (s RunnerSummarizer) Summarize(ctx context.Context, messages []agent.Message) (string, string, error) {
	if s.Runner == nil || len(messages) < 2 {
		return "", "", errors.New("conversation history is insufficient for note summary")
	}
	input := append([]agent.Message(nil), messages...)
	input = append(input, agent.Message{Role: "user", Content: "请仅把本次对话整理成一份可独立阅读的摘要。第一行输出简短标题，后续输出正文；不要声称已经保存。"})
	var content strings.Builder
	completed := false
	for event := range s.Runner.StreamMessages(ctx, input) {
		switch event.Type {
		case agent.EventTextDelta:
			content.WriteString(event.Delta)
		case agent.EventRunFailed:
			return "", "", event.Err
		case agent.EventRunCompleted:
			completed = true
		}
	}
	value := strings.TrimSpace(content.String())
	if !completed || value == "" {
		return "", "", errors.New("note summary did not complete")
	}
	parts := strings.SplitN(value, "\n", 2)
	title := titleFromContent(strings.TrimSpace(strings.TrimLeft(parts[0], "# ")))
	body := value
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		body = strings.TrimSpace(parts[1])
	}
	return title, body, nil
}
