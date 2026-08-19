package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
)

type ServiceOptions struct {
	MessageHistoryLimit int
	DefaultModel        string
}

type Service struct {
	root      context.Context
	repo      Repository
	runner    agent.ConversationRunner
	assembler contextmanager.Assembler
	notifier  *Notifier
	options   ServiceOptions

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewService(root context.Context, repo Repository, runner agent.ConversationRunner, assembler contextmanager.Assembler, options ServiceOptions) (*Service, error) {
	if repo == nil || runner == nil || assembler == nil {
		return nil, errors.New("chat service requires repository, runner, and context assembler")
	}
	if options.MessageHistoryLimit < 1 {
		options.MessageHistoryLimit = 100
	}
	service := &Service{
		root: root, repo: repo, runner: runner, assembler: assembler,
		notifier: NewNotifier(), options: options, cancels: make(map[string]context.CancelFunc),
	}
	if err := repo.InterruptRunning(root); err != nil {
		return nil, fmt.Errorf("recover interrupted runs: %w", err)
	}
	return service, nil
}

func (s *Service) CreateSession(ctx context.Context, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	if len([]rune(title)) > 200 {
		return Session{}, fmt.Errorf("%w: session title is too long", ErrInvalidInput)
	}
	now := time.Now().UTC()
	session := Session{ID: uuid.NewString(), Title: title, Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Service) GetSession(ctx context.Context, id string) (Session, error) {
	return s.repo.GetSession(ctx, id)
}

func (s *Service) ListMessages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return s.repo.ListMessages(ctx, sessionID, limit)
}

func (s *Service) CreateRun(ctx context.Context, input CreateRunInput) (CreatedRun, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Content = strings.TrimSpace(input.Content)
	input.Model = strings.TrimSpace(input.Model)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.SessionID == "" || input.Content == "" {
		return CreatedRun{}, fmt.Errorf("%w: session_id and message are required", ErrInvalidInput)
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 {
		return CreatedRun{}, fmt.Errorf("%w: a valid Idempotency-Key is required", ErrInvalidInput)
	}
	if len([]rune(input.Content)) > 50000 {
		return CreatedRun{}, fmt.Errorf("%w: message is too long", ErrInvalidInput)
	}
	if input.Model == "" {
		input.Model = s.options.DefaultModel
	} else if input.Model != s.options.DefaultModel {
		return CreatedRun{}, fmt.Errorf("%w: model %q is not active on this server", ErrInvalidInput, input.Model)
	}
	now := time.Now().UTC()
	run := Run{
		ID: uuid.NewString(), SessionID: input.SessionID, Status: RunQueued, Model: input.Model,
		IdempotencyKey: input.IdempotencyKey, CreatedAt: now,
	}
	message := Message{
		ID: uuid.NewString(), SessionID: input.SessionID, RunID: run.ID,
		Role: "user", Content: input.Content, CreatedAt: now,
	}
	created, err := s.repo.CreateRun(ctx, input, run, message, Event{
		RunID: run.ID, Type: "run.queued", Data: map[string]any{"status": RunQueued}, CreatedAt: now,
	})
	if err != nil {
		return CreatedRun{}, err
	}
	if created.Created {
		s.notifier.Notify(run.ID)
		go s.execute(run.ID)
	}
	return created, nil
}

func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	return s.repo.GetRun(ctx, id)
}

func (s *Service) ListEvents(ctx context.Context, runID string, after int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return s.repo.ListEvents(ctx, runID, after, limit)
}

func (s *Service) Subscribe(runID string) (<-chan struct{}, func()) {
	return s.notifier.Subscribe(runID)
}

func (s *Service) CancelRun(ctx context.Context, runID string) (Run, error) {
	run, err := s.repo.CancelRun(ctx, runID, Event{
		RunID: runID, Type: "run.cancelled", Data: map[string]any{"status": RunCancelled}, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	cancel := s.cancels[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.notifier.Notify(runID)
	return run, nil
}

func (s *Service) execute(runID string) {
	ctx, cancel := context.WithCancel(s.root)
	s.mu.Lock()
	s.cancels[runID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancels, runID)
		s.mu.Unlock()
	}()

	started := Event{RunID: runID, Type: "run.started", Data: map[string]any{"status": RunRunning}, CreatedAt: time.Now().UTC()}
	if err := s.repo.StartRun(ctx, runID, started); err != nil {
		return
	}
	s.notifier.Notify(runID)
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		s.fail(runID, RunFailed, "load_run_failed", err)
		return
	}
	messages, err := s.repo.ListMessages(ctx, run.SessionID, s.options.MessageHistoryLimit)
	if err != nil {
		s.fail(runID, RunFailed, "load_history_failed", err)
		return
	}
	input := make([]agent.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" || message.Role == "assistant" {
			input = append(input, agent.Message{Role: message.Role, Content: message.Content})
		}
	}
	contextResult, err := s.assembler.Build(input)
	if err != nil {
		s.fail(runID, RunFailed, "context_build_failed", err)
		return
	}
	if contextResult.Truncated {
		if _, err := s.append(runID, "context.truncated", map[string]any{
			"dropped_messages": contextResult.DroppedMessages,
			"estimated_tokens": contextResult.EstimatedTokens,
		}); err != nil {
			s.fail(runID, RunFailed, "persist_event_failed", err)
			return
		}
	}

	var answer strings.Builder
	for event := range s.runner.StreamMessages(ctx, contextResult.Messages) {
		switch event.Type {
		case agent.EventTextDelta:
			answer.WriteString(event.Delta)
			if _, err := s.append(runID, string(event.Type), map[string]any{"content": event.Delta}); err != nil {
				cancel()
				s.fail(runID, RunFailed, "persist_event_failed", err)
				return
			}
		case agent.EventStatus:
			if _, err := s.append(runID, string(event.Type), map[string]any{"status": event.Status}); err != nil {
				s.fail(runID, RunFailed, "persist_event_failed", err)
				return
			}
		case agent.EventToolCompleted:
			if _, err := s.append(runID, string(event.Type), map[string]any{"tool": event.ToolName, "summary": event.Delta}); err != nil {
				s.fail(runID, RunFailed, "persist_event_failed", err)
				return
			}
		case agent.EventRunFailed:
			status := RunFailed
			code := "agent_failed"
			if errors.Is(event.Err, context.Canceled) {
				return
			}
			if errors.Is(event.Err, context.DeadlineExceeded) {
				status, code = RunTimedOut, "run_timeout"
			}
			s.fail(runID, status, code, event.Err)
			return
		case agent.EventRunCompleted:
			now := time.Now().UTC()
			assistant := Message{
				ID: uuid.NewString(), SessionID: run.SessionID, RunID: runID,
				Role: "assistant", Content: answer.String(), CreatedAt: now,
			}
			completed := Event{RunID: runID, Type: string(event.Type), Data: map[string]any{"status": RunCompleted}, CreatedAt: now}
			if err := s.repo.CompleteRun(ctx, runID, assistant, completed); err != nil {
				s.fail(runID, RunFailed, "complete_run_failed", err)
				return
			}
			s.notifier.Notify(runID)
			return
		}
	}
	s.fail(runID, RunFailed, "stream_closed", errors.New("agent stream closed without terminal event"))
}

func (s *Service) append(runID, eventType string, data map[string]any) (Event, error) {
	event, err := s.repo.AppendEvent(s.root, Event{RunID: runID, Type: eventType, Data: data, CreatedAt: time.Now().UTC()})
	if err == nil {
		s.notifier.Notify(runID)
	}
	return event, err
}

func (s *Service) fail(runID string, status RunStatus, code string, err error) {
	message := "agent run failed"
	if err != nil {
		message = err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
	}
	eventType := "run.failed"
	if status == RunTimedOut {
		eventType = "run.timed_out"
	}
	event := Event{RunID: runID, Type: eventType, Data: map[string]any{"status": status, "code": code}, CreatedAt: time.Now().UTC()}
	if updateErr := s.repo.FailRun(s.root, runID, status, code, message, event); updateErr == nil {
		s.notifier.Notify(runID)
	}
}
