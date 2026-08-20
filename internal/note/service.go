package note

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

var (
	ErrNotFound     = errors.New("note not found")
	ErrInvalidInput = errors.New("invalid note input")
	ErrConflict     = errors.New("note idempotency conflict")
)

const (
	eventCreate = "note.create"
	eventDelete = "note.delete"
)

type Repository interface {
	CreateNoteWithOutbox(context.Context, Note, string, OutboxEvent) (Note, bool, error)
	GetNote(context.Context, uint64, uint64, string) (Note, error)
	ListNotes(context.Context, uint64, uint64, int, string) ([]Note, error)
	QueueNoteDelete(context.Context, uint64, uint64, string, string, OutboxEvent) (Note, bool, error)
	ClaimNoteOutbox(context.Context, uint64, uint64, int) ([]OutboxEvent, error)
	CompleteNoteCreate(context.Context, OutboxEvent, uint64, uint64, string) error
	UpdateNoteJobStatus(context.Context, uint64, uint64, string, string, string, string) error
	CompleteNoteDelete(context.Context, OutboxEvent) error
	FailNoteProjection(context.Context, OutboxEvent, string, time.Time) error
}

type RAGClient interface {
	CreateNote(context.Context, ragclient.CreateNoteRequest) (*ragclient.CreateNoteResponse, error)
	GetNoteJob(context.Context, uint64) (*ragclient.NoteJobResponse, error)
	DeleteNote(context.Context, uint64, string) (*ragclient.DeleteNoteResponse, error)
}

type Service struct {
	repository Repository
	rag        RAGClient
	kbID       uint64
	now        func() time.Time
}

func NewService(repository Repository, rag RAGClient, kbID uint64) (*Service, error) {
	if repository == nil || rag == nil || kbID == 0 {
		return nil, errors.New("note repository, RAG client and personal KB ID are required")
	}
	return &Service{repository: repository, rag: rag, kbID: kbID, now: time.Now}, nil
}

func (s *Service) Create(ctx context.Context, principal auth.Principal, input CreateInput) (Note, bool, error) {
	if err := validatePrincipal(principal); err != nil {
		return Note{}, false, err
	}
	input.Title = strings.Join(strings.Fields(input.Title), " ")
	input.Content = strings.TrimSpace(input.Content)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.Title == "" || input.Content == "" || input.IdempotencyKey == "" || utf8.RuneCountInString(input.Title) > 200 || len(input.Content) > 1024*1024 {
		return Note{}, false, ErrInvalidInput
	}
	tags := normalizeTags(input.Tags)
	tagJSON, _ := json.Marshal(tags)
	hash := sha256.Sum256([]byte(input.Title + "\x00" + input.Content + "\x00" + string(tagJSON)))
	now := s.now().UTC()
	noteID := uuid.NewString()
	note := Note{
		ID: noteID, UserID: principal.UserID, TenantID: principal.TenantID,
		ExternalNoteID: "note-" + strings.ReplaceAll(noteID, "-", ""), Title: input.Title, Content: input.Content,
		Tags: tags, OccurredAt: input.OccurredAt, Status: StatusIndexing, RAGKBID: s.kbID,
		ContentHash: hex.EncodeToString(hash[:]), CreatedAt: now, UpdatedAt: now,
	}
	event := OutboxEvent{ID: uuid.NewString(), NoteID: note.ID, UserID: principal.UserID, TenantID: principal.TenantID, EventType: eventCreate, CreatedAt: now, AvailableAt: now}
	created, replayed, err := s.repository.CreateNoteWithOutbox(ctx, note, input.IdempotencyKey, event)
	return created, replayed, err
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, noteID string) (Note, error) {
	if err := validatePrincipal(principal); err != nil {
		return Note{}, err
	}
	return s.repository.GetNote(ctx, principal.UserID, principal.TenantID, strings.TrimSpace(noteID))
}

