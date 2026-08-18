package einoagent

import (
	"context"
	"fmt"

	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
)

func NewModel(ctx context.Context, cfg config.ModelConfig) (model.BaseChatModel, error) {
	if cfg.Provider != "openai-compatible" {
		return nil, fmt.Errorf("unsupported model provider %q", cfg.Provider)
	}
	chatModel, err := modelopenai.NewChatModel(ctx, &modelopenai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Name,
		Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI-compatible chat model: %w", err)
	}
	return chatModel, nil
}
