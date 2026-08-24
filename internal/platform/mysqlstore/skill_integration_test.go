package mysqlstore_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/google/uuid"
)

func TestSkillInvocationMySQLLifecycle(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run skill invocation integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := chat.Owner{TenantID: 901, UserID: 902}
	session := chat.Session{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, Title: "skill", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: uuid.NewString(), SessionID: session.ID, Status: chat.RunQueued, Model: "test", IdempotencyKey: uuid.NewString(), CreatedAt: now}
	created, err := store.CreateRun(ctx, chat.CreateRunInput{SessionID: session.ID, Owner: owner, Content: "回顾今天", Model: "test", IdempotencyKey: run.IdempotencyKey}, run, chat.Message{ID: uuid.NewString(), SessionID: session.ID, RunID: run.ID, Role: "user", Content: "回顾今天", CreatedAt: now}, chat.Event{RunID: run.ID, Type: "run.queued", Data: map[string]any{"status": "queued"}, CreatedAt: now})
	if err != nil || !created.Created {
		t.Fatalf("create run=%#v err=%v", created, err)
	}
	invocation, err := skill.NewInvocation(uuid.NewString(), skill.Owner{TenantID: owner.TenantID, UserID: owner.UserID}, session.ID, run.ID, skill.Ref{ID: "daily_review", Version: "v1"}, []byte(`{"date":"2026-08-24"}`), now)
	if err != nil {
		t.Fatal(err)
	}
	stored, first, err := store.CreateInvocation(ctx, invocation)
	if err != nil || !first {
		t.Fatalf("create invocation=%#v created=%v err=%v", stored, first, err)
	}
	replayed, first, err := store.CreateInvocation(ctx, invocation)
	if err != nil || first || replayed.ID != invocation.ID {
		t.Fatalf("replay=%#v created=%v err=%v", replayed, first, err)
	}
	stored, err = store.TransitionInvocation(ctx, invocation.Owner, invocation.ID, skill.InvocationPending, skill.InvocationRunning, "", now.Add(time.Second))
	if err != nil || stored.Status != skill.InvocationRunning {
		t.Fatalf("running=%#v err=%v", stored, err)
	}
	stored, err = store.TransitionInvocation(ctx, invocation.Owner, invocation.ID, skill.InvocationRunning, skill.InvocationCompleted, "", now.Add(2*time.Second))
	if err != nil || stored.Status != skill.InvocationCompleted {
		t.Fatalf("completed=%#v err=%v", stored, err)
	}
	if _, err := store.GetInvocation(ctx, skill.Owner{TenantID: owner.TenantID, UserID: owner.UserID + 1}, invocation.ID); !errors.Is(err, skill.ErrNotFound) {
		t.Fatalf("cross owner error=%v", err)
	}
}
