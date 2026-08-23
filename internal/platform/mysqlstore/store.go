package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
)

type Store struct {
	db                *sql.DB
	projectionVersion string
}

type Options struct {
	DSN               string
	MaxOpenConns      int
	MaxIdleConns      int
	ConnMaxLifetime   time.Duration
	ProjectionVersion string
}

func Open(ctx context.Context, options Options) (*Store, error) {
	if options.DSN == "" {
		return nil, errors.New("database DSN is required")
	}
	db, err := sql.Open("mysql", options.DSN)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	if options.MaxOpenConns > 0 {
		db.SetMaxOpenConns(options.MaxOpenConns)
	}
	if options.MaxIdleConns >= 0 {
		db.SetMaxIdleConns(options.MaxIdleConns)
	}
	if options.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(options.ConnMaxLifetime)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	projectionVersion := strings.TrimSpace(options.ProjectionVersion)
	if len(projectionVersion) > 128 {
		db.Close()
		return nil, errors.New("memory projection version is too large")
	}
	return &Store{db: db, projectionVersion: projectionVersion}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateSession(ctx context.Context, session chat.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_sessions (id, user_id, tenant_id, title, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.UserID, session.TenantID, session.Title, session.Status, session.CreatedAt, session.UpdatedAt)
	return err
}

func (s *Store) ListSessions(ctx context.Context, owner chat.Owner, limit int) ([]chat.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, tenant_id, title, status, created_at, updated_at
		FROM chat_sessions WHERE user_id = ? AND tenant_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, owner.UserID, owner.TenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []chat.Session
	for rows.Next() {
		var session chat.Session
		if err := rows.Scan(&session.ID, &session.UserID, &session.TenantID, &session.Title, &session.Status, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) GetSession(ctx context.Context, owner chat.Owner, id string) (chat.Session, error) {
	var session chat.Session
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, tenant_id, title, status, created_at, updated_at
		FROM chat_sessions WHERE id = ? AND user_id = ? AND tenant_id = ?`, id, owner.UserID, owner.TenantID).
		Scan(&session.ID, &session.UserID, &session.TenantID, &session.Title, &session.Status, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.Session{}, chat.ErrNotFound
	}
	return session, err
}

func (s *Store) ListMessages(ctx context.Context, owner chat.Owner, sessionID string, limit int) ([]chat.Message, error) {
	if _, err := s.GetSession(ctx, owner, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, COALESCE(run_id, ''), sequence, role, content, created_at
		FROM (
			SELECT id, session_id, run_id, sequence, role, content, created_at
			FROM chat_messages WHERE session_id = ? ORDER BY sequence DESC LIMIT ?
		) recent ORDER BY sequence ASC`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []chat.Message
	for rows.Next() {
		var message chat.Message
		if err := rows.Scan(&message.ID, &message.SessionID, &message.RunID, &message.Sequence, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) CreateRun(ctx context.Context, input chat.CreateRunInput, run chat.Run, userMessage chat.Message, queued chat.Event) (chat.CreatedRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.CreatedRun{}, err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM chat_sessions WHERE id = ? AND user_id = ? AND tenant_id = ? AND status = 'active' FOR UPDATE`, input.SessionID, input.Owner.UserID, input.Owner.TenantID).Scan(&sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return chat.CreatedRun{}, chat.ErrNotFound
		}
		return chat.CreatedRun{}, err
	}
	existing, err := getRunByIdempotency(ctx, tx, input.SessionID, input.IdempotencyKey)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return chat.CreatedRun{}, err
		}
		return chat.CreatedRun{Run: existing, Created: false}, nil
	}
	if !errors.Is(err, chat.ErrNotFound) {
		return chat.CreatedRun{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE session_id = ? AND status IN ('queued','running')`, input.SessionID).Scan(&active); err != nil {
		return chat.CreatedRun{}, err
	}
	if active > 0 {
		return chat.CreatedRun{}, chat.ErrActiveRun
	}
	var messageSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM chat_messages WHERE session_id = ?`, input.SessionID).Scan(&messageSequence); err != nil {
		return chat.CreatedRun{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_runs (id, session_id, status, model_name, idempotency_key, last_event_sequence, created_at)
		VALUES (?, ?, 'queued', ?, ?, 1, ?)`,
		run.ID, input.SessionID, run.Model, input.IdempotencyKey, run.CreatedAt); err != nil {
		return chat.CreatedRun{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_messages (id, session_id, run_id, sequence, role, content, created_at)
		VALUES (?, ?, ?, ?, 'user', ?, ?)`,
		userMessage.ID, input.SessionID, run.ID, messageSequence, userMessage.Content, userMessage.CreatedAt); err != nil {
		return chat.CreatedRun{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chat_sessions SET updated_at = ? WHERE id = ?`, userMessage.CreatedAt, input.SessionID); err != nil {
		return chat.CreatedRun{}, err
	}
	queued.Sequence = 1
	if err := insertEvent(ctx, tx, queued); err != nil {
		return chat.CreatedRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return chat.CreatedRun{}, err
	}
	return chat.CreatedRun{Run: run, Created: true}, nil
}

func (s *Store) GetRun(ctx context.Context, owner chat.Owner, id string) (chat.Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, scopedRunSelect+` WHERE r.id = ? AND cs.user_id = ? AND cs.tenant_id = ?`, id, owner.UserID, owner.TenantID))
}

func (s *Store) StartRun(ctx context.Context, runID string, event chat.Event) error {
	return s.updateRunWithEvent(ctx, runID, chat.RunQueued, event, func(tx *sql.Tx, sequence int64) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = 'running', started_at = ?, last_event_sequence = ? WHERE id = ?`, event.CreatedAt, sequence, runID)
		return err
	})
}

func (s *Store) AppendEvent(ctx context.Context, event chat.Event) (chat.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.Event{}, err
	}
	defer tx.Rollback()
	sequence, status, err := lockRun(ctx, tx, event.RunID)
	if err != nil {
		return chat.Event{}, err
	}
	if status.Terminal() {
		return chat.Event{}, chat.ErrInvalidState
	}
	event.Sequence = sequence + 1
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET last_event_sequence = ? WHERE id = ?`, event.Sequence, event.RunID); err != nil {
		return chat.Event{}, err
	}
	if err := insertEvent(ctx, tx, event); err != nil {
		return chat.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return chat.Event{}, err
	}
	return event, nil
}

func (s *Store) CompleteRun(ctx context.Context, runID string, assistant chat.Message, event chat.Event) error {
	return s.updateRunWithEvent(ctx, runID, chat.RunRunning, event, func(tx *sql.Tx, sequence int64) error {
		var messageSequence int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM chat_messages WHERE session_id = ?`, assistant.SessionID).Scan(&messageSequence); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chat_messages (id, session_id, run_id, sequence, role, content, created_at)
			VALUES (?, ?, ?, ?, 'assistant', ?, ?)`,
			assistant.ID, assistant.SessionID, runID, messageSequence, assistant.Content, assistant.CreatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE agent_runs SET status = 'completed', completed_at = ?, last_event_sequence = ? WHERE id = ?`,
			event.CreatedAt, sequence, runID)
		return err
	})
}

func (s *Store) FailRun(ctx context.Context, runID string, status chat.RunStatus, code, message string, event chat.Event) error {
	return s.updateNonTerminalRunWithEvent(ctx, runID, event, func(tx *sql.Tx, sequence int64) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE agent_runs SET status = ?, error_code = ?, error_message = ?, completed_at = ?, last_event_sequence = ? WHERE id = ?`,
			status, code, message, event.CreatedAt, sequence, runID)
		return err
	})
}

func (s *Store) CancelRun(ctx context.Context, owner chat.Owner, runID string, event chat.Event) (chat.Run, error) {
	if _, err := s.GetRun(ctx, owner, runID); err != nil {
		return chat.Run{}, err
	}
	err := s.updateNonTerminalRunWithEvent(ctx, runID, event, func(tx *sql.Tx, sequence int64) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = 'cancelled', completed_at = ?, last_event_sequence = ? WHERE id = ?`, event.CreatedAt, sequence, runID)
		return err
	})
	if err != nil {
		return chat.Run{}, err
	}
	return s.getRun(ctx, runID)
}

func (s *Store) ListEvents(ctx context.Context, owner chat.Owner, runID string, after int64, limit int) ([]chat.Event, error) {
	if _, err := s.GetRun(ctx, owner, runID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, sequence, event_type, event_data, created_at
		FROM agent_run_events WHERE run_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT ?`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []chat.Event
	for rows.Next() {
		var event chat.Event
		var raw []byte
		if err := rows.Scan(&event.RunID, &event.Sequence, &event.Type, &raw, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &event.Data); err != nil {
			return nil, fmt.Errorf("decode event %d: %w", event.Sequence, err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) InterruptRunning(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, last_event_sequence FROM agent_runs WHERE status IN ('queued','running') FOR UPDATE`)
	if err != nil {
		return err
	}
	type interrupted struct {
		id       string
		sequence int64
	}
	var runs []interrupted
	for rows.Next() {
		var run interrupted
		if err := rows.Scan(&run.id, &run.sequence); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, run := range runs {
		event := chat.Event{RunID: run.id, Sequence: run.sequence + 1, Type: "run.interrupted", Data: map[string]any{"status": chat.RunInterrupted}, CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = 'interrupted', completed_at = ?, last_event_sequence = ? WHERE id = ?`, now, event.Sequence, run.id); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) updateRunWithEvent(ctx context.Context, runID string, expected chat.RunStatus, event chat.Event, update func(*sql.Tx, int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	last, status, err := lockRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	if status != expected {
		return chat.ErrInvalidState
	}
	event.Sequence = last + 1
	if err := update(tx, event.Sequence); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) updateNonTerminalRunWithEvent(ctx context.Context, runID string, event chat.Event, update func(*sql.Tx, int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	last, status, err := lockRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	if status.Terminal() {
		return chat.ErrInvalidState
	}
	event.Sequence = last + 1
	if err := update(tx, event.Sequence); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

type rowScanner interface {
	Scan(...any) error
}

const runSelect = `SELECT id, session_id, status, model_name, idempotency_key, COALESCE(error_code, ''), COALESCE(error_message, ''), created_at, started_at, completed_at FROM agent_runs`
const scopedRunSelect = `SELECT r.id, r.session_id, r.status, r.model_name, r.idempotency_key, COALESCE(r.error_code, ''), COALESCE(r.error_message, ''), r.created_at, r.started_at, r.completed_at FROM agent_runs r JOIN chat_sessions cs ON cs.id = r.session_id`

func (s *Store) getRun(ctx context.Context, id string) (chat.Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` WHERE id = ?`, id))
}

func scanRun(row rowScanner) (chat.Run, error) {
	var run chat.Run
	var started, completed sql.NullTime
	if err := row.Scan(&run.ID, &run.SessionID, &run.Status, &run.Model, &run.IdempotencyKey, &run.ErrorCode, &run.ErrorMessage, &run.CreatedAt, &started, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return chat.Run{}, chat.ErrNotFound
		}
		return chat.Run{}, err
	}
	if started.Valid {
		run.StartedAt = &started.Time
	}
	if completed.Valid {
		run.CompletedAt = &completed.Time
	}
	return run, nil
}

func getRunByIdempotency(ctx context.Context, tx *sql.Tx, sessionID, key string) (chat.Run, error) {
	return scanRun(tx.QueryRowContext(ctx, runSelect+` WHERE session_id = ? AND idempotency_key = ?`, sessionID, key))
}

func lockRun(ctx context.Context, tx *sql.Tx, runID string) (int64, chat.RunStatus, error) {
	var sequence int64
	var status chat.RunStatus
	err := tx.QueryRowContext(ctx, `SELECT last_event_sequence, status FROM agent_runs WHERE id = ? FOR UPDATE`, runID).Scan(&sequence, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", chat.ErrNotFound
	}
	return sequence, status, err
}

func insertEvent(ctx context.Context, tx *sql.Tx, event chat.Event) error {
	raw, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_run_events (run_id, sequence, event_type, event_data, created_at) VALUES (?, ?, ?, ?, ?)`,
		event.RunID, event.Sequence, event.Type, raw, event.CreatedAt)
	return err
}
