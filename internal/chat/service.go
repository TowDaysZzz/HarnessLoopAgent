package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
	agentruntime "github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
)

type IntentRouter interface {
	Route(context.Context, routing.RouteInput) routing.RouteDecision
}

type IntentExecutor interface {
	Execute(context.Context, routing.Input) (routing.Execution, error)
}

type ServiceOptions struct {
	MessageHistoryLimit         int
	DefaultModel                string
	EnableIntentRouting         bool
	EnableLegacyRoutingFallback bool
	Router                      IntentRouter
	Executor                    IntentExecutor
}

type Service struct {
	root      context.Context
	repo      Repository
	runner    agent.ConversationRunner
	assembler contextmanager.Assembler
	router    IntentRouter
	executor  IntentExecutor
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
		router: options.Router, executor: options.Executor,
		notifier: NewNotifier(), options: options, cancels: make(map[string]context.CancelFunc),
	}
	if options.EnableIntentRouting && (service.router == nil || service.executor == nil) {
		return nil, errors.New("intent routing requires router and executor")
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
	owner := ownerFromContext(ctx)
	session := Session{ID: uuid.NewString(), UserID: owner.UserID, TenantID: owner.TenantID, Title: title, Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Service) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return s.repo.ListSessions(ctx, ownerFromContext(ctx), limit)
}

func (s *Service) GetSession(ctx context.Context, id string) (Session, error) {
	return s.repo.GetSession(ctx, ownerFromContext(ctx), id)
}

func (s *Service) ListMessages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return s.repo.ListMessages(ctx, ownerFromContext(ctx), sessionID, limit)
}

func (s *Service) CreateRun(ctx context.Context, input CreateRunInput) (CreatedRun, error) {
	input.Owner = ownerFromContext(ctx)
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
		go s.execute(run.ID, input.Owner, input.UserAccessToken, input.KnowledgeBaseIDs)
	}
	return created, nil
}

func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	return s.repo.GetRun(ctx, ownerFromContext(ctx), id)
}

func (s *Service) ListEvents(ctx context.Context, runID string, after int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return s.repo.ListEvents(ctx, ownerFromContext(ctx), runID, after, limit)
}

func (s *Service) Subscribe(runID string) (<-chan struct{}, func()) {
	return s.notifier.Subscribe(runID)
}

