package workflow

import (
	"context"
	"sync"
	"time"
)

type NodeEventType string

const (
	EventNodeStarted   NodeEventType = "node.started"
	EventNodeCompleted NodeEventType = "node.completed"
	EventNodeSuspended NodeEventType = "node.suspended"
	EventNodeResumed   NodeEventType = "node.resumed"
	EventNodeFailed    NodeEventType = "node.failed"
	EventNodeSkipped   NodeEventType = "node.skipped"
)

func (t NodeEventType) Valid() bool {
	switch t {
	case EventNodeStarted, EventNodeCompleted, EventNodeSuspended, EventNodeResumed, EventNodeFailed, EventNodeSkipped:
		return true
	default:
		return false
	}
}

type NodeEvent struct {
	Sequence    int64         `json:"sequence"`
	WorkflowID  WorkflowID    `json:"workflow_id"`
	RunID       WorkflowRunID `json:"run_id"`
	NodeID      NodeID        `json:"node_id"`
	Type        NodeEventType `json:"type"`
	Status      RunStatus     `json:"status"`
	Attempt     int           `json:"attempt"`
	ResumeCount int           `json:"resume_count"`
	WaitID      WaitID        `json:"wait_id,omitempty"`
	ErrorCode   ErrorCode     `json:"error_code,omitempty"`
	Duration    time.Duration `json:"duration"`
	OccurredAt  time.Time     `json:"occurred_at"`
}

type Observer interface {
	// Observe is synchronous but not transactional with node side effects.
	// Side-effecting nodes must remain idempotent until a durable audit/outbox
	// adapter is introduced.
	Observe(context.Context, NodeEvent) error
}

type NoopObserver struct{}

func (NoopObserver) Observe(context.Context, NodeEvent) error { return nil }

type MemoryCollector struct {
	mu        sync.Mutex
	events    []NodeEvent
	lastByRun map[WorkflowRunID]int64
}

func NewMemoryCollector() *MemoryCollector {
	return &MemoryCollector{lastByRun: make(map[WorkflowRunID]int64)}
}

func (c *MemoryCollector) Observe(_ context.Context, event NodeEvent) error {
	if c == nil {
		return contractError("nil memory collector", nil)
	}
	if event.Sequence < 1 || !validIdentifier(string(event.WorkflowID)) || !validIdentifier(string(event.RunID)) || !validIdentifier(string(event.NodeID)) || !event.Type.Valid() {
		return contractError("invalid node event", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if event.Sequence <= c.lastByRun[event.RunID] {
		return contractError("node event sequence must increase", nil)
	}
	c.lastByRun[event.RunID] = event.Sequence
	c.events = append(c.events, event)
	return nil
}

func (c *MemoryCollector) Events() []NodeEvent {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]NodeEvent(nil), c.events...)
}
