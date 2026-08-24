package chat

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

type MemoryRepository struct {
	mu              sync.Mutex
	sessions        map[string]Session
	messages        map[string][]Message
	runs            map[string]Run
	events          map[string][]Event
	idem            map[string]string
	invocations     map[string]skill.Invocation
	invocationByRun map[string]string
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		sessions: make(map[string]Session), messages: make(map[string][]Message),
		runs: make(map[string]Run), events: make(map[string][]Event), idem: make(map[string]string),
		invocations: make(map[string]skill.Invocation), invocationByRun: make(map[string]string),
	}
}

func (r *MemoryRepository) CreateInvocation(_ context.Context, value skill.Invocation) (skill.Invocation, bool, error) {
	if err := value.Validate(); err != nil {
		return skill.Invocation{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.invocationByRun[value.ChatRunID]; ok {
		existing := r.invocations[id]
		if existing.Owner != value.Owner || existing.Skill != value.Skill || existing.ArgumentsHash != value.ArgumentsHash {
			return skill.Invocation{}, false, skill.ErrInvalidInvocation
		}
		return existing, false, nil
	}
	r.invocations[value.ID] = value
	r.invocationByRun[value.ChatRunID] = value.ID
	return value, true, nil
}

func (r *MemoryRepository) GetInvocation(_ context.Context, owner skill.Owner, id string) (skill.Invocation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.invocations[id]
	if !ok || value.Owner != owner {
		return skill.Invocation{}, skill.ErrNotFound
	}
	return value, nil
}

func (r *MemoryRepository) TransitionInvocation(_ context.Context, owner skill.Owner, id string, from, to skill.InvocationStatus, _ string, now time.Time) (skill.Invocation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.invocations[id]
	if !ok || value.Owner != owner || value.Status != from || !skill.CanTransitionInvocation(from, to) {
		return skill.Invocation{}, skill.ErrNotFound
	}
	value.Status, value.UpdatedAt = to, now.UTC()
	r.invocations[id] = value
	return value, nil
}

func (r *MemoryRepository) CreateSession(_ context.Context, session Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session.ID] = session
	return nil
}

func (r *MemoryRepository) ListSessions(_ context.Context, owner Owner, limit int) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Session, 0, len(r.sessions))
	for _, session := range r.sessions {
		if session.UserID == owner.UserID && session.TenantID == owner.TenantID {
			result = append(result, session)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (r *MemoryRepository) GetSession(_ context.Context, owner Owner, id string) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id]
	if !ok || session.UserID != owner.UserID || session.TenantID != owner.TenantID {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (r *MemoryRepository) ListMessages(_ context.Context, owner Owner, sessionID string, limit int) ([]Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, ok := r.sessions[sessionID]; !ok || session.UserID != owner.UserID || session.TenantID != owner.TenantID {
		return nil, ErrNotFound
	}
	messages := r.messages[sessionID]
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return append([]Message(nil), messages...), nil
}

func (r *MemoryRepository) CreateRun(_ context.Context, input CreateRunInput, run Run, userMessage Message, queued Event) (CreatedRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[input.SessionID]; !ok {
		return CreatedRun{}, ErrNotFound
	}
	if session := r.sessions[input.SessionID]; session.UserID != input.Owner.UserID || session.TenantID != input.Owner.TenantID {
		return CreatedRun{}, ErrNotFound
	}
	idemKey := input.SessionID + "\x00" + input.IdempotencyKey
	if existingID, ok := r.idem[idemKey]; ok {
		return CreatedRun{Run: r.runs[existingID], Created: false}, nil
	}
	for _, existing := range r.runs {
		if existing.SessionID == input.SessionID && !existing.Status.Terminal() {
			return CreatedRun{}, ErrActiveRun
		}
	}
	userMessage.Sequence = int64(len(r.messages[input.SessionID]) + 1)
	r.messages[input.SessionID] = append(r.messages[input.SessionID], userMessage)
	session := r.sessions[input.SessionID]
	session.UpdatedAt = userMessage.CreatedAt
	r.sessions[input.SessionID] = session
	r.runs[run.ID] = run
	r.idem[idemKey] = run.ID
	queued.Sequence = 1
	r.events[run.ID] = append(r.events[run.ID], queued)
	return CreatedRun{Run: run, Created: true}, nil
}

func (r *MemoryRepository) GetRun(_ context.Context, owner Owner, id string) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	session := r.sessions[run.SessionID]
	if session.UserID != owner.UserID || session.TenantID != owner.TenantID {
		return Run{}, ErrNotFound
	}
	return run, nil
}

func (r *MemoryRepository) StartRun(_ context.Context, runID string, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if run.Status != RunQueued {
		return ErrInvalidState
	}
	now := event.CreatedAt
	run.Status, run.StartedAt = RunRunning, &now
	r.runs[runID] = run
	r.appendEventLocked(event)
	return nil
}

func (r *MemoryRepository) AppendEvent(_ context.Context, event Event) (Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[event.RunID]; !ok {
		return Event{}, ErrNotFound
	}
	return r.appendEventLocked(event), nil
}

func (r *MemoryRepository) CompleteRun(_ context.Context, runID string, assistant Message, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if run.Status != RunRunning {
		return ErrInvalidState
	}
	assistant.Sequence = int64(len(r.messages[run.SessionID]) + 1)
	r.messages[run.SessionID] = append(r.messages[run.SessionID], assistant)
	now := event.CreatedAt
	run.Status, run.CompletedAt = RunCompleted, &now
	r.runs[runID] = run
	r.appendEventLocked(event)
	return nil
}

func (r *MemoryRepository) FailRun(_ context.Context, runID string, status RunStatus, code, message string, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if run.Status.Terminal() {
		return ErrInvalidState
	}
	now := event.CreatedAt
	run.Status, run.ErrorCode, run.ErrorMessage, run.CompletedAt = status, code, message, &now
	r.runs[runID] = run
	r.appendEventLocked(event)
	return nil
}

func (r *MemoryRepository) CancelRun(_ context.Context, owner Owner, runID string, event Event) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return Run{}, ErrNotFound
	}
	session := r.sessions[run.SessionID]
	if session.UserID != owner.UserID || session.TenantID != owner.TenantID {
		return Run{}, ErrNotFound
	}
	if run.Status.Terminal() {
		return run, ErrInvalidState
	}
	now := event.CreatedAt
	run.Status, run.CompletedAt = RunCancelled, &now
	r.runs[runID] = run
	r.appendEventLocked(event)
	return run, nil
}

func (r *MemoryRepository) ListEvents(_ context.Context, owner Owner, runID string, after int64, limit int) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return nil, ErrNotFound
	}
	session := r.sessions[run.SessionID]
	if session.UserID != owner.UserID || session.TenantID != owner.TenantID {
		return nil, ErrNotFound
	}
	var result []Event
	for _, event := range r.events[runID] {
		if event.Sequence > after {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (r *MemoryRepository) InterruptRunning(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, run := range r.runs {
		if run.Status == RunRunning || run.Status == RunQueued {
			now := time.Now().UTC()
			run.Status, run.CompletedAt = RunInterrupted, &now
			r.runs[id] = run
			r.appendEventLocked(Event{RunID: id, Type: "run.interrupted", Data: map[string]any{"status": RunInterrupted}, CreatedAt: now})
		}
	}
	return nil
}

func (r *MemoryRepository) appendEventLocked(event Event) Event {
	event.Sequence = int64(len(r.events[event.RunID]) + 1)
	r.events[event.RunID] = append(r.events[event.RunID], event)
	return event
}
