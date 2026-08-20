package knowledgebase

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

var ErrNotConfigured = errors.New("personal knowledge base is not configured")

type Binding struct {
	UserID    uint64    `json:"-"`
	TenantID  uint64    `json:"-"`
	RAGKBID   uint64    `json:"kb_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Repository interface {
	GetKnowledgeBaseBinding(context.Context, uint64, uint64) (Binding, error)
	UpsertKnowledgeBaseBinding(context.Context, Binding) error
}

type RAGClient interface {
	ListKnowledgeBases(context.Context) (*ragclient.KnowledgeBaseList, error)
	CreateKnowledgeBase(context.Context, ragclient.CreateKnowledgeBaseRequest) (*ragclient.KnowledgeBase, error)
}

type Service struct {
	repository Repository
	rag        RAGClient
	now        func() time.Time
}

func NewService(repository Repository, rag RAGClient) (*Service, error) {
	if repository == nil || rag == nil {
		return nil, errors.New("knowledge base repository and RAG client are required")
	}
	return &Service{repository: repository, rag: rag, now: time.Now}, nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal) (Binding, error) {
	if principal.UserID == 0 || principal.TenantID == 0 {
		return Binding{}, auth.ErrUnauthenticated
	}
	return s.repository.GetKnowledgeBaseBinding(ctx, principal.UserID, principal.TenantID)
}

func (s *Service) ResolveKnowledgeBase(ctx context.Context, principal auth.Principal) (uint64, error) {
	binding, err := s.Get(ctx, principal)
	if err != nil {
		return 0, err
	}
	return binding.RAGKBID, nil
}

func (s *Service) Ensure(ctx context.Context, principal auth.Principal, name string) (Binding, bool, error) {
	if principal.UserID == 0 || principal.TenantID == 0 || strings.TrimSpace(principal.AccessToken) == "" {
		return Binding{}, false, auth.ErrUnauthenticated
	}
	if binding, err := s.Get(ctx, principal); err == nil {
		return binding, false, nil
	} else if !errors.Is(err, ErrNotConfigured) {
		return Binding{}, false, err
	}

	userCtx := ragclient.WithUserAccessToken(ctx, principal.AccessToken)
	listed, err := s.rag.ListKnowledgeBases(userCtx)
	if err != nil {
		return Binding{}, false, err
	}
	created := false
	var kb ragclient.KnowledgeBase
	if len(listed.Items) > 0 {
		kb = listed.Items[0]
	} else {
		name = strings.Join(strings.Fields(name), " ")
		if name == "" {
			name = "我的笔记"
		}
		value, createErr := s.rag.CreateKnowledgeBase(userCtx, ragclient.CreateKnowledgeBaseRequest{
			Name: name, Description: "Note Agent 个人笔记知识库",
		})
		if createErr != nil {
			return Binding{}, false, createErr
		}
		kb, created = *value, true
	}
	if kb.ID == 0 || kb.TenantID != principal.TenantID || kb.UserID != principal.UserID {
		return Binding{}, false, errors.New("RAG returned a knowledge base outside the current user scope")
	}
	now := s.now().UTC()
	binding := Binding{UserID: principal.UserID, TenantID: principal.TenantID, RAGKBID: kb.ID, Name: kb.Name, Status: kb.Status, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.UpsertKnowledgeBaseBinding(ctx, binding); err != nil {
		return Binding{}, false, err
	}
	return binding, created, nil
}
