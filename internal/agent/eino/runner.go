package einoagent

import (
	"context"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

const instruction = `You are a personal note assistant. Be concise and never claim that a note was saved unless a note tool confirms it.
When the user asks about previous records or notes, you must call semantic_search_notes before answering. Answer only from its retrieved content and include the returned citation file name and chunk ID as sources. If the tool returns no items, a refusal, or an error, say that the notes do not provide enough evidence; do not answer from general knowledge or invent a source.`

type Runner struct {
	runner *adk.Runner
}

func NewRunner(ctx context.Context, chatModel model.BaseChatModel, tools []tool.BaseTool) (*Runner, error) {
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "note_agent",
		Description:   "Stores, recalls, and reflects on personal notes",
		Instruction:   instruction,
		Model:         chatModel,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools}},
		MaxIterations: 8,
	})
	if err != nil {
		return nil, err
	}
	return &Runner{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})}, nil
}

func (r *Runner) Stream(ctx context.Context, prompt string) <-chan appagent.Event {
	out := make(chan appagent.Event)
	go func() {
		defer close(out)
		iterator := r.runner.Query(ctx, prompt)
		for {
			event, ok := iterator.Next()
			if !ok {
				out <- appagent.Event{Type: appagent.EventRunCompleted}
				return
			}
			if event.Err != nil {
				out <- appagent.Event{Type: appagent.EventRunFailed, Err: event.Err}
				return
			}
			if event.Output == nil || event.Output.MessageOutput == nil {
				continue
			}
			if err := forwardMessage(ctx, out, event.Output.MessageOutput); err != nil {
				out <- appagent.Event{Type: appagent.EventRunFailed, Err: err}
				return
			}
		}
	}()
	return out
}

func forwardMessage(ctx context.Context, out chan<- appagent.Event, message *adk.MessageVariant) error {
	eventType := appagent.EventTextDelta
	if message.Role == schema.Tool {
		eventType = appagent.EventToolCompleted
	}
	emit := func(content string) error {
		select {
		case out <- appagent.Event{Type: eventType, Delta: content, ToolName: message.ToolName}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if !message.IsStreaming {
		if message.Message == nil {
			return nil
		}
		return emit(message.Message.Content)
	}
	defer message.MessageStream.Close()
	for {
		chunk, err := message.MessageStream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if chunk != nil {
			if err := emit(chunk.Content); err != nil {
				return err
			}
		}
	}
}
