package knowledgebase

import (
	"context"
	"testing"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type bindingRepository struct{ value *Binding }

func (r *bindingRepository) GetKnowledgeBaseBinding(context.Context, uint64, uint64) (Binding, error) {
	if r.value == nil {
		return Binding{}, ErrNotConfigured
	}
	return *r.value, nil
}

func (r *bindingRepository) UpsertKnowledgeBaseBinding(_ context.Context, value Binding) error {
	r.value = &value
	return nil
}

type knowledgeBaseRAG struct {
	items       []ragclient.KnowledgeBase
	createCalls int
}

func (r *knowledgeBaseRAG) ListKnowledgeBases(context.Context) (*ragclient.KnowledgeBaseList, error) {
	return &ragclient.KnowledgeBaseList{Items: append([]ragclient.KnowledgeBase(nil), r.items...)}, nil
}

func (r *knowledgeBaseRAG) CreateKnowledgeBase(_ context.Context, request ragclient.CreateKnowledgeBaseRequest) (*ragclient.KnowledgeBase, error) {
	r.createCalls++
	return &ragclient.KnowledgeBase{ID: 9, UserID: 3, TenantID: 4, Name: request.Name, Status: "active"}, nil
}

func TestEnsureCreatesAndPersistsOneKnowledgeBase(t *testing.T) {
	repository := &bindingRepository{}
	rag := &knowledgeBaseRAG{}
	service, err := NewService(repository, rag)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	principal := auth.Principal{UserID: 3, TenantID: 4, AccessToken: "jwt"}
	first, created, err := service.Ensure(context.Background(), principal, "我的笔记")
	if err != nil || !created || first.RAGKBID != 9 {
		t.Fatalf("first Ensure() = %#v, %v, %v", first, created, err)
	}
	second, created, err := service.Ensure(context.Background(), principal, "其他名称")
	if err != nil || created || second.RAGKBID != first.RAGKBID || rag.createCalls != 1 {
		t.Fatalf("idempotent Ensure() = %#v, %v, calls=%d, err=%v", second, created, rag.createCalls, err)
	}
}

func TestEnsureBindsExistingPersonalKnowledgeBase(t *testing.T) {
	repository := &bindingRepository{}
	rag := &knowledgeBaseRAG{items: []ragclient.KnowledgeBase{{ID: 7, UserID: 3, TenantID: 4, Name: "已有笔记", Status: "active"}}}
	service, _ := NewService(repository, rag)
	binding, created, err := service.Ensure(context.Background(), auth.Principal{UserID: 3, TenantID: 4, AccessToken: "jwt"}, "new")
	if err != nil || created || binding.RAGKBID != 7 || rag.createCalls != 0 {
		t.Fatalf("Ensure() = %#v, %v, calls=%d, err=%v", binding, created, rag.createCalls, err)
	}
}

func TestEnsureRejectsKnowledgeBaseOutsideUserScope(t *testing.T) {
	repository := &bindingRepository{}
	rag := &knowledgeBaseRAG{items: []ragclient.KnowledgeBase{{ID: 7, UserID: 99, TenantID: 4, Name: "wrong"}}}
	service, _ := NewService(repository, rag)
	if _, _, err := service.Ensure(context.Background(), auth.Principal{UserID: 3, TenantID: 4, AccessToken: "jwt"}, "new"); err == nil {
		t.Fatal("Ensure() accepted another user's knowledge base")
	}
}
