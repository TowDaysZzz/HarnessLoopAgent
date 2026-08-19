package ragclient

import (
	"context"
	"errors"
	"net"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/resilience"
)

type ResilientConfig struct {
	Retry    resilience.RetryPolicy
	Bulkhead *resilience.Bulkhead
	Breaker  *resilience.CircuitBreaker
}

type ResilientRetriever struct {
	next     Retriever
	retry    resilience.RetryPolicy
	bulkhead *resilience.Bulkhead
	breaker  *resilience.CircuitBreaker
}

func NewResilientRetriever(next Retriever, config ResilientConfig) *ResilientRetriever {
	return &ResilientRetriever{next: next, retry: config.Retry, bulkhead: config.Bulkhead, breaker: config.Breaker}
}

func (r *ResilientRetriever) Retrieve(ctx context.Context, request RetrieveRequest) (*RetrieveResponse, error) {
	return resilience.Do(ctx, r.retry, classifyRAGError, func(_ int) (*RetrieveResponse, error) {
		if r.bulkhead != nil {
			if err := r.bulkhead.Acquire(ctx); err != nil {
				return nil, err
			}
			defer r.bulkhead.Release()
		}
		if r.breaker != nil {
			if err := r.breaker.Allow(); err != nil {
				return nil, err
			}
		}
		response, err := r.next.Retrieve(ctx, request)
		if err == nil {
			if r.breaker != nil {
				r.breaker.Success()
			}
			return response, nil
		}
		if r.breaker != nil {
			if classifyRAGError(err).Retry {
				r.breaker.Failure()
			} else {
				r.breaker.Success()
			}
		}
		return nil, err
	})
}

func classifyRAGError(err error) resilience.RetryDecision {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, resilience.ErrCircuitOpen) {
		return resilience.RetryDecision{}
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return resilience.RetryDecision{Retry: apiErr.Temporary(), RetryAfter: apiErr.RetryAfter}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return resilience.RetryDecision{Retry: netErr.Timeout() || netErr.Temporary()}
	}
	return resilience.RetryDecision{}
}
