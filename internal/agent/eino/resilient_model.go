package einoagent

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/resilience"
	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

type resilientModel struct {
	next            model.BaseChatModel
	retry           resilience.RetryPolicy
	bulkhead        *resilience.Bulkhead
	breaker         *resilience.CircuitBreaker
	maxOutputTokens int
}

func newResilientModel(next model.BaseChatModel, retry resilience.RetryPolicy, bulkhead *resilience.Bulkhead, breaker *resilience.CircuitBreaker, maxOutputTokens int) model.BaseChatModel {
	return &resilientModel{next: next, retry: retry, bulkhead: bulkhead, breaker: breaker, maxOutputTokens: maxOutputTokens}
}

func (m *resilientModel) Generate(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
	options = m.withOutputLimit(options)
	return resilience.Do(ctx, m.retry, classifyModelError, func(attempt int) (*schema.Message, error) {
		if err := agentruntime.ConsumeModelCall(ctx); err != nil {
			return nil, err
		}
		if err := m.bulkhead.Acquire(ctx); err != nil {
			return nil, err
		}
		if m.breaker != nil {
			if err := m.breaker.Allow(); err != nil {
				m.bulkhead.Release()
				return nil, err
			}
		}
		start := time.Now()
		agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageModelStart, Name: "generate", Attempt: attempt})
		response, err := m.next.Generate(ctx, input, options...)
		m.bulkhead.Release()
		agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageModelEnd, Name: "generate", Attempt: attempt, Duration: time.Since(start), Err: err})
		m.recordResult(err)
		return response, err
	})
}

func (m *resilientModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	options = m.withOutputLimit(options)
	return resilience.Do(ctx, m.retry, classifyModelError, func(attempt int) (*schema.StreamReader[*schema.Message], error) {
		if err := agentruntime.ConsumeModelCall(ctx); err != nil {
			return nil, err
		}
		if err := m.bulkhead.Acquire(ctx); err != nil {
			return nil, err
		}
		if m.breaker != nil {
			if err := m.breaker.Allow(); err != nil {
				m.bulkhead.Release()
				return nil, err
			}
		}
		start := time.Now()
		agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageModelStart, Name: "stream", Attempt: attempt})
		stream, err := m.next.Stream(ctx, input, options...)
		if err != nil {
			m.bulkhead.Release()
			agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageModelEnd, Name: "stream", Attempt: attempt, Duration: time.Since(start), Err: err})
			m.recordResult(err)
			return nil, err
		}
		return m.proxyStream(ctx, stream, attempt, start), nil
	})
}

func (m *resilientModel) withOutputLimit(options []model.Option) []model.Option {
	if m.maxOutputTokens < 1 {
		return options
	}
	return append(options, model.WithMaxTokens(m.maxOutputTokens))
}

func (m *resilientModel) proxyStream(ctx context.Context, source *schema.StreamReader[*schema.Message], attempt int, start time.Time) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		defer source.Close()
		defer m.bulkhead.Release()
		var finalErr error
		defer func() {
			agentruntime.Emit(ctx, agentruntime.Event{Stage: agentruntime.StageModelEnd, Name: "stream", Attempt: attempt, Duration: time.Since(start), Err: finalErr})
			m.recordResult(finalErr)
		}()
		for {
			chunk, err := source.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				finalErr = err
				writer.Send(nil, err)
				return
			}
			if writer.Send(chunk, nil) {
				finalErr = ctx.Err()
				return
			}
		}
	}()
	return reader
}

func (m *resilientModel) recordResult(err error) {
	if m.breaker == nil {
		return
	}
	if err == nil {
		m.breaker.Success()
	} else if classifyModelError(err).Retry {
		m.breaker.Failure()
	} else {
		m.breaker.Success()
	}
}

func classifyModelError(err error) resilience.RetryDecision {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, resilience.ErrCircuitOpen) {
		return resilience.RetryDecision{}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return resilience.RetryDecision{Retry: true}
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "x509") || strings.Contains(message, "certificate") || strings.Contains(message, "401") || strings.Contains(message, "403") || strings.Contains(message, "400") {
		return resilience.RetryDecision{}
	}
	for _, status := range []string{"429", "502", "503", "504", "connection reset", "unexpected eof"} {
		if strings.Contains(message, status) {
			return resilience.RetryDecision{Retry: true}
		}
	}
	return resilience.RetryDecision{}
}
