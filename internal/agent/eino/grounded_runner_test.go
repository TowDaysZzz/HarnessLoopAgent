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
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/grounding"
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

func TestGroundedRunnerUsesExplicitRetrievalRequirement(t *testing.T) {
	t.Run("required follow-up without note keywords is protected", func(t *testing.T) {
		model := &scriptedGroundedModel{turns: []*schema.Message{schema.AssistantMessage("没有检索的回答", nil)}}
		runner, err := NewRunner(context.Background(), model, nil, RunnerOptions{RunTimeout: time.Second, MaxIterations: 2, MaxModelCalls: 2, MaxToolCalls: 1, RequireRAGForNoteQuery: true})
		if err != nil {
			t.Fatal(err)
		}
		var answer strings.Builder
		terminal := 0
		for event := range runner.StreamConversation(context.Background(), appagent.ConversationRequest{
			Messages: []appagent.Message{
				{Role: "user", Content: "总结我笔记里的重试策略"},
				{Role: "assistant", Content: "有三点"},
				{Role: "user", Content: "第二点为什么？"},
			},
			RequireNoteRetrieval: true,
		}) {
			if event.Type == appagent.EventTextDelta {
				answer.WriteString(event.Delta)
			}
			if event.Type == appagent.EventRunCompleted || event.Type == appagent.EventRunFailed {
				terminal++
			}
		}
		if answer.String() != groundedRefusal || terminal != 1 {
			t.Fatalf("answer = %q, terminal events = %d", answer.String(), terminal)
		}
	})

	t.Run("non-required mode remains model-driven", func(t *testing.T) {
		model := &scriptedGroundedModel{turns: []*schema.Message{schema.AssistantMessage("普通回答", nil)}}
		runner, err := NewRunner(context.Background(), model, nil, RunnerOptions{RunTimeout: time.Second, MaxIterations: 2, MaxModelCalls: 2, MaxToolCalls: 1, RequireRAGForNoteQuery: true})
		if err != nil {
			t.Fatal(err)
		}
		var answer strings.Builder
		for event := range runner.StreamConversation(context.Background(), appagent.ConversationRequest{
			Messages:             []appagent.Message{{Role: "user", Content: "即使文本提到笔记，也按显式 false 正常对话"}},
			RequireNoteRetrieval: false,
		}) {
			if event.Type == appagent.EventTextDelta {
				answer.WriteString(event.Delta)
			}
		}
		if answer.String() != "普通回答" {
			t.Fatalf("answer = %q", answer.String())
		}
	})
}

func TestGroundedRunnerProtectsAutonomousRetrievalAnswers(t *testing.T) {
	retriever := &recordingRetriever{result: &ragclient.RetrieveResponse{RequestID: "rag-autonomous", Items: []ragclient.RetrieveItem{{
		Content: "指数退避", Score: .9, Citation: ragclient.Citation{KBID: 1, DocumentID: 1, ChunkID: "chunk-1", FileName: "retry.md"},
	}}}}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{KBIDs: []uint64{1}, DefaultTopK: 5})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name              string
		maxRepairAttempts int
		turns             []*schema.Message
		wantAnswer        string
	}{
		{
			name:              "invalid citation is refused without repair budget",
			maxRepairAttempts: 0,
			turns: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{{ID: "call-refuse", Type: "function", Function: schema.FunctionCall{Name: "semantic_search_notes", Arguments: `{"query":"重试策略"}`}}}),
				schema.AssistantMessage("来源 invented.md，chunk ID: made-up", nil),
			},
			wantAnswer: groundedRefusal,
		},
		{
			name:              "invalid citation is repaired within budget",
			maxRepairAttempts: 1,
			turns: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{{ID: "call-repair", Type: "function", Function: schema.FunctionCall{Name: "semantic_search_notes", Arguments: `{"query":"重试策略"}`}}}),
				schema.AssistantMessage("来源 invented.md，chunk ID: made-up", nil),
				schema.AssistantMessage("来源 retry.md，chunk ID: chunk-1", nil),
			},
			wantAnswer: "来源 retry.md，chunk ID: chunk-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &scriptedGroundedModel{turns: test.turns}
			runner, err := NewRunner(context.Background(), model, []tool.BaseTool{searchTool}, RunnerOptions{
				RunTimeout: time.Second, MaxIterations: 4, MaxModelCalls: 4, MaxToolCalls: 2,
				MaxRepairAttempts: test.maxRepairAttempts, RequireRAGForNoteQuery: true,
			})
			if err != nil {
				t.Fatal(err)
			}

			var order []appagent.EventType
			var answer strings.Builder
			terminal := 0
			for event := range runner.StreamConversation(context.Background(), appagent.ConversationRequest{
				Messages:             []appagent.Message{{Role: "user", Content: "重试策略怎么设计更好？"}},
				RequireNoteRetrieval: false,
			}) {
				order = append(order, event.Type)
				if event.Type == appagent.EventTextDelta {
					answer.WriteString(event.Delta)
				}
				if event.Type == appagent.EventRunCompleted || event.Type == appagent.EventRunFailed {
					terminal++
				}
			}

			if got := answer.String(); got != test.wantAnswer || strings.Contains(got, "invented.md") || strings.Contains(got, "made-up") {
				t.Fatalf("answer = %q, want %q", got, test.wantAnswer)
			}
			toolIndex, textIndex := indexOf(order, appagent.EventToolCompleted), indexOf(order, appagent.EventTextDelta)
			if toolIndex < 0 || textIndex <= toolIndex || terminal != 1 {
				t.Fatalf("event order = %v, terminal events = %d", order, terminal)
			}
		})
	}
}

