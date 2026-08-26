package einoagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/grounding"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type fakeStreamingModel struct{}

func (fakeStreamingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("hello world", nil), nil
}

func TestSummarizeSemanticSearchResultForDisplay(t *testing.T) {
	items := make([]ragclient.RetrieveItem, 0, 6)
	for index := 0; index < 6; index++ {
		items = append(items, ragclient.RetrieveItem{
			Content: strings.Repeat("笔", 321), Score: 0.9,
			Citation: ragclient.Citation{KBID: 6, DocumentID: 12, ChunkID: "chunk-1", FileName: "note.md", ChunkIndex: index},
			Source:   ragclient.Source{Collection: "must-not-leak"},
		})
	}
	encoded := summarizeToolResult("semantic_search_notes", grounding.Observation{Usable: true, RequestID: "req-1", Items: items}, "raw")
	var result struct {
		Usable    bool `json:"usable"`
		ItemCount int  `json:"item_count"`
		Items     []struct {
			Content  string             `json:"content"`
			Score    float64            `json:"score"`
			Citation ragclient.Citation `json:"citation"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatalf("summary is invalid JSON: %v", err)
	}
	if !result.Usable || result.ItemCount != 6 || len(result.Items) != 5 {
		t.Fatalf("unexpected summary: %#v", result)
	}
	if got := len([]rune(result.Items[0].Content)); got != 323 {
		t.Fatalf("truncated content rune count = %d, want 323", got)
	}
	if result.Items[0].Citation.FileName != "note.md" || result.Items[0].Citation.ChunkID != "chunk-1" || result.Items[0].Score != 0.9 {
		t.Fatalf("display fields missing: %#v", result.Items[0])
	}
	if strings.Contains(encoded, "must-not-leak") || strings.Contains(encoded, "collection") {
		t.Fatalf("internal source metadata leaked: %s", encoded)
	}
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

func TestEmitTerminalWaitsForSlowConsumer(t *testing.T) {
	out := make(chan appagent.Event)
	done := make(chan struct{})
	go func() {
		emitTerminal(out, appagent.Event{Type: appagent.EventRunCompleted})
		close(done)
	}()

	// This is deliberately longer than the former 100ms best-effort timeout.
	time.Sleep(150 * time.Millisecond)
	select {
	case event := <-out:
		if event.Type != appagent.EventRunCompleted {
			t.Fatalf("terminal event = %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event was not delivered")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal sender did not return")
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
		{Role: "system", Content: "return strict JSON"},
		{Role: "user", Content: "my name is Ada"},
		{Role: "assistant", Content: "noted"},
		{Role: "user", Content: "what is my name?"},
	}) {
	}
	if len(model.messages) < 4 {
		t.Fatalf("model messages = %#v", model.messages)
	}
	got := model.messages[len(model.messages)-4:]
	if got[0].Role != schema.System || got[0].Content != "return strict JSON" || got[1].Content != "my name is Ada" || got[2].Content != "noted" || got[3].Content != "what is my name?" {
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
