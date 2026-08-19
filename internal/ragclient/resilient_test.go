package ragclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/resilience"
)

type retrieverFunc func(context.Context, RetrieveRequest) (*RetrieveResponse, error)

func (f retrieverFunc) Retrieve(ctx context.Context, request RetrieveRequest) (*RetrieveResponse, error) {
	return f(ctx, request)
}

func TestResilientRetrieverRetriesTemporaryErrorsOnly(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		wantAttempts int
	}{
		{name: "temporary", err: &APIError{HTTPStatus: 503}, wantAttempts: 3},
		{name: "permanent", err: &APIError{HTTPStatus: 403}, wantAttempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			next := retrieverFunc(func(context.Context, RetrieveRequest) (*RetrieveResponse, error) {
				attempts++
				if attempts < test.wantAttempts || test.wantAttempts == 1 {
					return nil, test.err
				}
				return &RetrieveResponse{RequestID: "ok"}, nil
			})
			retriever := NewResilientRetriever(next, ResilientConfig{
				Retry:    resilience.RetryPolicy{MaxAttempts: 3, Sleep: func(context.Context, time.Duration) error { return nil }},
				Bulkhead: resilience.NewBulkhead(1), Breaker: resilience.NewCircuitBreaker(10, time.Second),
			})
			result, err := retriever.Retrieve(context.Background(), RetrieveRequest{})
			if test.wantAttempts == 1 {
				if err == nil || result != nil {
					t.Fatalf("result=%#v err=%v", result, err)
				}
			} else if err != nil || result.RequestID != "ok" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if attempts != test.wantAttempts {
				t.Fatalf("attempts=%d want=%d", attempts, test.wantAttempts)
			}
		})
	}
}

func TestClassifyRAGErrorDoesNotRetryContext(t *testing.T) {
	if classifyRAGError(context.DeadlineExceeded).Retry || classifyRAGError(errors.New("bad request")).Retry {
		t.Fatal("non-retryable error was classified as retryable")
	}
}
