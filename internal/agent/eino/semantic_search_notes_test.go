package einoagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/grounding"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type recordingRetriever struct {
	request  ragclient.RetrieveRequest
	requests []ragclient.RetrieveRequest
	result   *ragclient.RetrieveResponse
	err      error
}

func (r *recordingRetriever) Retrieve(_ context.Context, request ragclient.RetrieveRequest) (*ragclient.RetrieveResponse, error) {
	r.request = request
	r.requests = append(r.requests, request)
	return r.result, r.err
}

func TestSemanticSearchNotesInjectsServerOptionsAndPreservesSources(t *testing.T) {
	retriever := &recordingRetriever{result: &ragclient.RetrieveResponse{
		RequestID: "rag-request-1",
		Items: []ragclient.RetrieveItem{{
			Content: "Go uses concurrent garbage collection.",
			Score:   0.93,
			Citation: ragclient.Citation{
				KBID: 2, DocumentID: 7, ChunkID: "chunk-gc", FileName: "go_interview.md", ChunkIndex: 3,
			},
		}},
	}}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{
		KBIDs: []uint64{2}, DefaultTopK: 5, StrategyProfile: "default",
	})
	if err != nil {
		t.Fatalf("NewSemanticSearchNotesTool() error = %v", err)
	}

	output, err := searchTool.InvokableRun(context.Background(), `{"query":"  垃圾回收  "}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if retriever.request.Query != "垃圾回收" || retriever.request.TopK != 5 || retriever.request.StrategyProfile != "default" {
		t.Fatalf("retrieve request = %#v", retriever.request)
	}
	if len(retriever.request.KBIDs) != 1 || retriever.request.KBIDs[0] != 2 {
		t.Fatalf("server KB IDs were not injected: %#v", retriever.request.KBIDs)
	}
	var decoded grounding.Observation
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode tool output: %v; output=%s", err, output)
	}
	if !decoded.Usable || decoded.RequestID != "rag-request-1" || decoded.Items[0].Citation.FileName != "go_interview.md" || decoded.Items[0].Citation.ChunkID != "chunk-gc" {
		t.Fatalf("tool output lost retrieval source: %#v", decoded)
	}
}

func TestSemanticSearchNotesSchemaDoesNotExposeServerConfiguration(t *testing.T) {
	searchTool, err := NewSemanticSearchNotesTool(&recordingRetriever{}, SemanticSearchNotesOptions{KBIDs: []uint64{2}, DefaultTopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	info, err := searchTool.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	jsonSchema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	schemaJSON, err := json.Marshal(jsonSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"kb_ids", "api_key", "base_url", "strategy_profile"} {
		if strings.Contains(string(schemaJSON), forbidden) {
			t.Fatalf("tool schema exposes %q: %s", forbidden, schemaJSON)
		}
	}
	for _, required := range []string{"query", "top_k"} {
		if !strings.Contains(string(schemaJSON), required) {
			t.Fatalf("tool schema is missing %q: %s", required, schemaJSON)
		}
	}
}

func TestSemanticSearchNotesUsesRequestKnowledgeBaseBinding(t *testing.T) {
	retriever := &recordingRetriever{result: &ragclient.RetrieveResponse{}}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{KBIDs: []uint64{2}, DefaultTopK: 5})
	if err != nil {
		t.Fatalf("NewSemanticSearchNotesTool() error = %v", err)
	}
	ctx := ragclient.WithKnowledgeBaseIDs(context.Background(), []uint64{9})
	if _, err := searchTool.InvokableRun(ctx, `{"query":"notes"}`); err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if len(retriever.request.KBIDs) != 1 || retriever.request.KBIDs[0] != 9 {
		t.Fatalf("KBIDs = %#v, want user binding [9]", retriever.request.KBIDs)
	}
}

func TestSemanticSearchNotesValidatesModelArguments(t *testing.T) {
	searchTool, err := NewSemanticSearchNotesTool(&recordingRetriever{}, SemanticSearchNotesOptions{KBIDs: []uint64{2}, DefaultTopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"empty query": `{"query":" "}`,
		"large top k": `{"query":"gc","top_k":21}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := searchTool.InvokableRun(context.Background(), input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
