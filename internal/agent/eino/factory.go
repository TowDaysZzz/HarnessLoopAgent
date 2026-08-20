package einoagent

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/grounding"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/resilience"
	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

func NewConfiguredRunner(ctx context.Context, cfg config.Config) (*Runner, error) {
	chatModel, err := NewModel(ctx, cfg.Model)
	if err != nil {
		return nil, err
	}
	retryPolicy := resilience.RetryPolicy{MaxAttempts: cfg.Resilience.ModelMaxAttempts, BaseDelay: cfg.Resilience.RetryBaseDelay, MaxDelay: cfg.Resilience.RetryMaxDelay}
	chatModel = newResilientModel(chatModel, retryPolicy,
		resilience.NewBulkhead(cfg.Resilience.ModelMaxConcurrency),
		resilience.NewCircuitBreaker(cfg.Resilience.CircuitFailureThreshold, cfg.Resilience.CircuitOpenTimeout),
		cfg.Agent.MaxOutputTokens,
	)

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
		retriever = ragclient.NewResilientRetriever(retriever, ragclient.ResilientConfig{
			Retry:    resilience.RetryPolicy{MaxAttempts: cfg.Resilience.RAGMaxAttempts, BaseDelay: cfg.Resilience.RetryBaseDelay, MaxDelay: cfg.Resilience.RetryMaxDelay},
			Bulkhead: resilience.NewBulkhead(cfg.Resilience.RAGMaxConcurrency),
			Breaker:  resilience.NewCircuitBreaker(cfg.Resilience.CircuitFailureThreshold, cfg.Resilience.CircuitOpenTimeout),
		})
	}
	tools, err := buildTools(ctx, cfg.RAG, cfg.Grounding, cfg.Agent.ToolTimeout, retriever)
	if err != nil {
		return nil, err
	}
	metrics := &agentruntime.Metrics{}
	return NewRunner(ctx, chatModel, tools, RunnerOptions{
		RunTimeout: cfg.Agent.RunTimeout, MaxIterations: cfg.Agent.MaxIterations,
		MaxModelCalls: cfg.Agent.MaxModelCalls, MaxToolCalls: cfg.Agent.MaxToolCalls,
		MaxRepairAttempts:      cfg.Agent.MaxRepairAttempts,
		RequireRAGForNoteQuery: cfg.Grounding.RequireRAGForNoteQuery,
		Observer:               agentruntime.MultiObserver{agentruntime.LogObserver{}, metrics},
		Metrics:                metrics,
	})
}

func buildTools(ctx context.Context, cfg config.RAGConfig, groundingConfig config.GroundingConfig, toolTimeout time.Duration, retriever ragclient.Retriever) ([]tool.BaseTool, error) {
	echoTool, err := NewEchoTool()
	if err != nil {
		return nil, fmt.Errorf("create echo tool: %w", err)
	}
	boundedEcho, err := newBoundedTool(ctx, echoTool, toolTimeout)
	if err != nil {
		return nil, fmt.Errorf("bound echo tool: %w", err)
	}
	tools := []tool.BaseTool{boundedEcho}
	if !cfg.Enabled {
		return tools, nil
	}
	searchTool, err := NewSemanticSearchNotesTool(retriever, SemanticSearchNotesOptions{
		KBIDs:           cfg.KBIDs,
		DefaultTopK:     cfg.TopK,
		StrategyProfile: cfg.StrategyProfile,
		Policy: grounding.Policy{
			RequireEvidenceGate:  groundingConfig.RequireEvidenceGate,
			RequireCitationCheck: groundingConfig.RequireCitationCheck,
			MinResults:           groundingConfig.MinResults, MinTopScore: groundingConfig.MinTopScore,
			MinItemScore: groundingConfig.MinItemScore, RequireCompleteCitation: groundingConfig.RequireCompleteCitation,
			MaxContextChars: groundingConfig.MaxContextChars, RejectPromptInjection: groundingConfig.RejectPromptInjection,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create semantic search notes tool: %w", err)
	}
	boundedSearch, err := newBoundedTool(ctx, searchTool, toolTimeout)
	if err != nil {
		return nil, fmt.Errorf("bound semantic search notes tool: %w", err)
	}
	return append(tools, boundedSearch), nil
}
