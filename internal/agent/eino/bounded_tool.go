package einoagent

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

type boundedInvokableTool struct {
	next    tool.InvokableTool
	name    string
	timeout time.Duration
}

func newBoundedTool(ctx context.Context, next tool.InvokableTool, timeout time.Duration) (tool.InvokableTool, error) {
	info, err := next.Info(ctx)
	if err != nil {
		return nil, err
	}
	return &boundedInvokableTool{next: next, name: info.Name, timeout: timeout}, nil
}

func (t *boundedInvokableTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.next.Info(ctx)
}

func (t *boundedInvokableTool) InvokableRun(ctx context.Context, arguments string, options ...tool.Option) (output string, err error) {
	if err := agentruntime.ConsumeToolCall(ctx); err != nil {
		return "", err
	}
	toolCtx := ctx
	cancel := func() {}
	if t.timeout > 0 {
		toolCtx, cancel = context.WithTimeout(ctx, t.timeout)
	}
	defer cancel()
	start := time.Now()
	agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageToolStart, Name: t.name})
	defer func() {
		agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageToolEnd, Name: t.name, Duration: time.Since(start), Err: err})
	}()
	return t.next.InvokableRun(toolCtx, arguments, options...)
}
