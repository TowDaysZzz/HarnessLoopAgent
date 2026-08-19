package runtime

import (
	"context"
	"log"
)

type LogObserver struct{}

func (LogObserver) Observe(_ context.Context, event Event) {
	log.Printf("agent_event stage=%s run_id=%s name=%s attempt=%d duration=%s error=%v fields=%v",
		event.Stage, event.RunID, event.Name, event.Attempt, event.Duration, event.Err, event.Fields)
}
