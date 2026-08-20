package runtime

import (
	"context"
	"sync"
	"time"
)

type Metrics struct {
	mu           sync.RWMutex
	runs         uint64
	modelCalls   uint64
	toolCalls    uint64
	failedRuns   uint64
	totalLatency time.Duration
	tokenInput   uint64
	tokenOutput  uint64
}

func (m *Metrics) Observe(_ context.Context, event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch event.Stage {
	case StageRunStart:
		m.runs++
	case StageModelStart:
		m.modelCalls++
	case StageToolStart:
		m.toolCalls++
	case StageRunEnd:
		m.totalLatency += event.Duration
		if event.Err != nil {
			m.failedRuns++
		}
	}
	if event.Fields != nil {
		if value, ok := event.Fields["input_tokens"].(int); ok && value > 0 {
			m.tokenInput += uint64(value)
		}
		if value, ok := event.Fields["output_tokens"].(int); ok && value > 0 {
			m.tokenOutput += uint64(value)
		}
	}
}

type SnapshotMetrics struct {
	Runs, ModelCalls, ToolCalls, FailedRuns uint64
	TotalLatencyMS                          int64
	InputTokens, OutputTokens               uint64
}

func (m *Metrics) Snapshot() SnapshotMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return SnapshotMetrics{Runs: m.runs, ModelCalls: m.modelCalls, ToolCalls: m.toolCalls, FailedRuns: m.failedRuns, TotalLatencyMS: m.totalLatency.Milliseconds(), InputTokens: m.tokenInput, OutputTokens: m.tokenOutput}
}

type MultiObserver []Observer

func (o MultiObserver) Observe(ctx context.Context, event Event) {
	for _, observer := range o {
		if observer != nil {
			observer.Observe(ctx, event)
		}
	}
}
