package einoagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

func NewConfiguredRunner(ctx context.Context, cfg config.Config) (*Runner, error) {
	chatModel, err := NewModel(ctx, cfg.Model)
	if err != nil {
		return nil, err
	}

	var retriever ragclient.Retriever
	if cfg.RAG.Enabled {
		retriever, err = ragclient.NewClient(ragclient.ClientConfig{
			BaseURL: cfg.RAG.BaseURL,
			APIKey:  cfg.RAG.APIKey,
			Timeout: cfg.RAG.Timeout,
		})
		if err != nil {
			return nil, fmt.Errorf("create RAG client: %w", err)
		}
	}
	tools, err := buildTools(cfg.RAG, retriever)
	if err != nil {
		return nil, err
	}
	return NewRunner(ctx, chatModel, tools)
}

func buildTools(cfg config.RAGConfig, retriever ragclient.Retriever) ([]tool.BaseTool, error) {
	echoTool, err := NewEchoTool()
	if err != nil {
		return nil, fmt.Errorf("create echo tool: %w", err)
	}
	tools := []tool.BaseTool{echoTool}
	if !cfg.Enabled {
		return tools, nil
	}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{
		KBIDs:           cfg.KBIDs,
		DefaultTopK:     cfg.TopK,
		StrategyProfile: cfg.StrategyProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("create semantic search notes tool: %w", err)
	}
	return append(tools, searchTool), nil
}
