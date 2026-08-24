package mysqlstore_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/google/uuid"
)

func openGORMRepositoryStore(t *testing.T) (*mysqlstore.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to run GORM repository integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 16, MaxIdleConns: 4, ConnMaxLifetime: time.Minute, ProjectionVersion: "gorm-integration-v1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func TestGORMAuthKnowledgeBaseDraftEditPayloadAndOutboxConcurrency(t *testing.T) {
	store, ctx := openGORMRepositoryStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := uint64(now.UnixNano()%500000000) + 3000000000
	userID, tenantID := suffix, suffix+1
	authValue := auth.Session{ID: "gorm-auth-" + uuid.NewString(), UserID: userID, TenantID: tenantID, Role: "user", Email: uuid.NewString() + "@example.com", Name: "gorm", EncryptedAccessToken: "cipher-a", EncryptedRefreshToken: "cipher-r", AccessExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := store.CreateAuthSession(ctx, authValue); err != nil {
		t.Fatal(err)
	}
	gotAuth, err := store.GetAuthSession(ctx, authValue.ID)
	if err != nil || gotAuth.EncryptedAccessToken != "cipher-a" {
		t.Fatalf("auth=%+v err=%v", gotAuth, err)
	}
	authValue.Role = ""
	authValue.EncryptedAccessToken = ""
	authValue.UpdatedAt = now.Add(time.Second)
	if err := store.UpdateAuthSessionTokens(ctx, authValue); err != nil {
		t.Fatal(err)
	}
	gotAuth, err = store.GetAuthSession(ctx, authValue.ID)
	if err != nil || gotAuth.Role != "" || gotAuth.EncryptedAccessToken != "" {
		t.Fatalf("auth zero update=%+v err=%v", gotAuth, err)
	}
	kb := knowledgebase.Binding{UserID: userID, TenantID: tenantID, RAGKBID: suffix + 2, Name: "kb", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertKnowledgeBaseBinding(ctx, kb); err != nil {
		t.Fatal(err)
	}
	kb.Name = "renamed"
	kb.Status = ""
	kb.UpdatedAt = now.Add(time.Second)
	if err := store.UpsertKnowledgeBaseBinding(ctx, kb); err != nil {
		t.Fatal(err)
	}
	gotKB, err := store.GetKnowledgeBaseBinding(ctx, userID, tenantID)
	if err != nil || gotKB.Name != "renamed" || gotKB.Status != "" {
		t.Fatalf("kb=%+v err=%v", gotKB, err)
	}
	owner := chat.Owner{UserID: userID, TenantID: tenantID}
	session := chat.Session{ID: uuid.NewString(), UserID: userID, TenantID: tenantID, Title: "gorm", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	draft := notedraft.Draft{ID: uuid.NewString(), UserID: userID, TenantID: tenantID, SessionID: session.ID, Title: "draft", Content: "body", Status: notedraft.StatusPending, ContentHash: stringsOf('d', 64), ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := store.ReplacePending(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Transition(ctx, notedraft.Owner{UserID: userID, TenantID: tenantID}, session.ID, draft.ID, draft.ContentHash, notedraft.StatusConfirmed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	canonical, structured, hash, err := memory.NormalizeContent("tea", memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"drink": "tea"}})
	if err != nil {
		t.Fatal(err)
	}
	editDraft := memoryworkflow.Draft{Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", SlotKey: "drink", CanonicalText: canonical, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserStated, Confidence: 1, Salience: .5, Source: memory.SourceRef{Type: "workflow", ID: "gorm"}}
	memoryOwner := memory.Owner{TenantID: tenantID, UserID: userID}
	payloadID := "edit-" + uuid.NewString()
	if err := store.PutMemoryEditPayload(ctx, memoryOwner, payloadID, editDraft, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	consumeErrs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ConsumeMemoryEditPayload(context.Background(), memoryOwner, payloadID, now.Add(time.Second))
			consumeErrs <- err
		}()
	}
	wg.Wait()
	close(consumeErrs)
	success, notFound := 0, 0
	for err := range consumeErrs {
		if err == nil {
			success++
		} else if errors.Is(err, memory.ErrNotFound) {
			notFound++
		} else {
			t.Fatalf("consume err=%v", err)
		}
	}
	if success != 1 || notFound != 1 {
		t.Fatalf("consume success=%d notFound=%d", success, notFound)
	}
	noteValue := note.Note{ID: uuid.NewString(), UserID: userID, TenantID: tenantID, ExternalNoteID: "external-" + uuid.NewString(), Title: "note", Content: "content", Tags: []string{}, Status: note.StatusDraft, RAGKBID: kb.RAGKBID, RAGStatus: "", ContentHash: stringsOf('e', 64), CreatedAt: now, UpdatedAt: now}
	event := note.OutboxEvent{ID: uuid.NewString(), NoteID: noteValue.ID, UserID: userID, TenantID: tenantID, EventType: "note.create", AvailableAt: now, CreatedAt: now}
	if _, _, err := store.CreateNoteWithOutbox(ctx, noteValue, "note-"+uuid.NewString(), event); err != nil {
		t.Fatal(err)
	}
	claims := make(chan []note.OutboxEvent, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events, err := store.ClaimNoteOutbox(context.Background(), userID, tenantID, 1)
			claims <- events
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	claimed := 0
	for events := range claims {
		claimed += len(events)
	}
	if claimed != 1 {
		t.Fatalf("claimed outbox=%d", claimed)
	}
	if _, err := store.GetSession(ctx, owner, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAuthSession(ctx, authValue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAuthSession(ctx, authValue.ID); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("deleted auth err=%v", err)
	}
}

func TestGORMConcurrentCreateRunKeepsSingleActiveGuard(t *testing.T) {
	store, ctx := openGORMRepositoryStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := uint64(now.UnixNano()%500000000) + 4000000000
	owner := chat.Owner{UserID: base, TenantID: base + 1}
	session := chat.Session{ID: uuid.NewString(), UserID: owner.UserID, TenantID: owner.TenantID, Title: "concurrent", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runID := uuid.NewString()
			_, err := store.CreateRun(context.Background(), chat.CreateRunInput{SessionID: session.ID, Owner: owner, Content: "hello", Model: "test", IdempotencyKey: uuid.NewString()}, chat.Run{ID: runID, SessionID: session.ID, Status: chat.RunQueued, Model: "test", IdempotencyKey: uuid.NewString(), CreatedAt: now}, chat.Message{ID: uuid.NewString(), SessionID: session.ID, RunID: runID, Role: "user", Content: "hello", CreatedAt: now}, chat.Event{RunID: runID, Type: "run.queued", Data: map[string]any{"status": chat.RunQueued}, CreatedAt: now})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful active runs=%d", success)
	}
}
