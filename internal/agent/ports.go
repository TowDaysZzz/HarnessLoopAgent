package agent

import "context"

type EventType string

const (
	EventTextDelta      EventType = "text.delta"
	EventToolCompleted  EventType = "tool.completed"
	EventRunCompleted   EventType = "run.completed"
	EventRunFailed      EventType = "run.failed"
	EventStatus         EventType = "run.status"
	EventDraftCandidate EventType = "note.draft.candidate"
)

type Event struct {
	Type     EventType
	Delta    string
	ToolName string
	Err      error
	Status   string
}

type Message struct {
	Role    string
	Content string
}

type StreamRunner interface {
	Stream(ctx context.Context, prompt string) <-chan Event
}

type ConversationRunner interface {
	StreamMessages(ctx context.Context, messages []Message) <-chan Event
}
