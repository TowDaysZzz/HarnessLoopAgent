package einoagent

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
)

func TestBuildToolsFollowsRAGSwitch(t *testing.T) {
	tests := []struct {
		name      string
		rag       config.RAGConfig
		retriever *recordingRetriever
		wantNames []string
	}{
		{name: "disabled", rag: config.RAGConfig{Enabled: false}, wantNames: []string{"echo"}},
		{
			name:      "enabled",
			rag:       config.RAGConfig{Enabled: true, KBIDs: []uint64{2}, TopK: 5, StrategyProfile: "default"},
			retriever: &recordingRetriever{},
			wantNames: []string{"echo", "semantic_search_notes"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, err := buildTools(context.Background(), tt.rag, config.GroundingConfig{}, time.Second, tt.retriever)
			if err != nil {
				t.Fatalf("buildTools() error = %v", err)
			}
			if names := toolNames(t, tools); !equalStrings(names, tt.wantNames) {
				t.Fatalf("tool names = %v, want %v", names, tt.wantNames)
			}
		})
	}
}

func toolNames(t *testing.T, tools []tool.BaseTool) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, info.Name)
	}
	return names
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