func (s *Service) CancelRun(ctx context.Context, runID string) (Run, error) {
	run, err := s.repo.CancelRun(ctx, ownerFromContext(ctx), runID, Event{
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

func (s *Service) execute(runID string, owner Owner, userAccessToken string, knowledgeBaseIDs []uint64) {
	ctx, cancel := context.WithCancel(s.root)
	ctx = agentruntime.WithRunID(ctx, runID)
	ctx = ragclient.WithTraceHeaders(ctx, ragclient.TraceHeaders{RequestID: runID})
	if strings.TrimSpace(userAccessToken) != "" {
		ctx = ragclient.WithUserAccessToken(ctx, userAccessToken)
	}
	if len(knowledgeBaseIDs) > 0 {
		ctx = ragclient.WithKnowledgeBaseIDs(ctx, knowledgeBaseIDs)
	}
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
	run, err := s.repo.GetRun(ctx, owner, runID)
	if err != nil {
		s.fail(runID, RunFailed, "load_run_failed", err)
		return
	}
	messages, err := s.repo.ListMessages(ctx, owner, run.SessionID, s.options.MessageHistoryLimit)
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

	var stream <-chan agent.Event
	handlerName := "legacy"
	decision := routing.RouteDecision{Intent: routing.IntentChat, Complexity: routing.ComplexitySimple, NeedsModel: true, Confidence: 1, Reason: "legacy_routing_disabled"}
	if s.options.EnableIntentRouting {
		lastUserInput := ""
		for index := len(contextResult.Messages) - 1; index >= 0; index-- {
			if contextResult.Messages[index].Role == "user" {
				lastUserInput = contextResult.Messages[index].Content
				break
			}
		}
		decision = s.router.Route(ctx, routing.RouteInput{
			UserID: owner.UserID, TenantID: owner.TenantID, SessionID: run.SessionID, Content: lastUserInput,
		})
		if _, err := s.append(runID, "route.decided", routeEventMap(decision)); err != nil {
			s.fail(runID, RunFailed, "persist_event_failed", err)
			return
		}
		execution, executeErr := s.executor.Execute(ctx, routing.Input{
			Run: routing.RunContext{
				RunID: runID, SessionID: run.SessionID, UserID: owner.UserID, TenantID: owner.TenantID,
				AccessToken: userAccessToken, KnowledgeBaseIDs: append([]uint64(nil), knowledgeBaseIDs...), Decision: decision,
			},
			Content: lastUserInput, Messages: contextResult.Messages,
		})
		if executeErr != nil {
			if !s.canLegacyFallback(decision) {
				s.fail(runID, RunFailed, "executor_unavailable", executeErr)
				return
			}
			stream = s.runner.StreamMessages(ctx, contextResult.Messages)
		} else {
			handlerName, stream = execution.Handler, execution.Events
		}
	} else {
		stream = s.runner.StreamMessages(ctx, contextResult.Messages)
	}
	executorStartedAt := time.Now()
	if _, err := s.append(runID, "executor.started", executorEventMap(decision, handlerName, "started", "", 0)); err != nil {
		s.fail(runID, RunFailed, "persist_event_failed", err)
		return
	}

	var answer strings.Builder
	for event := range stream {
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
		case agent.EventDraftCandidate, agent.EventWorkflowCandidate:
			data := map[string]any{}
			if err := json.Unmarshal([]byte(event.Delta), &data); err != nil {
				s.fail(runID, RunFailed, "invalid_candidate_event", err)
				return
			}
			if _, err := s.append(runID, string(event.Type), data); err != nil {
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
			_, _ = s.append(runID, "executor.failed", executorEventMap(decision, handlerName, "failed", code, time.Since(executorStartedAt)))
			s.fail(runID, status, code, event.Err)
			return
		case agent.EventRunCompleted:
			if _, err := s.append(runID, "executor.completed", executorEventMap(decision, handlerName, "completed", "", time.Since(executorStartedAt))); err != nil {
				s.fail(runID, RunFailed, "persist_event_failed", err)
				return
			}
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
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		_, _ = s.append(runID, "executor.failed", executorEventMap(decision, handlerName, "failed", "run_timeout", time.Since(executorStartedAt)))
		s.fail(runID, RunTimedOut, "run_timeout", ctx.Err())
		return
	}
	_, _ = s.append(runID, "executor.failed", executorEventMap(decision, handlerName, "failed", "stream_closed", time.Since(executorStartedAt)))
	s.fail(runID, RunFailed, "stream_closed", errors.New("agent stream closed without terminal event"))
}

func (s *Service) canLegacyFallback(decision routing.RouteDecision) bool {
	if !s.options.EnableLegacyRoutingFallback {
		return false
	}
	return decision.Intent == routing.IntentChat || decision.Intent == routing.IntentNoteQuery
}

func routeEventMap(decision routing.RouteDecision) map[string]any {
	value := routing.NewRouteEventData(decision)
	return map[string]any{
		"intent": value.Intent, "complexity": value.Complexity, "confidence": value.Confidence,
		"reason": value.Reason, "deterministic": value.Deterministic, "needs_rag": value.NeedsRAG, "needs_model": value.NeedsModel,
	}
}

func executorEventMap(decision routing.RouteDecision, handler, status, errorCode string, duration time.Duration) map[string]any {
	value := routing.NewExecutorEventData(decision, handler, status, errorCode, duration)
	return map[string]any{
		"intent": value.Intent, "complexity": value.Complexity, "handler": value.Handler,
		"status": value.Status, "error_code": value.ErrorCode, "duration_ms": value.DurationMS,
	}
}

func ownerFromContext(ctx context.Context) Owner {
	principal, _ := agentauth.PrincipalFromContext(ctx)
	return Owner{UserID: principal.UserID, TenantID: principal.TenantID}
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
