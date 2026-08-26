package mysqlstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/dailyreview"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/google/uuid"
)

type mysqlReviewRunner struct {
	output string
	calls  int
}

func (r *mysqlReviewRunner) StreamConversation(_ context.Context, _ agent.ConversationRequest) <-chan agent.Event {
	r.calls++
	out := make(chan agent.Event, 2)
	out <- agent.Event{Type: agent.EventTextDelta, Delta: r.output}
	out <- agent.Event{Type: agent.EventRunCompleted}
	close(out)
	return out
}

func TestDailyActivityMySQLCrossSessionPinnedAndSkillExclusion(t *testing.T) {
	store, ctx := openGORMRepositoryStore(t)
	window, _ := dailyreview.ResolveWindow(time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02"), "Asia/Shanghai")
	at := window.Start.Add(time.Hour).UTC()
	suffix := uint64(time.Now().UnixNano()%500000000) + 4000000000
	owner := chat.Owner{TenantID: suffix, UserID: suffix + 1}
	skillOwner := skill.Owner{TenantID: owner.TenantID, UserID: owner.UserID}
	createSession := func(title string) chat.Session {
		s := chat.Session{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, Title: title, Status: "active", CreatedAt: at, UpdatedAt: at}
		if err := store.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	createCompleted := func(session chat.Session, content, answer string, created time.Time) (chat.Run, chat.Message) {
		run := chat.Run{ID: uuid.NewString(), SessionID: session.ID, Status: chat.RunQueued, Model: "test", IdempotencyKey: uuid.NewString(), CreatedAt: created}
		user := chat.Message{ID: uuid.NewString(), SessionID: session.ID, RunID: run.ID, Role: "user", Content: content, CreatedAt: created}
		got, err := store.CreateRun(ctx, chat.CreateRunInput{SessionID: session.ID, Owner: owner, Content: content, Model: "test", IdempotencyKey: run.IdempotencyKey}, run, user, chat.Event{RunID: run.ID, Type: "run.queued", Data: map[string]any{"status": "queued"}, CreatedAt: created})
		if err != nil || !got.Created {
			t.Fatalf("create run=%#v err=%v", got, err)
		}
		if err := store.StartRun(ctx, run.ID, chat.Event{RunID: run.ID, Type: "run.started", Data: map[string]any{"status": "running"}, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
		assistant := chat.Message{ID: uuid.NewString(), SessionID: session.ID, RunID: run.ID, Role: "assistant", Content: answer, CreatedAt: created.Add(time.Microsecond)}
		if err := store.CompleteRun(ctx, run.ID, assistant, chat.Event{RunID: run.ID, Type: "run.completed", Data: map[string]any{"status": "completed"}, CreatedAt: created.Add(time.Microsecond)}); err != nil {
			t.Fatal(err)
		}
		return run, user
	}
	first, second := createSession("one"), createSession("two")
	createCompleted(first, "one", "answer-one", at)
	createCompleted(second, "two", "answer-two", at.Add(time.Minute))
	dailyRuns := map[string]bool{}
	firstDailyRunID := ""
	for index := 0; index < 3; index++ {
		dailyRun, _ := createCompleted(first, "回顾今天", "daily output", at.Add(time.Duration(index+2)*time.Minute))
		if firstDailyRunID == "" {
			firstDailyRunID = dailyRun.ID
		}
		dailyRuns[dailyRun.ID] = true
		invocation, err := skill.NewInvocation(uuid.NewString(), skillOwner, first.ID, dailyRun.ID, skill.Ref{ID: "daily_review", Version: "v1"}, []byte(`{"date":"2026-08-24"}`), at)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CreateInvocation(ctx, invocation); err != nil {
			t.Fatal(err)
		}
	}
	noteBody := "daily note"
	occurred := at.Add(3 * time.Minute)
	noteValue := note.Note{ID: uuid.NewString(), UserID: owner.UserID, TenantID: owner.TenantID, ExternalNoteID: uuid.NewString(), Title: "note", Content: noteBody, Tags: []string{}, OccurredAt: &occurred, Status: note.StatusIndexed, RAGKBID: 1, ContentHash: dailyreview.ContentHash(noteBody), CreatedAt: at, UpdatedAt: at}
	event := note.OutboxEvent{ID: uuid.NewString(), NoteID: noteValue.ID, UserID: owner.UserID, TenantID: owner.TenantID, EventType: "note.create", CreatedAt: at, AvailableAt: at}
	if _, _, err := store.CreateNoteWithOutbox(ctx, noteValue, uuid.NewString(), event); err != nil {
		t.Fatal(err)
	}
	chatRefs, truncated, err := store.SnapshotChat(ctx, skillOwner, window, 20, 10)
	if err != nil || truncated || len(chatRefs) != 4 {
		t.Fatalf("chat refs=%#v truncated=%v err=%v", chatRefs, truncated, err)
	}
	for _, ref := range chatRefs {
		if dailyRuns[ref.RunID] {
			t.Fatalf("daily review polluted snapshot: %#v", ref)
		}
	}
	if _, err := store.LoadChatPinned(ctx, skillOwner, chatRefs); err != nil {
		t.Fatal(err)
	}
	noteRefs, _, err := store.SnapshotNotes(ctx, skillOwner, window, 10)
	if err != nil || len(noteRefs) != 1 {
		t.Fatalf("note refs=%#v err=%v", noteRefs, err)
	}
	if _, err := store.LoadNotesPinned(ctx, skillOwner, noteRefs); err != nil {
		t.Fatal(err)
	}
	memoryOwner := memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID}
	canonical, structured, memoryHash, err := memory.NormalizeContent("完成年度项目", memory.StructuredValue{Schema: "goal", Version: 1, Data: map[string]any{"goal": "annual"}})
	if err != nil {
		t.Fatal(err)
	}
	memoryValue := memory.Record{ID: uuid.NewString(), Owner: memoryOwner, Layer: memory.LayerLongTerm, Kind: memory.KindGoal, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "goals", SlotKey: "annual", LineageID: uuid.NewString(), LineageVersion: 1, RowVersion: 1, CanonicalText: canonical, StructuredValue: structured, ContentHash: memoryHash, Authority: memory.AuthorityUserConfirmed, Confidence: 1, Salience: 1, Source: memory.SourceRef{Type: "workflow", ID: "mysql-review"}, Status: memory.StatusActive, CreatedAt: at, UpdatedAt: at}
	if _, err := store.CommitMutation(ctx, memory.Mutation{Owner: memoryOwner, NewMemory: &memoryValue, IdempotencyKey: uuid.NewString(), InputHash: memoryHash, OccurredAt: at}); err != nil {
		t.Fatal(err)
	}
	recall, err := memory.NewRecallService(store, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 3, MaxTarget: 10, PageSize: 10, MaxScanned: 20, MaxBatches: 2, MaxDuration: time.Second, MaxContextChars: 4096, PlanMinConfidence: .8, MaxExactCandidates: 10})
	if err != nil {
		t.Fatal(err)
	}
	memoryAdapter := dailyreview.RecallAdapter{Service: recall, Candidates: store, Target: 3, MaxContextChars: 4096}
	memoryContext, err := memoryAdapter.Recall(ctx, skillOwner, window)
	if err != nil || len(memoryContext.Refs) != 1 {
		t.Fatalf("memory context=%#v err=%v", memoryContext, err)
	}
	report := dailyreview.EmptyReport(window, nil)
	report.Highlights = []dailyreview.Fact{{Text: "跨会话活动与目标", Evidence: []dailyreview.EvidenceRef{{Type: "chat", ID: chatRefs[0].ID, Version: uint64(chatRefs[0].Sequence), Hash: chatRefs[0].ContentHash}, {Type: "note", ID: noteRefs[0].ID, Version: noteRefs[0].Version, Hash: noteRefs[0].ContentHash}, {Type: "memory", ID: memoryContext.Refs[0].ID, Version: memoryContext.Refs[0].LineageVersion, Hash: memoryContext.Refs[0].ContentHash}}}}
	raw, _ := json.Marshal(report)
	runner := &mysqlReviewRunner{output: string(raw)}
	review := dailyreview.ReviewWorkflow{Reader: dailyreview.ActivityReader{Chat: store, Notes: store, Memory: store}, Cache: store, Memory: memoryAdapter, Generator: dailyreview.StructuredGenerator{Runner: runner, MaxRepairs: 0, MaxOutputBytes: 64 * 1024, Timeout: time.Second}, Config: dailyreview.WorkflowConfig{Options: dailyreview.Options{MaxChatMessages: 20, PerSession: 10, MaxNotes: 10, IncludeMemory: true}, CacheTTL: time.Hour, CacheLease: time.Minute, CacheWait: time.Second, MaxSteps: 9, MaxModelCalls: 1, MaxToolCalls: 2, OutputSchemaVersion: "v1", PromptPolicyVersion: "v1"}, Now: func() time.Time { return at.Add(10 * time.Minute) }}
	reviewInvocation, err := skill.NewInvocation("mysql-review-1", skillOwner, first.ID, firstDailyRunID, skill.Ref{ID: "daily_review", Version: "v1"}, []byte(`{"date":"`+window.LocalDate+`","timezone":"Asia/Shanghai"}`), at)
	if err != nil {
		t.Fatal(err)
	}
	request := skill.Request{Invocation: reviewInvocation, Content: "回顾今天"}
	generated, err := review.Run(ctx, request)
	if err != nil || generated.CacheState != "miss" {
		t.Fatalf("generated=%#v err=%v", generated, err)
	}
	request.Invocation.ID = "mysql-review-2"
	cached, err := review.Run(ctx, request)
	if err != nil || cached.CacheState != "hit" || cached.Text != generated.Text || runner.calls != 1 {
		t.Fatalf("cached=%#v calls=%d err=%v", cached, runner.calls, err)
	}
	createCompleted(second, "new activity", "new answer", at.Add(20*time.Minute))
	runner.output = string(raw)
	request.Invocation.ID = "mysql-review-3"
	invalidated, err := review.Run(ctx, request)
	if err != nil || invalidated.CacheState != "miss" || runner.calls != 2 {
		t.Fatalf("invalidated=%#v calls=%d err=%v", invalidated, runner.calls, err)
	}
	if refs, _, err := store.SnapshotChat(ctx, skill.Owner{TenantID: owner.TenantID, UserID: owner.UserID + 99}, window, 20, 10); err != nil || len(refs) != 0 {
		t.Fatalf("cross owner refs=%#v err=%v", refs, err)
	}
	deleteEvent := note.OutboxEvent{ID: uuid.NewString(), NoteID: noteValue.ID, UserID: owner.UserID, TenantID: owner.TenantID, EventType: "note.delete", CreatedAt: at.Add(time.Hour), AvailableAt: at.Add(time.Hour)}
	if _, _, err := store.QueueNoteDelete(ctx, owner.UserID, owner.TenantID, noteValue.ID, uuid.NewString(), deleteEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadNotesPinned(ctx, skillOwner, noteRefs); !errors.Is(err, dailyreview.ErrStaleSnapshot) {
		t.Fatalf("stale note err=%v", err)
	}
}
