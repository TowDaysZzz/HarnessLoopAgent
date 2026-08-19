package einoagent

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/resilience"
	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

type flakyChatModel struct {
	mu          sync.Mutex
	streamCalls int
	failOpen    int
	midstream   bool
}

func (m *flakyChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (m *flakyChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.streamCalls++
	call := m.streamCalls
	m.mu.Unlock()
	if call <= m.failOpen {
		return nil, errors.New("503 service unavailable")
	}
	if !m.midstream {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(schema.AssistantMessage("partial", nil), nil)
		writer.Send(nil, errors.New("unexpected EOF"))
	}()
	return reader, nil
}

func TestResilientModelRetriesOnlyBeforeStreamStarts(t *testing.T) {
	base := &flakyChatModel{failOpen: 1}
	wrapped := newResilientModel(base, resilience.RetryPolicy{
		MaxAttempts: 2, Sleep: func(context.Context, time.Duration) error { return nil },
	}, resilience.NewBulkhead(1), resilience.NewCircuitBreaker(5, time.Second), 100)
	ctx, cancel, _ := agentruntime.Start(context.Background(), agentruntime.Budget{MaxModelCalls: 3}, nil)
	defer cancel()
	stream, err := wrapped.Stream(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if base.streamCalls != 2 {
		t.Fatalf("stream calls = %d", base.streamCalls)
	}

	midstream := &flakyChatModel{midstream: true}
	wrapped = newResilientModel(midstream, resilience.RetryPolicy{MaxAttempts: 3}, resilience.NewBulkhead(1), resilience.NewCircuitBreaker(5, time.Second), 100)
	stream, err = wrapped.Stream(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected midstream error, got %v", err)
	}
	if midstream.streamCalls != 1 {
		t.Fatalf("midstream was retried: calls=%d", midstream.streamCalls)
	}
}
