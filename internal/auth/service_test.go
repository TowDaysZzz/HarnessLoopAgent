package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type authRepositoryFake struct{ session Session }

func (f *authRepositoryFake) CreateAuthSession(_ context.Context, session Session) error {
	f.session = session
	return nil
}
func (f *authRepositoryFake) GetAuthSession(_ context.Context, id string) (Session, error) {
	if id != f.session.ID {
		return Session{}, ErrUnauthenticated
	}
	return f.session, nil
}
func (f *authRepositoryFake) UpdateAuthSessionTokens(_ context.Context, session Session) error {
	f.session = session
	return nil
}
func (f *authRepositoryFake) DeleteAuthSession(_ context.Context, _ string) error { return nil }

type authRAGFake struct{}

func (authRAGFake) Register(context.Context, ragclient.RegisterRequest) (*ragclient.RegisterResponse, error) {
	return &ragclient.RegisterResponse{UserID: 3, TenantID: 4}, nil
}
func (authRAGFake) Login(context.Context, ragclient.LoginRequest) (*ragclient.TokenResponse, error) {
	return &ragclient.TokenResponse{AccessToken: "plain-access", RefreshToken: "plain-refresh", ExpiresIn: 3600, UserID: 3, TenantID: 4, Role: "owner"}, nil
}
func (authRAGFake) Refresh(context.Context, string) (*ragclient.TokenResponse, error) {
	return &ragclient.TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600, UserID: 3, TenantID: 4, Role: "owner"}, nil
}
func (authRAGFake) Me(context.Context) (*ragclient.User, error) {
	return &ragclient.User{UserID: 3, TenantID: 4, Email: "user@example.com", Name: "User", Role: "owner"}, nil
}

func TestLoginStoresHashedSessionIDAndEncryptedTokens(t *testing.T) {
	repository := &authRepositoryFake{}
	service, err := NewService(repository, authRAGFake{}, strings.Repeat("s", 32), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rawID, principal, err := service.Login(context.Background(), ragclient.LoginRequest{Email: "user@example.com", Password: "password"})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if rawID == repository.session.ID || repository.session.ID != hashSessionID(rawID) {
		t.Fatal("database session ID must be a hash of the cookie value")
	}
	if strings.Contains(repository.session.EncryptedAccessToken, "plain-access") || strings.Contains(repository.session.EncryptedRefreshToken, "plain-refresh") {
		t.Fatal("RAG tokens must not be stored in plaintext")
	}
	if principal.AccessToken != "plain-access" || principal.UserID != 3 || principal.TenantID != 4 {
		t.Fatalf("principal = %#v", principal)
	}
	resolved, err := service.Resolve(context.Background(), rawID)
	if err != nil || resolved.AccessToken != "plain-access" {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
}