func TestGroundedRunnerReretrievesEachFollowUpWithCurrentOwnerScope(t *testing.T) {
	model := &scriptedGroundedModel{turns: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "semantic_search_notes", Arguments: `{"query":"重试策略"}`}}}),
		schema.AssistantMessage("来源 retry.md，chunk ID: chunk-1", nil),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-2", Type: "function", Function: schema.FunctionCall{Name: "semantic_search_notes", Arguments: `{"query":"第二点"}`}}}),
		schema.AssistantMessage("来源 retry.md，chunk ID: chunk-1", nil),
	}}
	retriever := &recordingRetriever{result: &ragclient.RetrieveResponse{RequestID: "rag", Items: []ragclient.RetrieveItem{{
		Content: "指数退避", Score: .9, Citation: ragclient.Citation{KBID: 1, DocumentID: 1, ChunkID: "chunk-1", FileName: "retry.md"},
	}}}}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{KBIDs: []uint64{1}, DefaultTopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(context.Background(), model, []tool.BaseTool{searchTool}, RunnerOptions{RunTimeout: time.Second, MaxIterations: 4, MaxModelCalls: 4, MaxToolCalls: 2, RequireRAGForNoteQuery: true})
	if err != nil {
		t.Fatal(err)
	}
	for index, kbID := range []uint64{11, 22} {
		ctx := ragclient.WithKnowledgeBaseIDs(context.Background(), []uint64{kbID})
		for range runner.StreamConversation(ctx, appagent.ConversationRequest{
			Messages:             []appagent.Message{{Role: "user", Content: []string{"重试策略", "第二点为什么？"}[index]}},
			RequireNoteRetrieval: true,
		}) {
		}
	}
	if len(retriever.requests) != 2 || retriever.requests[0].KBIDs[0] != 11 || retriever.requests[1].KBIDs[0] != 22 {
		t.Fatalf("retrieval requests = %#v", retriever.requests)
	}
}

func TestValidateOrRepairIsFailClosedAndBounded(t *testing.T) {
	observation := grounding.Observation{Usable: true, Items: []ragclient.RetrieveItem{{
		Content: "指数退避", Score: .9, Citation: ragclient.Citation{KBID: 1, DocumentID: 1, ChunkID: "chunk-1", FileName: "retry.md"},
	}}}

	t.Run("unusable evidence", func(t *testing.T) {
		runner := &Runner{options: RunnerOptions{MaxRepairAttempts: 1}, chatModel: &scriptedGroundedModel{}}
		if _, err := runner.validateOrRepair(context.Background(), "问题", "草稿", true, grounding.Observation{}); err == nil {
			t.Fatal("expected unusable evidence error")
		}
	})

	t.Run("invalid citation without repair budget", func(t *testing.T) {
		runner := &Runner{options: RunnerOptions{MaxRepairAttempts: 0}, chatModel: &scriptedGroundedModel{}}
		if _, err := runner.validateOrRepair(context.Background(), "问题", "来源 invented.md，chunk ID: made-up", true, observation); err == nil {
			t.Fatal("expected invalid citation error")
		}
	})

	t.Run("one bounded repair", func(t *testing.T) {
		model := &scriptedGroundedModel{turns: []*schema.Message{schema.AssistantMessage("来源 retry.md，chunk ID: chunk-1", nil)}}
		runner := &Runner{options: RunnerOptions{MaxRepairAttempts: 1}, chatModel: model}
		answer, err := runner.validateOrRepair(context.Background(), "问题", "来源 invented.md，chunk ID: made-up", true, observation)
		if err != nil || !strings.Contains(answer, "retry.md") {
			t.Fatalf("repair = %q, %v", answer, err)
		}
		model.mu.Lock()
		remaining := len(model.turns)
		model.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("repair model was not called exactly once; remaining turns = %d", remaining)
		}
	})
}

func indexOf(events []appagent.EventType, target appagent.EventType) int {
	for index, event := range events {
		if event == target {
			return index
		}
	}
	return -1
}
