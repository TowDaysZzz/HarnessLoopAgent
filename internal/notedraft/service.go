package notedraft

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("note draft not found")
	ErrInvalidInput = errors.New("invalid note draft input")
	ErrInvalidState = errors.New("invalid note draft state")
	ErrExpired      = errors.New("note draft expired")
)

type Repository interface {
	ReplacePending(context.Context, Draft) error
	LatestPending(context.Context, Owner, string) (Draft, error)
	Transition(context.Context, Owner, string, string, string, Status, time.Time) (Draft, bool, error)
}

type Service struct {
	repository Repository
	ttl        time.Duration
	now        func() time.Time
}

func NewService(repository Repository, ttl time.Duration) (*Service, error) {
	if repository == nil || ttl <= 0 {
		return nil, errors.New("note draft repository and positive TTL are required")
	}
	return &Service{repository: repository, ttl: ttl, now: time.Now}, nil
}

func (s *Service) Create(ctx context.Context, owner Owner, sessionID, title, content string) (Draft, error) {
	title = strings.Join(strings.Fields(title), " ")
	content = strings.TrimSpace(content)
	sessionID = strings.TrimSpace(sessionID)
	if !validOwner(owner) || sessionID == "" || title == "" || content == "" || utf8.RuneCountInString(title) > 200 || len(content) > 1024*1024 {
		return Draft{}, ErrInvalidInput
	}
	now := s.now().UTC()
	draft := Draft{
		ID: uuid.NewString(), UserID: owner.UserID, TenantID: owner.TenantID, SessionID: sessionID,
		Title: title, Content: content, Status: StatusPending, ContentHash: hashContent(title, content),
		ExpiresAt: now.Add(s.ttl), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.ReplacePending(ctx, draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *Service) Latest(ctx context.Context, owner Owner, sessionID string) (Draft, error) {
	if !validOwner(owner) || strings.TrimSpace(sessionID) == "" {
		return Draft{}, ErrInvalidInput
	}
	draft, err := s.repository.LatestPending(ctx, owner, sessionID)
	if err != nil {
		return Draft{}, err
	}
	if !draft.ExpiresAt.After(s.now().UTC()) {
		_, _, _ = s.repository.Transition(ctx, owner, sessionID, draft.ID, draft.ContentHash, StatusExpired, s.now().UTC())
		return Draft{}, ErrExpired
	}
	return draft, nil
}

func (s *Service) HasPending(ctx context.Context, userID, tenantID uint64, sessionID string) (bool, error) {
	_, err := s.Latest(ctx, Owner{UserID: userID, TenantID: tenantID}, sessionID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrExpired) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) Confirm(ctx context.Context, owner Owner, sessionID, id, contentHash string) (Draft, bool, error) {
	return s.transition(ctx, owner, sessionID, id, contentHash, StatusConfirmed)
}

func (s *Service) Cancel(ctx context.Context, owner Owner, sessionID, id, contentHash string) (Draft, bool, error) {
	return s.transition(ctx, owner, sessionID, id, contentHash, StatusCancelled)
}

func (s *Service) transition(ctx context.Context, owner Owner, sessionID, id, contentHash string, status Status) (Draft, bool, error) {
	if !validOwner(owner) || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(id) == "" || strings.TrimSpace(contentHash) == "" {
		return Draft{}, false, ErrInvalidInput
	}
	latest, err := s.Latest(ctx, owner, sessionID)
	if err != nil {
		return Draft{}, false, err
	}
	if latest.ID != id || latest.ContentHash != contentHash {
		return Draft{}, false, ErrInvalidState
	}
	return s.repository.Transition(ctx, owner, sessionID, id, contentHash, status, s.now().UTC())
}

func hashContent(title, content string) string {
	hash := sha256.Sum256([]byte(title + "\x00" + content))
	return hex.EncodeToString(hash[:])
}

func validOwner(owner Owner) bool { return owner.UserID > 0 && owner.TenantID > 0 }
