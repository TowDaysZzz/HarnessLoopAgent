package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrModelBudgetExceeded = errors.New("agent model call budget exceeded")
	ErrToolBudgetExceeded  = errors.New("agent tool call budget exceeded")
)

type Budget struct {
	RunTimeout    time.Duration
	MaxModelCalls int
	MaxToolCalls  int
}

type State struct {
	RunID string

	mu         sync.Mutex
	modelCalls int
	toolCalls  int
	budget     Budget
	observer   Observer
}

type stateKey struct{}
type requestedRunIDKey struct{}

// WithRunID correlates runtime/model/tool observations with the persisted HTTP run.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, requestedRunIDKey{}, runID)
}

func Start(ctx context.Context, budget Budget, observer Observer) (context.Context, context.CancelFunc, *State) {
	if observer == nil {
		observer = NoopObserver{}
	}
	runID, _ := ctx.Value(requestedRunIDKey{}).(string)
	if runID == "" {
		runID = newRunID()
	}
	state := &State{RunID: runID, budget: budget, observer: observer}
	if budget.RunTimeout > 0 {
		ctx, cancel := context.WithTimeout(ctx, budget.RunTimeout)
		ctx = context.WithValue(ctx, stateKey{}, state)
		return ctx, cancel, state
	}
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, stateKey{}, state)
	return ctx, cancel, state
}

func StateFrom(ctx context.Context) *State {
	state, _ := ctx.Value(stateKey{}).(*State)
	return state
}

func ConsumeModelCall(ctx context.Context) error {
	state := StateFrom(ctx)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.budget.MaxModelCalls > 0 && state.modelCalls >= state.budget.MaxModelCalls {
		return ErrModelBudgetExceeded
	}
	state.modelCalls++
	return nil
}

func ConsumeToolCall(ctx context.Context) error {
	state := StateFrom(ctx)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.budget.MaxToolCalls > 0 && state.toolCalls >= state.budget.MaxToolCalls {
		return ErrToolBudgetExceeded
	}
	state.toolCalls++
	return nil
}

func Snapshot(ctx context.Context) (modelCalls, toolCalls int) {
	state := StateFrom(ctx)
	if state == nil {
		return 0, 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.modelCalls, state.toolCalls
}

func Emit(ctx context.Context, event Event) {
	state := StateFrom(ctx)
	if state == nil {
		return
	}
	event.RunID = state.RunID
	state.observer.Observe(ctx, event)
}

func newRunID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	return fmt.Sprintf("run-%d", time.Now().UnixNano())
}
