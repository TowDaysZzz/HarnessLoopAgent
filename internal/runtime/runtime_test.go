package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingObserver struct {
	mu     sync.Mutex
	events []Event
}

func (o *recordingObserver) Observe(_ context.Context, event Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func TestRunBudgetAndObserver(t *testing.T) {
	observer := &recordingObserver{}
	ctx, cancel, state := Start(context.Background(), Budget{RunTimeout: time.Second, MaxModelCalls: 1, MaxToolCalls: 1}, observer)
	defer cancel()
	if state.RunID == "" {
		t.Fatal("missing run ID")
	}
	if err := ConsumeModelCall(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeModelCall(ctx); !errors.Is(err, ErrModelBudgetExceeded) {
		t.Fatalf("second model call error = %v", err)
	}
	if err := ConsumeToolCall(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeToolCall(ctx); !errors.Is(err, ErrToolBudgetExceeded) {
		t.Fatalf("second tool call error = %v", err)
	}
	Emit(ctx, Event{Stage: StageRunStart})
	if len(observer.events) != 1 || observer.events[0].RunID != state.RunID {
		t.Fatalf("events = %#v", observer.events)
	}
}
