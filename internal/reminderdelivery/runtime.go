package reminderdelivery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
)

type Observer interface {
	Observe(component, event, status, errorCode string, attempt int, latency time.Duration)
}

type DispatcherConfig struct {
	BatchSize, MaxBatches   int
	LeaseDuration, Interval time.Duration
	Now                     func() time.Time
	NewToken                func() string
	Observer                Observer
}

type Dispatcher struct {
	repository reminder.Repository
	config     DispatcherConfig
}

func NewDispatcher(repository reminder.Repository, config DispatcherConfig) (*Dispatcher, error) {
	if repository == nil || config.BatchSize < 1 || config.BatchSize > reminder.MaxPageSize || config.MaxBatches < 1 || config.LeaseDuration < time.Second || config.Interval <= 0 || config.NewToken == nil {
		return nil, reminder.ErrInvalidInput
	}
	return &Dispatcher{repository: repository, config: config}, nil
}

func (d *Dispatcher) Tick(ctx context.Context) (int, error) {
	if d == nil {
		return 0, reminder.ErrInvalidInput
	}
	total := 0
	for batch := 0; batch < d.config.MaxBatches; batch++ {
		now := d.now()
		token := strings.TrimSpace(d.config.NewToken())
		if token == "" {
			return total, reminder.ErrInvalidInput
		}
		values, err := d.repository.ClaimDue(ctx, reminder.DueClaimRequest{Limit: d.config.BatchSize, Now: now, LeaseDuration: d.config.LeaseDuration, Token: token})
		if err != nil {
			d.observe("claim", "failed", "repository", 0, 0)
			return total, err
		}
		for _, value := range values {
			start := time.Now()
			occurrence := fmt.Sprintf("%s:%d", value.ID, value.RowVersion)
			_, _, err := d.repository.CommitOccurrence(ctx, reminder.CommitOccurrenceInput{ReminderID: value.ID, ExpectedRowVersion: value.RowVersion, ClaimToken: token, OccurrenceID: occurrence, OccurredAt: now})
			if err != nil {
				d.observe("commit", "failed", stableCode(err), 0, time.Since(start))
				return total, err
			}
			total++
			d.observe("commit", "processing", "", 0, time.Since(start))
		}
		if len(values) < d.config.BatchSize {
			break
		}
	}
	return total, nil
}

