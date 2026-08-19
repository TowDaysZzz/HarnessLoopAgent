package resilience

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker is open")

type CircuitBreaker struct {
	mu               sync.Mutex
	failureThreshold int
	openTimeout      time.Duration
	failures         int
	openedAt         time.Time
	halfOpenProbe    bool
	now              func() time.Time
}

func NewCircuitBreaker(failureThreshold int, openTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		openTimeout:      openTimeout,
		now:              time.Now,
	}
}

func (b *CircuitBreaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return nil
	}
	if b.now().Sub(b.openedAt) < b.openTimeout || b.halfOpenProbe {
		return ErrCircuitOpen
	}
	b.halfOpenProbe = true
	return nil
}

func (b *CircuitBreaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openedAt = time.Time{}
	b.halfOpenProbe = false
}

func (b *CircuitBreaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halfOpenProbe = false
	b.failures++
	if b.failureThreshold > 0 && b.failures >= b.failureThreshold {
		b.openedAt = b.now()
	}
}
