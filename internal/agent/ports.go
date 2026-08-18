package agent

import "context"

type EventType string

const (
	EventTextDelta     EventType = "text.delta"
	EventToolCompleted EventType = "tool.completed"
	EventRunCompleted  EventType = "run.completed"
	EventRunFailed     EventType = "run.failed"
)

type Event struct {
	Type     EventType
	Delta    string
	ToolName string
	Err      error
}

type StreamRunner interface {
	Stream(ctx context.Context, prompt string) <-chan Event
}
