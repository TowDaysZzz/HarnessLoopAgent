package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Layer string

const (
	LayerShortTerm Layer = "short_term"
	LayerSession   Layer = "session"
	LayerLongTerm  Layer = "long_term"
)

type Item struct {
	ID         string    `json:"id"`
	UserID     uint64    `json:"user_id"`
	TenantID   uint64    `json:"tenant_id"`
	Layer      Layer     `json:"layer"`
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	Status     string    `json:"status"`
	Confidence float64   `json:"confidence"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Store interface {
	Put(ctx context.Context, item Item) error
	List(ctx context.Context, userID, tenantID uint64, layer Layer) ([]Item, error)
	Delete(ctx context.Context, userID, tenantID uint64, id string) error
}

type CandidateService struct{ store Store }

func NewCandidateService(store Store) (*CandidateService, error) {
	if store == nil {
		return nil, errors.New("memory store is required")
	}
	return &CandidateService{store: store}, nil
}

func (s *CandidateService) Propose(ctx context.Context, item Item) error {
	if item.Layer != LayerLongTerm || strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Value) == "" {
		return errors.New("long-term memory candidate requires key and value")
	}
	item.Status = "candidate"
	return s.store.Put(ctx, item)
}

func (s *CandidateService) Confirm(ctx context.Context, item Item) error {
	if item.Status != "candidate" {
		return errors.New("only candidate memory can be confirmed")
	}
	item.Status = "active"
	return s.store.Put(ctx, item)
}

func (s *CandidateService) Reject(ctx context.Context, item Item) error {
	if item.Status != "candidate" {
		return errors.New("only candidate memory can be rejected")
	}
	item.Status = "rejected"
	return s.store.Put(ctx, item)
}
