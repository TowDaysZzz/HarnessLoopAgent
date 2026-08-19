package runtime

import (
	"context"
	"time"
)

type Stage string

const (
	StageRunStart   Stage = "run.start"
	StageRunEnd     Stage = "run.end"
	StageModelStart Stage = "model.start"
	StageModelEnd   Stage = "model.end"
	StageToolStart  Stage = "tool.start"
	StageToolEnd    Stage = "tool.end"
	StageEvidence   Stage = "evidence.evaluated"
	StageValidation Stage = "answer.validated"
)

type Event struct {
	RunID    string
	Stage    Stage
	Name     string
	Attempt  int
	Duration time.Duration
	Err      error
	Fields   map[string]any
}

type Observer interface {
	Observe(ctx context.Context, event Event)
}

type NoopObserver struct{}

func (NoopObserver) Observe(context.Context, Event) {}
