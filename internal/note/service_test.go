package note

import (
	"context"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type noteRepositoryFake struct {
	note   Note
	events []OutboxEvent
}

func (f *noteRepositoryFake) CreateNoteWithOutbox(_ context.Context, value Note, _ string, event OutboxEvent) (Note, bool, error) {
	f.note, f.events = value, append(f.events, event)
	return value, false, nil
}
func (f *noteRepositoryFake) GetNote(_ context.Context, userID, tenantID uint64, id string) (Note, error) {
	if f.note.ID != id || f.note.UserID != userID || f.note.TenantID != tenantID {
		return Note{}, ErrNotFound
	}
	return f.note, nil
}
func (f *noteRepositoryFake) ListNotes(context.Context, uint64, uint64, int, string) ([]Note, error) {
	return []Note{f.note}, nil
}
func (f *noteRepositoryFake) QueueNoteDelete(_ context.Context, _, _ uint64, _ string, _ string, event OutboxEvent) (Note, bool, error) {
	f.note.Status = StatusDeletePending
	f.events = append(f.events, event)
	return f.note, false, nil
}
func (f *noteRepositoryFake) ClaimNoteOutbox(context.Context, uint64, uint64, int) ([]OutboxEvent, error) {
	events := f.events
	f.events = nil
	return events, nil
}
func (f *noteRepositoryFake) CompleteNoteCreate(_ context.Context, _ OutboxEvent, documentID, jobID uint64, status string) error {
	f.note.RAGDocumentID, f.note.RAGJobID, f.note.RAGStatus, f.note.Status = documentID, jobID, status, StatusIndexing
	return nil
}
func (f *noteRepositoryFake) UpdateNoteJobStatus(_ context.Context, _, _ uint64, _ string, status, ragStatus, lastError string) error {
	f.note.Status, f.note.RAGStatus, f.note.LastError = Status(status), ragStatus, lastError
	return nil
}
func (f *noteRepositoryFake) CompleteNoteDelete(context.Context, OutboxEvent) error {
	f.note.Status = StatusDeleted
	return nil
}
func (f *noteRepositoryFake) FailNoteProjection(context.Context, OutboxEvent, string, time.Time) error {
	f.note.Status = StatusIndexFailed
	return nil
}

type noteRAGFake struct {
	jobStatus   string
	deleteCalls int
}

type noteKBResolver uint64

func (r noteKBResolver) ResolveKnowledgeBase(context.Context, auth.Principal) (uint64, error) {
	return uint64(r), nil
}

func (f *noteRAGFake) CreateNote(context.Context, ragclient.CreateNoteRequest) (*ragclient.CreateNoteResponse, error) {
	return &ragclient.CreateNoteResponse{DocumentID: 10, JobID: 20, ExternalNoteID: "external", Status: "pending"}, nil
}
func (f *noteRAGFake) GetNoteJob(context.Context, uint64) (*ragclient.NoteJobResponse, error) {
	return &ragclient.NoteJobResponse{JobID: 20, DocumentID: 10, Status: f.jobStatus}, nil
}
func (f *noteRAGFake) DeleteNote(context.Context, uint64, string) (*ragclient.DeleteNoteResponse, error) {
	f.deleteCalls++
	return &ragclient.DeleteNoteResponse{DocumentID: 10, Deleted: true}, nil
}

func TestCreateProjectsPendingAndOnlyJobCompletionMarksIndexed(t *testing.T) {
	repository := &noteRepositoryFake{}
	rag := &noteRAGFake{jobStatus: "completed"}
	service, err := NewService(repository, rag, 5)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{UserID: 3, TenantID: 4, Role: "owner", AccessToken: "jwt"}
	created, replayed, err := service.Create(context.Background(), principal, CreateInput{Title: "Go GC", Content: "concurrent mark", IdempotencyKey: "request-1"})
	if err != nil || replayed || created.Status != StatusIndexing || repository.note.Content != "concurrent mark" {
		t.Fatalf("Create() = %#v, replayed=%v, err=%v", created, replayed, err)
	}
	if err := service.ProjectPending(context.Background(), principal, 5); err != nil {
		t.Fatalf("ProjectPending() error = %v", err)
	}
	if repository.note.Status != StatusIndexing || repository.note.RAGJobID != 20 {
		t.Fatalf("pending projection = %#v", repository.note)
	}
	refreshed, err := service.RefreshStatus(context.Background(), principal, created.ID)
	if err != nil || refreshed.Status != StatusIndexed {
		t.Fatalf("RefreshStatus() = %#v, %v", refreshed, err)
	}
}

func TestCreateUsesPersonalKnowledgeBaseBinding(t *testing.T) {
	repository := &noteRepositoryFake{}
	service, err := NewServiceWithResolver(repository, &noteRAGFake{}, noteKBResolver(9))
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := service.Create(context.Background(), auth.Principal{UserID: 3, TenantID: 4}, CreateInput{Title: "note", Content: "content", IdempotencyKey: "key"})
	if err != nil || created.RAGKBID != 9 {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
}

func TestDeleteProjectsToRAGBeforeMarkingDeleted(t *testing.T) {
	repository := &noteRepositoryFake{note: Note{ID: "note-1", UserID: 3, TenantID: 4, RAGKBID: 9, RAGDocumentID: 10, Status: StatusIndexed}}
	rag := &noteRAGFake{}
	service, err := NewService(repository, rag, 9)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{UserID: 3, TenantID: 4, AccessToken: "jwt"}
	queued, replayed, err := service.Delete(context.Background(), principal, "note-1", "delete-1")
	if err != nil || replayed || queued.Status != StatusDeletePending || repository.note.Status != StatusDeletePending {
		t.Fatalf("Delete() = %#v, replayed=%v, err=%v", queued, replayed, err)
	}
	if rag.deleteCalls != 0 {
		t.Fatal("RAG deletion ran before outbox projection")
	}
	if err := service.ProjectPending(context.Background(), principal, 5); err != nil {
		t.Fatalf("ProjectPending() error = %v", err)
	}
	if rag.deleteCalls != 1 || repository.note.Status != StatusDeleted {
		t.Fatalf("deleteCalls=%d note=%#v", rag.deleteCalls, repository.note)
	}
}
