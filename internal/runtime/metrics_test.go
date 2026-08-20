package runtime

import (
	"context"
	"testing"
	"time"
)

func TestMetricsAggregatesRunCallsAndTokens(t *testing.T) {
	m := &Metrics{}
	m.Observe(context.Background(), Event{Stage: StageRunStart})
	m.Observe(context.Background(), Event{Stage: StageModelStart, Fields: map[string]any{"input_tokens": 12}})
	m.Observe(context.Background(), Event{Stage: StageToolStart})
	m.Observe(context.Background(), Event{Stage: StageRunEnd, Duration: 25 * time.Millisecond, Fields: map[string]any{"output_tokens": 8}})
	snapshot := m.Snapshot()
	if snapshot.Runs != 1 || snapshot.ModelCalls != 1 || snapshot.ToolCalls != 1 || snapshot.InputTokens != 12 || snapshot.OutputTokens != 8 || snapshot.TotalLatencyMS != 25 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
