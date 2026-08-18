package einoagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

type EchoInput struct {
	Text string `json:"text" jsonschema:"description=text to echo back"`
}

type EchoOutput struct {
	Text string `json:"text"`
}

func NewEchoTool() (tool.InvokableTool, error) {
	return toolutils.InferTool("echo", "Echo text for deterministic agent integration checks", func(_ context.Context, input EchoInput) (EchoOutput, error) {
		if input.Text == "" {
			return EchoOutput{}, fmt.Errorf("text is required")
		}
		return EchoOutput{Text: input.Text}, nil
	})
}
