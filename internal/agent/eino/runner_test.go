package einoagent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type fakeStreamingModel struct{}

func (fakeStreamingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("hello world", nil), nil
}

func (fakeStreamingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("hello ", nil),
		schema.AssistantMessage("world", nil),
	}), nil
}

func TestRunnerConsumesStreamingModelOutput(t *testing.T) {
	runner, err := NewRunner(context.Background(), fakeStreamingModel{}, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	var text strings.Builder
	completed := false
	for event := range runner.Stream(context.Background(), "say hello") {
		switch event.Type {
		case appagent.EventTextDelta:
			text.WriteString(event.Delta)
		case appagent.EventRunCompleted:
			completed = true
		case appagent.EventRunFailed:
			t.Fatalf("stream failed: %v", event.Err)
		}
	}
	if text.String() != "hello world" {
		t.Fatalf("streamed text = %q", text.String())
	}
	if !completed {
		t.Fatal("run did not emit completion event")
	}
}

type historyCapturingModel struct {
	messages []*schema.Message
}

func (m *historyCapturingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (m *historyCapturingModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.messages = messages
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func TestRunnerPassesConversationHistory(t *testing.T) {
	model := &historyCapturingModel{}
	runner, err := NewRunner(context.Background(), model, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	for range runner.StreamMessages(context.Background(), []appagent.Message{
		{Role: "user", Content: "my name is Ada"},
		{Role: "assistant", Content: "noted"},
		{Role: "user", Content: "what is my name?"},
	}) {
	}
	if len(model.messages) < 3 {
		t.Fatalf("model messages = %#v", model.messages)
	}
	got := model.messages[len(model.messages)-3:]
	if got[0].Content != "my name is Ada" || got[1].Content != "noted" || got[2].Content != "what is my name?" {
		t.Fatalf("history = %#v", got)
	}
}

func TestEchoTool(t *testing.T) {
	echo, err := NewEchoTool()
	if err != nil {
		t.Fatalf("NewEchoTool() error = %v", err)
	}
	result, err := echo.InvokableRun(context.Background(), `{"text":"note"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if !strings.Contains(result, "note") {
		t.Fatalf("result = %q", result)
	}
}
