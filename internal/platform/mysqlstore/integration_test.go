package mysqlstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
)

func TestIntegrationRepositoryLifecycle(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run against a disposable MySQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := chat.Owner{UserID: 7, TenantID: 9}
	session := chat.Session{ID: uuid.NewString(), UserID: owner.UserID, TenantID: owner.TenantID, Title: "integration", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	run := chat.Run{ID: uuid.NewString(), SessionID: session.ID, Status: chat.RunQueued, Model: "test", IdempotencyKey: "integration-key", CreatedAt: now}
	input := chat.CreateRunInput{SessionID: session.ID, Owner: owner, Content: "hello", Model: "test", IdempotencyKey: run.IdempotencyKey}
	created, err := store.CreateRun(ctx, input, run, chat.Message{ID: uuid.NewString(), SessionID: session.ID, RunID: run.ID, Role: "user", Content: input.Content, CreatedAt: now}, chat.Event{RunID: run.ID, Type: "run.queued", Data: map[string]any{"status": chat.RunQueued}, CreatedAt: now})
	if err != nil || !created.Created {
		t.Fatalf("CreateRun() = %#v, %v", created, err)
	}
	replay, err := store.CreateRun(ctx, input, chat.Run{ID: uuid.NewString()}, chat.Message{}, chat.Event{})
	if err != nil || replay.Created || replay.Run.ID != run.ID {
		t.Fatalf("idempotent CreateRun() = %#v, %v", replay, err)
	}
	if err := store.StartRun(ctx, run.ID, chat.Event{RunID: run.ID, Type: "run.started", Data: map[string]any{"status": chat.RunRunning}, CreatedAt: now}); err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if _, err := store.AppendEvent(ctx, chat.Event{RunID: run.ID, Type: "text.delta", Data: map[string]any{"content": "answer"}, CreatedAt: now}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := store.CompleteRun(ctx, run.ID, chat.Message{ID: uuid.NewString(), SessionID: session.ID, RunID: run.ID, Role: "assistant", Content: "answer", CreatedAt: now}, chat.Event{RunID: run.ID, Type: "run.completed", Data: map[string]any{"status": chat.RunCompleted}, CreatedAt: now}); err != nil {
		t.Fatalf("CompleteRun() error = %v", err)
	}
	stored, err := store.GetRun(ctx, owner, run.ID)
	if err != nil || stored.Status != chat.RunCompleted {
		t.Fatalf("GetRun() = %#v, %v", stored, err)
	}
	events, err := store.ListEvents(ctx, owner, run.ID, 1, 100)
	if err != nil || len(events) != 3 || events[0].Sequence != 2 || events[2].Type != "run.completed" {
		t.Fatalf("ListEvents() = %#v, %v", events, err)
	}
	messages, err := store.ListMessages(ctx, owner, session.ID, 100)
	if err != nil || len(messages) != 2 || messages[1].Content != "answer" {
		t.Fatalf("ListMessages() = %#v, %v", messages, err)
	}
}
