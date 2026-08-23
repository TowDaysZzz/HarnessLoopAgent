package memoryworkflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

var ErrInvalidEditPayload = errors.New("invalid memory edit payload")

const MaxEditTextBytes = 4096

type EditPayloadStore interface {
	PutMemoryEditPayload(context.Context, memory.Owner, string, Draft, time.Time, time.Time) error
	ConsumeMemoryEditPayload(context.Context, memory.Owner, string, time.Time) (Draft, error)
}

type EditPayloadService struct {
	Store     EditPayloadStore
	Extractor DraftExtractor
	TTL       time.Duration
	Now       func() time.Time
	NewID     func() string
}

func (s EditPayloadService) Create(ctx context.Context, owner memory.Owner, text string) (string, error) {
	if s.Store == nil || s.Extractor == nil || !owner.Valid() || s.TTL <= 0 || strings.TrimSpace(text) == "" || len(text) > MaxEditTextBytes {
		return "", ErrInvalidEditPayload
	}
	draft, err := s.Extractor.ExtractMemoryDraft(ctx, owner, text)
	if err != nil {
		return "", err
	}
	if err := draft.Normalize(); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	id := uuid.NewString()
	if s.NewID != nil {
		id = strings.TrimSpace(s.NewID())
	}
	if id == "" || len(id) > 191 {
		return "", ErrInvalidEditPayload
	}
	if err := s.Store.PutMemoryEditPayload(ctx, owner, id, draft, now.Add(s.TTL), now); err != nil {
		return "", err
	}
	return "memory-edit:" + id, nil
}

func (s EditPayloadService) LoadEditedMemoryDraft(ctx context.Context, owner memory.Owner, ref string) (Draft, error) {
	if s.Store == nil || !owner.Valid() || !strings.HasPrefix(ref, "memory-edit:") {
		return Draft{}, memory.ErrNotFound
	}
	id := strings.TrimPrefix(ref, "memory-edit:")
	if id == "" || len(id) > 191 {
		return Draft{}, memory.ErrNotFound
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	draft, err := s.Store.ConsumeMemoryEditPayload(ctx, owner, id, now)
	if err != nil {
		return Draft{}, err
	}
	if err := draft.Normalize(); err != nil {
		return Draft{}, err
	}
	return draft, nil
}
