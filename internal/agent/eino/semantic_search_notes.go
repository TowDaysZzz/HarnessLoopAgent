package einoagent

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/grounding"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

type SemanticSearchNotesInput struct {
	Query string `json:"query" jsonschema:"description=semantic query for searching the user's previous notes"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"description=maximum number of results from 1 to 20; omit to use the server default"`
}

type SemanticSearchNotesOptions struct {
	KBIDs           []uint64
	DefaultTopK     int
	StrategyProfile string
	Policy          grounding.Policy
}

func NewSemanticSearchNotesTool(retriever ragclient.Retriever, options SemanticSearchNotesOptions) (tool.InvokableTool, error) {
	if retriever == nil {
		return nil, errors.New("RAG retriever is required")
	}
	if len(options.KBIDs) == 0 {
		return nil, errors.New("at least one RAG knowledge base ID is required")
	}
	if options.DefaultTopK < 1 || options.DefaultTopK > 20 {
		return nil, errors.New("default RAG top_k must be between 1 and 20")
	}
	kbIDs := append([]uint64(nil), options.KBIDs...)
	strategyProfile := strings.TrimSpace(options.StrategyProfile)

	return toolutils.InferTool(
		"semantic_search_notes",
		"Search the user's previous notes. Use this before answering questions about prior records; cite file_name and chunk_id from the result.",
		func(ctx context.Context, input SemanticSearchNotesInput) (grounding.Observation, error) {
			query := strings.TrimSpace(input.Query)
			if query == "" {
				return grounding.Observation{}, errors.New("query is required")
			}
			topK := input.TopK
			if topK == 0 {
				topK = options.DefaultTopK
			}
			if topK < 1 || topK > 20 {
				return grounding.Observation{}, errors.New("top_k must be between 1 and 20")
			}
			requestKBIDs := ragclient.KnowledgeBaseIDsFromContext(ctx)
			if len(requestKBIDs) == 0 {
				requestKBIDs = kbIDs
			}
			response, err := retriever.Retrieve(ctx, ragclient.RetrieveRequest{
				Query:           query,
				KBIDs:           append([]uint64(nil), requestKBIDs...),
				TopK:            topK,
				StrategyProfile: strategyProfile,
			})
			if err != nil {
				return grounding.Observation{}, err
			}
			observation := options.Policy.Evaluate(response)
			agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageEvidence, Name: "semantic_search_notes", Fields: map[string]any{"usable": observation.Usable, "reason": observation.Reason, "items": len(observation.Items)}})
			return observation, nil
		},
	)
}
