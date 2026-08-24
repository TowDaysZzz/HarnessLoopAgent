package mysqlstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type Store struct {
	db                *gorm.DB
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
	db, err := gorm.Open(mysql.Open(options.DSN), &gorm.Config{SkipDefaultTransaction: true, PrepareStmt: false, TranslateError: true, Logger: logger.New(log.New(io.Discard, "", 0), logger.Config{LogLevel: logger.Silent, ParameterizedQueries: true})})
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get mysql connection pool: %w", err)
	}
	if options.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(options.MaxOpenConns)
	}
	if options.MaxIdleConns >= 0 {
		sqlDB.SetMaxIdleConns(options.MaxIdleConns)
	}
	if options.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(options.ConnMaxLifetime)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	version := strings.TrimSpace(options.ProjectionVersion)
	if len(version) > 128 {
		sqlDB.Close()
		return nil, errors.New("memory projection version is too large")
	}
	return &Store{db: db, projectionVersion: version}, nil
}

func (s *Store) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (s *Store) CreateSession(ctx context.Context, value chat.Session) error {
	return s.db.WithContext(ctx).Create(&chatSessionRow{ID: value.ID, UserID: value.UserID, TenantID: value.TenantID, Title: value.Title, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}).Error
}

func (s *Store) ListSessions(ctx context.Context, owner chat.Owner, limit int) ([]chat.Session, error) {
	var rows []chatSessionRow
	if err := s.db.WithContext(ctx).Where("user_id=? AND tenant_id=?", owner.UserID, owner.TenantID).Order("updated_at DESC,id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]chat.Session, 0, len(rows))
	for _, r := range rows {
		values = append(values, chat.Session{ID: r.ID, UserID: r.UserID, TenantID: r.TenantID, Title: r.Title, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return values, nil
}

func (s *Store) GetSession(ctx context.Context, owner chat.Owner, id string) (chat.Session, error) {
	var row chatSessionRow
	err := s.db.WithContext(ctx).Where("id=? AND user_id=? AND tenant_id=?", id, owner.UserID, owner.TenantID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chat.Session{}, chat.ErrNotFound
	}
	return chat.Session{ID: row.ID, UserID: row.UserID, TenantID: row.TenantID, Title: row.Title, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, err
}

func (s *Store) ListMessages(ctx context.Context, owner chat.Owner, sessionID string, limit int) ([]chat.Message, error) {
	if _, err := s.GetSession(ctx, owner, sessionID); err != nil {
		return nil, err
	}
	var rows []chatMessageRow
	err := s.db.WithContext(ctx).Raw(`SELECT id,session_id,run_id,sequence,role,content,created_at FROM (SELECT id,session_id,run_id,sequence,role,content,created_at FROM chat_messages WHERE session_id=? ORDER BY sequence DESC LIMIT ?) recent ORDER BY sequence ASC`, sessionID, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	values := make([]chat.Message, 0, len(rows))
	for _, r := range rows {
		values = append(values, chat.Message{ID: r.ID, SessionID: r.SessionID, RunID: stringValue(r.RunID), Sequence: r.Sequence, Role: r.Role, Content: r.Content, CreatedAt: r.CreatedAt})
	}
	return values, nil
}

func (s *Store) CreateRun(ctx context.Context, input chat.CreateRunInput, run chat.Run, userMessage chat.Message, queued chat.Event) (result chat.CreatedRun, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session chatSessionRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND tenant_id=? AND status='active'", input.SessionID, input.Owner.UserID, input.Owner.TenantID).First(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return chat.ErrNotFound
		}
		if err != nil {
			return err
		}
		existing, loadErr := getRunByIdempotency(tx, input.SessionID, input.IdempotencyKey)
		if loadErr == nil {
			result = chat.CreatedRun{Run: existing, Created: false}
			return nil
		}
		if !errors.Is(loadErr, chat.ErrNotFound) {
			return loadErr
		}
		var active int64
		if err := tx.Model(&agentRunRow{}).Where("session_id=? AND status IN ?", input.SessionID, []string{"queued", "running"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return chat.ErrActiveRun
		}
		sequence, err := nextMessageSequence(tx, input.SessionID)
		if err != nil {
			return err
		}
		if err := tx.Create(&agentRunRow{ID: run.ID, SessionID: input.SessionID, Status: string(chat.RunQueued), ModelName: run.Model, IdempotencyKey: input.IdempotencyKey, LastEventSequence: 1, CreatedAt: run.CreatedAt}).Error; err != nil {
			return err
		}
		runID := run.ID
		if err := tx.Create(&chatMessageRow{ID: userMessage.ID, SessionID: input.SessionID, RunID: &runID, Sequence: sequence, Role: "user", Content: userMessage.Content, CreatedAt: userMessage.CreatedAt}).Error; err != nil {
			return err
		}
		if err := tx.Model(&chatSessionRow{}).Where("id=?", input.SessionID).Update("updated_at", userMessage.CreatedAt).Error; err != nil {
			return err
		}
		queued.Sequence = 1
		if err := insertEvent(tx, queued); err != nil {
			return err
		}
		result = chat.CreatedRun{Run: run, Created: true}
		return nil
	})
	return
}

func (s *Store) GetRun(ctx context.Context, owner chat.Owner, id string) (chat.Run, error) {
	var row agentRunRow
	result := s.db.WithContext(ctx).Table("agent_runs r").Select("r.*").Joins("JOIN chat_sessions cs ON cs.id=r.session_id").Where("r.id=? AND cs.user_id=? AND cs.tenant_id=?", id, owner.UserID, owner.TenantID).Scan(&row)
	if result.Error != nil {
		return chat.Run{}, result.Error
	}
	if result.RowsAffected == 0 {
		return chat.Run{}, chat.ErrNotFound
	}
	return chatRunFromRow(row), nil
}

func (s *Store) StartRun(ctx context.Context, runID string, event chat.Event) error {
	return s.updateRunWithEvent(ctx, runID, chat.RunQueued, event, func(tx *gorm.DB, seq int64) error {
		return tx.Model(&agentRunRow{}).Where("id=?", runID).Updates(map[string]any{"status": "running", "started_at": event.CreatedAt, "last_event_sequence": seq}).Error
	})
}

func (s *Store) AppendEvent(ctx context.Context, event chat.Event) (result chat.Event, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seq, status, err := lockRun(tx, event.RunID)
		if err != nil {
			return err
		}
		if status.Terminal() {
			return chat.ErrInvalidState
		}
		event.Sequence = seq + 1
		if err := tx.Model(&agentRunRow{}).Where("id=?", event.RunID).Update("last_event_sequence", event.Sequence).Error; err != nil {
			return err
		}
		if err := insertEvent(tx, event); err != nil {
			return err
		}
		result = event
		return nil
	})
	return
}

func (s *Store) CompleteRun(ctx context.Context, runID string, assistant chat.Message, event chat.Event) error {
	return s.updateRunWithEvent(ctx, runID, chat.RunRunning, event, func(tx *gorm.DB, seq int64) error {
		messageSeq, err := nextMessageSequence(tx, assistant.SessionID)
		if err != nil {
			return err
		}
		rid := runID
		if err := tx.Create(&chatMessageRow{ID: assistant.ID, SessionID: assistant.SessionID, RunID: &rid, Sequence: messageSeq, Role: "assistant", Content: assistant.Content, CreatedAt: assistant.CreatedAt}).Error; err != nil {
			return err
		}
		return tx.Model(&agentRunRow{}).Where("id=?", runID).Updates(map[string]any{"status": "completed", "completed_at": event.CreatedAt, "last_event_sequence": seq}).Error
	})
}

func (s *Store) FailRun(ctx context.Context, runID string, status chat.RunStatus, code, message string, event chat.Event) error {
	return s.updateNonTerminalRunWithEvent(ctx, runID, event, func(tx *gorm.DB, seq int64) error {
		return tx.Model(&agentRunRow{}).Where("id=?", runID).Updates(map[string]any{"status": status, "error_code": code, "error_message": message, "completed_at": event.CreatedAt, "last_event_sequence": seq}).Error
	})
}

func (s *Store) CancelRun(ctx context.Context, owner chat.Owner, runID string, event chat.Event) (chat.Run, error) {
	if _, err := s.GetRun(ctx, owner, runID); err != nil {
		return chat.Run{}, err
	}
	err := s.updateNonTerminalRunWithEvent(ctx, runID, event, func(tx *gorm.DB, seq int64) error {
		return tx.Model(&agentRunRow{}).Where("id=?", runID).Updates(map[string]any{"status": "cancelled", "completed_at": event.CreatedAt, "last_event_sequence": seq}).Error
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
	var rows []agentRunEventRow
	if err := s.db.WithContext(ctx).Where("run_id=? AND sequence>?", runID, after).Order("sequence ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]chat.Event, 0, len(rows))
	for _, r := range rows {
		var data map[string]any
		if err := json.Unmarshal(r.EventData, &data); err != nil {
			return nil, fmt.Errorf("decode event %d: %w", r.Sequence, err)
		}
		values = append(values, chat.Event{RunID: r.RunID, Sequence: r.Sequence, Type: r.EventType, Data: data, CreatedAt: r.CreatedAt})
	}
	return values, nil
}

func (s *Store) InterruptRunning(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []agentRunRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,last_event_sequence").Where("status IN ?", []string{"queued", "running"}).Find(&rows).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, r := range rows {
			event := chat.Event{RunID: r.ID, Sequence: r.LastEventSequence + 1, Type: "run.interrupted", Data: map[string]any{"status": chat.RunInterrupted}, CreatedAt: now}
			if err := tx.Model(&agentRunRow{}).Where("id=?", r.ID).Updates(map[string]any{"status": "interrupted", "completed_at": now, "last_event_sequence": event.Sequence}).Error; err != nil {
				return err
			}
			if err := insertEvent(tx, event); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) updateRunWithEvent(ctx context.Context, runID string, expected chat.RunStatus, event chat.Event, update func(*gorm.DB, int64) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		last, status, err := lockRun(tx, runID)
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
		return insertEvent(tx, event)
	})
}
func (s *Store) updateNonTerminalRunWithEvent(ctx context.Context, runID string, event chat.Event, update func(*gorm.DB, int64) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		last, status, err := lockRun(tx, runID)
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
		return insertEvent(tx, event)
	})
}

func (s *Store) getRun(ctx context.Context, id string) (chat.Run, error) {
	var row agentRunRow
	err := s.db.WithContext(ctx).Where("id=?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chat.Run{}, chat.ErrNotFound
	}
	return chatRunFromRow(row), err
}
func getRunByIdempotency(tx *gorm.DB, sessionID, key string) (chat.Run, error) {
	var row agentRunRow
	err := tx.Where("session_id=? AND idempotency_key=?", sessionID, key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chat.Run{}, chat.ErrNotFound
	}
	return chatRunFromRow(row), err
}
func lockRun(tx *gorm.DB, runID string) (int64, chat.RunStatus, error) {
	var row agentRunRow
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("last_event_sequence,status").Where("id=?", runID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, "", chat.ErrNotFound
	}
	return row.LastEventSequence, chat.RunStatus(row.Status), err
}
func insertEvent(tx *gorm.DB, event chat.Event) error {
	raw, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	return tx.Create(&agentRunEventRow{RunID: event.RunID, Sequence: event.Sequence, EventType: event.Type, EventData: raw, CreatedAt: event.CreatedAt}).Error
}
func nextMessageSequence(tx *gorm.DB, sessionID string) (int64, error) {
	var seq int64
	err := tx.Model(&chatMessageRow{}).Select("COALESCE(MAX(sequence),0)+1").Where("session_id=?", sessionID).Scan(&seq).Error
	return seq, err
}
func chatRunFromRow(r agentRunRow) chat.Run {
	return chat.Run{ID: r.ID, SessionID: r.SessionID, Status: chat.RunStatus(r.Status), Model: r.ModelName, IdempotencyKey: r.IdempotencyKey, ErrorCode: stringValue(r.ErrorCode), ErrorMessage: stringValue(r.ErrorMessage), CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt}
}
func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