func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.config.Interval)
	defer ticker.Stop()
	for {
		if _, err := d.Tick(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (d *Dispatcher) now() time.Time {
	if d.config.Now != nil {
		return d.config.Now().UTC()
	}
	return time.Now().UTC()
}
func (d *Dispatcher) observe(event, status, code string, attempt int, latency time.Duration) {
	if d.config.Observer != nil {
		d.config.Observer.Observe("dispatcher", event, status, code, attempt, latency)
	}
}

type DeliveryRequest struct {
	Key, ReminderID, Content string
	Owner                    reminder.Owner
}
type DeliveryResult struct{ ProviderID string }
type Adapter interface {
	Deliver(context.Context, DeliveryRequest) (DeliveryResult, error)
	SupportsIdempotency() bool
}

type RecordingAdapter struct {
	mu      sync.Mutex
	results map[string]DeliveryResult
	Calls   int
}

func NewRecordingAdapter() *RecordingAdapter {
	return &RecordingAdapter{results: map[string]DeliveryResult{}}
}
func (a *RecordingAdapter) SupportsIdempotency() bool { return true }
func (a *RecordingAdapter) Deliver(_ context.Context, request DeliveryRequest) (DeliveryResult, error) {
	if a == nil || strings.TrimSpace(request.Key) == "" {
		return DeliveryResult{}, reminder.ErrInvalidInput
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if result, ok := a.results[request.Key]; ok {
		return result, nil
	}
	a.Calls++
	result := DeliveryResult{ProviderID: "recorded:" + request.Key}
	a.results[request.Key] = result
	return result, nil
}

type DeliveryError struct {
	Code      string
	Retryable bool
	Err       error
}

func (e *DeliveryError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}
func (e *DeliveryError) Unwrap() error { return e.Err }

type WorkerConfig struct {
	BatchSize, MaxBatches, MaxAttempts               int
	LeaseDuration, Interval, BaseBackoff, MaxBackoff time.Duration
	Production                                       bool
	Now                                              func() time.Time
	NewToken                                         func() string
	Observer                                         Observer
	BeforeComplete                                   func(reminder.Delivery) error
}
type Worker struct {
	repository reminder.Repository
	adapter    Adapter
	config     WorkerConfig
}

func NewWorker(repository reminder.Repository, adapter Adapter, config WorkerConfig) (*Worker, error) {
	if repository == nil || adapter == nil || config.BatchSize < 1 || config.BatchSize > reminder.MaxPageSize || config.MaxBatches < 1 || config.MaxAttempts < 1 || config.LeaseDuration < time.Second || config.Interval <= 0 || config.BaseBackoff <= 0 || config.MaxBackoff < config.BaseBackoff || config.NewToken == nil || config.Production && !adapter.SupportsIdempotency() {
		return nil, reminder.ErrInvalidInput
	}
	return &Worker{repository: repository, adapter: adapter, config: config}, nil
}

func (w *Worker) Tick(ctx context.Context) (int, error) {
	if w == nil {
		return 0, reminder.ErrInvalidInput
	}
	total := 0
	for batch := 0; batch < w.config.MaxBatches; batch++ {
		now := w.now()
		token := strings.TrimSpace(w.config.NewToken())
		if token == "" {
			return total, reminder.ErrInvalidInput
		}
		values, err := w.repository.ClaimDeliveries(ctx, w.config.BatchSize, now, w.config.LeaseDuration, token)
		if err != nil {
			return total, err
		}
		for _, value := range values {
			start := time.Now()
			_, deliveryErr := w.adapter.Deliver(ctx, DeliveryRequest{Key: value.DeliveryKey, ReminderID: value.ReminderID, Owner: value.Owner, Content: value.Content})
			if deliveryErr == nil && w.config.BeforeComplete != nil {
				deliveryErr = w.config.BeforeComplete(value)
			}
			if deliveryErr == nil {
				deliveryErr = w.repository.CompleteDelivery(ctx, value.ID, token, now)
				if deliveryErr == nil {
					total++
					w.observe("deliver", "fired", "", value.Attempt, time.Since(start))
					continue
				}
				return total, deliveryErr
			}
			code, retryable := classify(deliveryErr)
			permanent := !retryable || value.Attempt >= w.config.MaxAttempts
			next := now.Add(w.backoff(value.Attempt))
			if err := w.repository.FailDelivery(ctx, reminder.DeliveryFailure{ID: value.ID, ClaimToken: token, ErrorCode: code, Now: now, NextAvailable: next, Permanent: permanent}); err != nil {
				return total, err
			}
			w.observe("deliver", map[bool]string{true: "failed", false: "retrying"}[permanent], code, value.Attempt, time.Since(start))
		}
		if len(values) < w.config.BatchSize {
			break
		}
	}
	return total, nil
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()
	for {
		if _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (w *Worker) now() time.Time {
	if w.config.Now != nil {
		return w.config.Now().UTC()
	}
	return time.Now().UTC()
}
func (w *Worker) backoff(attempt int) time.Duration {
	multiplier := math.Pow(2, float64(max(0, attempt-1)))
	value := time.Duration(float64(w.config.BaseBackoff) * multiplier)
	if value > w.config.MaxBackoff {
		return w.config.MaxBackoff
	}
	return value
}
func (w *Worker) observe(event, status, code string, attempt int, latency time.Duration) {
	if w.config.Observer != nil {
		w.config.Observer.Observe("worker", event, status, code, attempt, latency)
	}
}
func classify(err error) (string, bool) {
	var typed *DeliveryError
	if errors.As(err, &typed) {
		code := strings.TrimSpace(typed.Code)
		if code == "" {
			code = "delivery_error"
		}
		return code, typed.Retryable
	}
	return "delivery_error", false
}
func stableCode(err error) string {
	switch {
	case errors.Is(err, reminder.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, reminder.ErrStateConflict):
		return "state_conflict"
	default:
		return "repository_error"
	}
}

type Observation struct {
	Component, Event, Status, ErrorCode string
	Attempt                             int
	Latency                             time.Duration
}
type RecordingObserver struct {
	mu     sync.Mutex
	Values []Observation
}

func (o *RecordingObserver) Observe(component, event, status, code string, attempt int, latency time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Values = append(o.Values, Observation{component, event, status, code, attempt, latency})
}
