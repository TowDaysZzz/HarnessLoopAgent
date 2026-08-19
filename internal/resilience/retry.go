package resilience

import (
	"context"
	"math/rand/v2"
	"time"
)

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Sleep       func(context.Context, time.Duration) error
	Jitter      func(time.Duration) time.Duration
}

type RetryDecision struct {
	RetryAfter time.Duration
	Retry      bool
}

type Classifier func(error) RetryDecision

func Do[T any](ctx context.Context, policy RetryPolicy, classify Classifier, operation func(attempt int) (T, error)) (T, error) {
	var zero T
	attempts := policy.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := operation(attempt)
		if err == nil {
			return result, nil
		}
		decision := classify(err)
		if !decision.Retry || attempt == attempts {
			return zero, err
		}
		delay := decision.RetryAfter
		if delay <= 0 {
			delay = backoff(policy, attempt)
		}
		if err := sleep(policy, ctx, delay); err != nil {
			return zero, err
		}
	}
	return zero, ctx.Err()
}

func backoff(policy RetryPolicy, attempt int) time.Duration {
	delay := policy.BaseDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	for i := 1; i < attempt; i++ {
		if policy.MaxDelay > 0 && delay >= policy.MaxDelay/2 {
			delay = policy.MaxDelay
			break
		}
		delay *= 2
	}
	if policy.MaxDelay > 0 && delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	if policy.Jitter != nil {
		return policy.Jitter(delay)
	}
	if delay <= 1 {
		return delay
	}
	return time.Duration(rand.Int64N(int64(delay) + 1))
}

func sleep(policy RetryPolicy, ctx context.Context, delay time.Duration) error {
	if policy.Sleep != nil {
		return policy.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