func (s *Service) RefreshStatus(ctx context.Context, principal auth.Principal, noteID string) (Note, error) {
	if err := validatePrincipal(principal); err != nil || principal.AccessToken == "" {
		return Note{}, auth.ErrUnauthenticated
	}
	value, err := s.Get(ctx, principal, noteID)
	if err != nil || value.RAGJobID == 0 || (value.Status != StatusIndexing && value.Status != StatusIndexFailed) {
		return value, err
	}
	job, err := s.rag.GetNoteJob(ragclient.WithUserAccessToken(ctx, principal.AccessToken), value.RAGJobID)
	if err != nil {
		return value, err
	}
	status := StatusIndexing
	switch job.Status {
	case "completed", "success", "succeeded":
		status = StatusIndexed
	case "failed", "error":
		status = StatusIndexFailed
	}
	if err := s.repository.UpdateNoteJobStatus(ctx, principal.UserID, principal.TenantID, noteID, string(status), job.Status, job.ErrorDetail); err != nil {
		return Note{}, err
	}
	value.Status, value.RAGStatus, value.LastError = status, job.Status, job.ErrorDetail
	return value, nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal, limit int, cursor string) ([]Note, error) {
	if err := validatePrincipal(principal); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return s.repository.ListNotes(ctx, principal.UserID, principal.TenantID, limit, strings.TrimSpace(cursor))
}

func (s *Service) Delete(ctx context.Context, principal auth.Principal, noteID, idempotencyKey string) (Note, bool, error) {
	if err := validatePrincipal(principal); err != nil {
		return Note{}, false, err
	}
	now := s.now().UTC()
	event := OutboxEvent{ID: uuid.NewString(), NoteID: noteID, UserID: principal.UserID, TenantID: principal.TenantID, EventType: eventDelete, CreatedAt: now, AvailableAt: now}
	return s.repository.QueueNoteDelete(ctx, principal.UserID, principal.TenantID, strings.TrimSpace(noteID), strings.TrimSpace(idempotencyKey), event)
}

// ProjectPending processes a small, user-scoped batch. The caller supplies the current
// access token so credentials are never persisted in the outbox or exposed to the model.
func (s *Service) ProjectPending(ctx context.Context, principal auth.Principal, limit int) error {
	if err := validatePrincipal(principal); err != nil || principal.AccessToken == "" {
		return auth.ErrUnauthenticated
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	events, err := s.repository.ClaimNoteOutbox(ctx, principal.UserID, principal.TenantID, limit)
	if err != nil {
		return err
	}
	ctx = ragclient.WithUserAccessToken(ctx, principal.AccessToken)
	for _, event := range events {
		if err := s.project(ctx, principal, event); err != nil {
			delay := time.Duration(1<<min(event.Attempt, 6)) * time.Second
			_ = s.repository.FailNoteProjection(ctx, event, truncate(err.Error(), 1000), s.now().UTC().Add(delay))
		}
	}
	return nil
}

func (s *Service) project(ctx context.Context, principal auth.Principal, event OutboxEvent) error {
	note, err := s.repository.GetNote(ctx, principal.UserID, principal.TenantID, event.NoteID)
	if err != nil {
		return err
	}
	switch event.EventType {
	case eventCreate:
		created, err := s.rag.CreateNote(ctx, ragclient.CreateNoteRequest{
			KBID: note.RAGKBID, ExternalNoteID: note.ExternalNoteID, Title: note.Title,
			Content: note.Content, Tags: note.Tags, OccurredAt: formatTime(note.OccurredAt),
		})
		if err != nil {
			return err
		}
		return s.repository.CompleteNoteCreate(ctx, event, created.DocumentID, created.JobID, created.Status)
	case eventDelete:
		if note.RAGDocumentID == 0 {
			return s.repository.CompleteNoteDelete(ctx, event)
		}
		deleted, err := s.rag.DeleteNote(ctx, note.RAGDocumentID, note.ExternalNoteID)
		if err != nil {
			return err
		}
		if !deleted.Deleted {
			return errors.New("RAG did not confirm note deletion")
		}
		return s.repository.CompleteNoteDelete(ctx, event)
	default:
		return fmt.Errorf("unsupported note outbox event %q", event.EventType)
	}
}

func validatePrincipal(principal auth.Principal) error {
	if principal.UserID == 0 || principal.TenantID == 0 {
		return auth.ErrUnauthenticated
	}
	return nil
}

func normalizeTags(input []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, min(len(input), 20))
	for _, raw := range input {
		tag := strings.Join(strings.Fields(raw), " ")
		if tag == "" || utf8.RuneCountInString(tag) > 64 {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, tag)
		if len(result) == 20 {
			break
		}
	}
	return result
}

func formatTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
