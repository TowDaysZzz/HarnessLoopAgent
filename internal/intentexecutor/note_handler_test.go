package intentexecutor

import (
	"context"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
)

type noteCreatorFake struct {
	calls  int
	inputs []note.CreateInput
	value  note.Note
}

type projectorFake struct{ called chan struct{} }

func (f projectorFake) ProjectPending(context.Context, auth.Principal, int) error {
	select {
	case f.called <- struct{}{}:
	default:
	}
	return nil
}

func (f *noteCreatorFake) Create(_ context.Context, _ auth.Principal, input note.CreateInput) (note.Note, bool, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	if f.value.ID == "" {
		f.value = note.Note{ID: "note-1", Status: note.StatusIndexing, Title: input.Title, Content: input.Content, RAGKBID: 5}
	}
	return f.value, f.calls > 1, nil
}

type summarizerFake struct{ calls int }

func (f *summarizerFake) Summarize(context.Context, []agent.Message) (string, string, error) {
	f.calls++
	return "Go GC", "Go GC uses concurrent marking.", nil
}

func TestDirectNoteCreateUsesNoteService(t *testing.T) {
	notes := &noteCreatorFake{}
	projected := make(chan struct{}, 1)
	handler := NoteCreateHandler{Notes: notes, Projector: projectorFake{called: projected}}
	result, err := handler.Execute(context.Background(), routing.Input{
		Run:     routing.RunContext{RunID: "run-1", UserID: 1, TenantID: 2, Decision: routing.RouteDecision{Intent: routing.IntentNoteCreate, Reason: "explicit_note_write"}},
		Content: "帮我记住：Go GC 默认 GOGC=100",
	})
	if err != nil || notes.calls != 1 || notes.inputs[0].Content != "Go GC 默认 GOGC=100" || result.Text == "" {
		t.Fatalf("Execute() result=%#v calls=%d inputs=%#v err=%v", result, notes.calls, notes.inputs, err)
	}
	if notes.inputs[0].IdempotencyKey != "chat-run:run-1" || notes.value.RAGKBID != 5 {
		t.Fatalf("create input/value = %#v %#v", notes.inputs[0], notes.value)
	}
	select {
	case <-projected:
	case <-time.After(time.Second):
		t.Fatal("note projection was not dispatched")
	}
}

func TestHistorySummaryCreatesDraftWithoutFormalNote(t *testing.T) {
	notes := &noteCreatorFake{}
	drafts, _ := notedraft.NewService(notedraft.NewMemoryRepository(), 24*time.Hour)
	summarizer := &summarizerFake{}
	handler := NoteCreateHandler{Notes: notes, Drafts: drafts, Summarizer: summarizer}
	result, err := handler.Execute(context.Background(), routing.Input{
		Run:      routing.RunContext{RunID: "run-1", SessionID: "session-1", UserID: 1, TenantID: 2, Decision: routing.RouteDecision{Intent: routing.IntentNoteCreate, Reason: "explicit_note_write", NeedsModel: true}},
		Content:  "总结刚才的讨论，帮我记成一条笔记",
		Messages: []agent.Message{{Role: "user", Content: "谈谈 GC"}, {Role: "assistant", Content: "concurrent mark"}, {Role: "user", Content: "总结刚才"}},
	})
	if err != nil || notes.calls != 0 || summarizer.calls != 1 || !result.NeedConfirm || result.Candidate == nil {
		t.Fatalf("Execute() = %#v, note calls=%d summary calls=%d err=%v", result, notes.calls, summarizer.calls, err)
	}
	if _, err := drafts.Latest(context.Background(), notedraft.Owner{UserID: 1, TenantID: 2}, "session-1"); err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestConfirmCancelAndModifyDraft(t *testing.T) {
	notes := &noteCreatorFake{}
	drafts, _ := notedraft.NewService(notedraft.NewMemoryRepository(), 24*time.Hour)
	owner := notedraft.Owner{UserID: 1, TenantID: 2}
	original, _ := drafts.Create(context.Background(), owner, "session-1", "Old", "old content")
	projected := make(chan struct{}, 1)
	handler := NoteCreateHandler{Notes: notes, Projector: projectorFake{called: projected}, Drafts: drafts}
	modified, err := handler.Execute(context.Background(), routing.Input{Run: routing.RunContext{SessionID: "session-1", UserID: 1, TenantID: 2, Decision: routing.RouteDecision{Reason: "draft_modify"}}, Content: "修改为：new content"})
	if err != nil || modified.Candidate == nil || modified.Candidate.ID == original.ID || notes.calls != 0 {
		t.Fatalf("modify = %#v, note calls=%d err=%v", modified, notes.calls, err)
	}
	confirmed, err := handler.Execute(context.Background(), routing.Input{Run: routing.RunContext{SessionID: "session-1", UserID: 1, TenantID: 2, Decision: routing.RouteDecision{Reason: "draft_confirm"}}})
	if err != nil || confirmed.Text == "" || notes.calls != 1 || notes.inputs[0].Content != "new content" {
		t.Fatalf("confirm = %#v, inputs=%#v err=%v", confirmed, notes.inputs, err)
	}
	select {
	case <-projected:
	case <-time.After(time.Second):
		t.Fatal("confirmed draft projection was not dispatched")
	}
	other, _ := drafts.Create(context.Background(), owner, "session-1", "Other", "cancel me")
	cancelled, err := handler.Execute(context.Background(), routing.Input{Run: routing.RunContext{SessionID: "session-1", UserID: 1, TenantID: 2, Decision: routing.RouteDecision{Reason: "draft_cancel"}}})
	if err != nil || cancelled.Text == "" || notes.calls != 1 {
		t.Fatalf("cancel = %#v, note calls=%d err=%v", cancelled, notes.calls, err)
	}
	if _, _, err := drafts.Confirm(context.Background(), owner, "session-1", other.ID, other.ContentHash); err == nil {
		t.Fatal("cancelled draft was confirmable")
	}
}

func TestRouterOnlyAcceptsDraftActionInOwningSession(t *testing.T) {
	drafts, _ := notedraft.NewService(notedraft.NewMemoryRepository(), 24*time.Hour)
	_, _ = drafts.Create(context.Background(), notedraft.Owner{UserID: 1, TenantID: 2}, "session-1", "Title", "Content")
	router := routing.Router{Classifier: routing.Classifier{}, Drafts: drafts}
	accepted := router.Route(context.Background(), routing.RouteInput{UserID: 1, TenantID: 2, SessionID: "session-1", Content: "确认保存"})
	if accepted.Intent != routing.IntentNoteCreate || accepted.Reason != "draft_confirm" {
		t.Fatalf("accepted route = %#v", accepted)
	}
	for _, input := range []routing.RouteInput{
		{UserID: 1, TenantID: 2, SessionID: "session-2", Content: "确认保存"},
		{UserID: 3, TenantID: 2, SessionID: "session-1", Content: "确认保存"},
	} {
		got := router.Route(context.Background(), input)
		if got.Intent != routing.IntentUnclear || got.Reason != "missing_pending_draft" {
			t.Fatalf("cross-scope route = %#v", got)
		}
	}
}
