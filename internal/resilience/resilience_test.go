package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryRecoversAndHonorsClassifier(t *testing.T) {
	attempts := 0
	policy := RetryPolicy{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Sleep:  func(context.Context, time.Duration) error { return nil },
		Jitter: func(delay time.Duration) time.Duration { return delay },
	}
	result, err := Do(context.Background(), policy, func(error) RetryDecision { return RetryDecision{Retry: true} }, func(_ int) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("temporary")
		}
		return "ok", nil
	})
	if err != nil || result != "ok" || attempts != 3 {
		t.Fatalf("result=%q attempts=%d err=%v", result, attempts, err)
	}

	attempts = 0
	_, err = Do(context.Background(), policy, func(error) RetryDecision { return RetryDecision{} }, func(_ int) (string, error) {
		attempts++
		return "", errors.New("permanent")
	})
	if err == nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestCircuitBreakerOpensAndUsesSingleHalfOpenProbe(t *testing.T) {
	now := time.Now()
	breaker := NewCircuitBreaker(2, time.Second)
	breaker.now = func() time.Time { return now }
	breaker.Failure()
	breaker.Failure()
	if !errors.Is(breaker.Allow(), ErrCircuitOpen) {
		t.Fatal("breaker should be open")
	}
	now = now.Add(2 * time.Second)
	if err := breaker.Allow(); err != nil {
		t.Fatalf("half-open probe rejected: %v", err)
	}
	if !errors.Is(breaker.Allow(), ErrCircuitOpen) {
		t.Fatal("second half-open probe should be rejected")
	}
	breaker.Success()
	if err := breaker.Allow(); err != nil {
		t.Fatalf("closed breaker rejected: %v", err)
	}
}

func TestBulkheadWaitsForCapacityAndContext(t *testing.T) {
	bulkhead := NewBulkhead(1)
	if err := bulkhead.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := bulkhead.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v", err)
	}
	bulkhead.Release()
}
