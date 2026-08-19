package einoagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type scriptedGroundedModel struct {
	mu    sync.Mutex
	turns []*schema.Message
}

func (m *scriptedGroundedModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) == 0 {
		return schema.AssistantMessage("没有依据的回答", nil)
	}
	result := m.turns[0]
	m.turns = m.turns[1:]
	return result
}

func (m *scriptedGroundedModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *scriptedGroundedModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func TestGroundedRunnerDoesNotEmitDraftBeforeValidation(t *testing.T) {
	model := &scriptedGroundedModel{turns: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "semantic_search_notes", Arguments: `{"query":"垃圾回收"}`}}}),
		schema.AssistantMessage("记录说明了三色标记法。来源 go_interview.md，chunk ID: doc-3-child-124", nil),
	}}
	retriever := &recordingRetriever{result: &ragclient.RetrieveResponse{RequestID: "rag-1", Items: []ragclient.RetrieveItem{{
		Content: "三色标记法", Score: 0.9,
		Citation: ragclient.Citation{KBID: 2, DocumentID: 3, ChunkID: "doc-3-child-124", FileName: "go_interview.md"},
	}}}}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{KBIDs: []uint64{2}, DefaultTopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(context.Background(), model, []tool.BaseTool{searchTool}, RunnerOptions{
		RunTimeout: time.Second, MaxIterations: 4, MaxModelCalls: 3, MaxToolCalls: 2, RequireRAGForNoteQuery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []appagent.EventType
	var answer strings.Builder
	for event := range runner.Stream(context.Background(), "查询我之前关于垃圾回收的记录") {
		order = append(order, event.Type)
		if event.Type == appagent.EventTextDelta {
			answer.WriteString(event.Delta)
		}
		if event.Type == appagent.EventRunFailed {
			t.Fatalf("run failed: %v", event.Err)
		}
	}
	toolIndex, textIndex := indexOf(order, appagent.EventToolCompleted), indexOf(order, appagent.EventTextDelta)
	if toolIndex < 0 || textIndex <= toolIndex {
		t.Fatalf("event order = %v", order)
	}
	if !strings.Contains(answer.String(), "go_interview.md") || !strings.Contains(answer.String(), "doc-3-child-124") {
		t.Fatalf("answer = %q", answer.String())
	}
}

func TestGroundedRunnerRefusesWhenModelSkipsRetrieval(t *testing.T) {
	model := &scriptedGroundedModel{turns: []*schema.Message{schema.AssistantMessage("垃圾回收是...", nil)}}
	runner, err := NewRunner(context.Background(), model, nil, RunnerOptions{RunTimeout: time.Second, MaxIterations: 2, MaxModelCalls: 2, MaxToolCalls: 1, RequireRAGForNoteQuery: true})
	if err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	for event := range runner.Stream(context.Background(), "查询我之前的垃圾回收记录") {
		if event.Type == appagent.EventTextDelta {
			answer.WriteString(event.Delta)
		}
	}
	if answer.String() != groundedRefusal {
		t.Fatalf("answer = %q", answer.String())
	}
}

func indexOf(events []appagent.EventType, target appagent.EventType) int {
	for index, event := range events {
		if event == target {
			return index
		}
	}
	return -1
}
