package chat

import (
	"context"
	"errors"
)

var (
	ErrNotFound     = errors.New("resource not found")
	ErrActiveRun    = errors.New("session already has an active run")
	ErrInvalidState = errors.New("invalid run state transition")
	ErrInvalidInput = errors.New("invalid input")
)

type Repository interface {
	CreateSession(ctx context.Context, session Session) error
	ListSessions(ctx context.Context, owner Owner, limit int) ([]Session, error)
	GetSession(ctx context.Context, owner Owner, id string) (Session, error)
	ListMessages(ctx context.Context, owner Owner, sessionID string, limit int) ([]Message, error)
	CreateRun(ctx context.Context, input CreateRunInput, run Run, userMessage Message, queued Event) (CreatedRun, error)
	GetRun(ctx context.Context, owner Owner, id string) (Run, error)
	StartRun(ctx context.Context, runID string, event Event) error
	AppendEvent(ctx context.Context, event Event) (Event, error)
	CompleteRun(ctx context.Context, runID string, assistant Message, event Event) error
	FailRun(ctx context.Context, runID string, status RunStatus, code, message string, event Event) error
	CancelRun(ctx context.Context, owner Owner, runID string, event Event) (Run, error)
	ListEvents(ctx context.Context, owner Owner, runID string, after int64, limit int) ([]Event, error)
	InterruptRunning(ctx context.Context) error
}
