package einoagent

import (
	"context"
	"errors"
	"testing"
	"time"

	toolutils "github.com/cloudwego/eino/components/tool/utils"

	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

func TestBoundedToolAppliesTimeoutAndBudget(t *testing.T) {
	blocking, err := toolutils.InferTool("blocking", "blocks until canceled", func(ctx context.Context, _ struct{}) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := newBoundedTool(context.Background(), blocking, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel, _ := agentruntime.Start(context.Background(), agentruntime.Budget{MaxToolCalls: 1}, nil)
	defer cancel()
	if _, err := bounded.InvokableRun(ctx, `{}`); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first call error = %v", err)
	}
	if _, err := bounded.InvokableRun(ctx, `{}`); !errors.Is(err, agentruntime.ErrToolBudgetExceeded) {
		t.Fatalf("second call error = %v", err)
	}
}
