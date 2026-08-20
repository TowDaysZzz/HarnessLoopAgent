package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrInvalidInput    = errors.New("invalid authentication input")
)

type Session struct {
	ID                    string
	UserID                uint64
	TenantID              uint64
	Role                  string
	Email                 string
	Name                  string
	EncryptedAccessToken  string
	EncryptedRefreshToken string
	AccessExpiresAt       time.Time
	ExpiresAt             time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Repository interface {
	CreateAuthSession(context.Context, Session) error
	GetAuthSession(context.Context, string) (Session, error)
	UpdateAuthSessionTokens(context.Context, Session) error
	DeleteAuthSession(context.Context, string) error
}

type RAGClient interface {
	Register(context.Context, ragclient.RegisterRequest) (*ragclient.RegisterResponse, error)
	Login(context.Context, ragclient.LoginRequest) (*ragclient.TokenResponse, error)
	Refresh(context.Context, string) (*ragclient.TokenResponse, error)
	Me(context.Context) (*ragclient.User, error)
}

type Service struct {
	repository Repository
	rag        RAGClient
	aead       cipher.AEAD
	sessionTTL time.Duration
	now        func() time.Time
}

func NewService(repository Repository, rag RAGClient, secret string, sessionTTL time.Duration) (*Service, error) {
	if repository == nil || rag == nil {
		return nil, errors.New("auth repository and RAG client are required")
	}
	if strings.TrimSpace(secret) == "" || sessionTTL <= 0 {
		return nil, errors.New("auth session secret and positive TTL are required")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{repository: repository, rag: rag, aead: aead, sessionTTL: sessionTTL, now: time.Now}, nil
}

func (s *Service) Register(ctx context.Context, request ragclient.RegisterRequest) (string, Principal, error) {
	if _, err := s.rag.Register(ctx, request); err != nil {
		return "", Principal{}, err
	}
	return s.Login(ctx, ragclient.LoginRequest{Email: request.Email, Password: request.Password})
}

func (s *Service) Login(ctx context.Context, request ragclient.LoginRequest) (string, Principal, error) {
	if strings.TrimSpace(request.Email) == "" || request.Password == "" {
		return "", Principal{}, ErrInvalidInput
	}
	tokens, err := s.rag.Login(ctx, request)
	if err != nil {
		return "", Principal{}, err
	}
	return s.createSession(ctx, request.Email, tokens)
}

func (s *Service) Resolve(ctx context.Context, rawSessionID string) (Principal, error) {
	hashed := hashSessionID(rawSessionID)
	if hashed == "" {
		return Principal{}, ErrUnauthenticated
	}
	session, err := s.repository.GetAuthSession(ctx, hashed)
	if err != nil || !session.ExpiresAt.After(s.now()) {
		return Principal{}, ErrUnauthenticated
	}
	if !session.AccessExpiresAt.After(s.now().Add(30 * time.Second)) {
		if err := s.refresh(ctx, &session); err != nil {
			return Principal{}, ErrUnauthenticated
		}
	}
	accessToken, err := s.decrypt(session.EncryptedAccessToken)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{UserID: session.UserID, TenantID: session.TenantID, Role: session.Role, Email: session.Email, Name: session.Name, AccessToken: accessToken}, nil
}

func (s *Service) Refresh(ctx context.Context, rawSessionID string) (Principal, error) {
	session, err := s.repository.GetAuthSession(ctx, hashSessionID(rawSessionID))
	if err != nil || !session.ExpiresAt.After(s.now()) {
		return Principal{}, ErrUnauthenticated
	}
	if err := s.refresh(ctx, &session); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	accessToken, err := s.decrypt(session.EncryptedAccessToken)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{UserID: session.UserID, TenantID: session.TenantID, Role: session.Role, Email: session.Email, Name: session.Name, AccessToken: accessToken}, nil
}

func (s *Service) Logout(ctx context.Context, rawSessionID string) error {
	if hashed := hashSessionID(rawSessionID); hashed != "" {
		return s.repository.DeleteAuthSession(ctx, hashed)
	}
	return nil
}

func (s *Service) createSession(ctx context.Context, email string, tokens *ragclient.TokenResponse) (string, Principal, error) {
	if tokens == nil || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.UserID == 0 || tokens.TenantID == 0 {
		return "", Principal{}, errors.New("RAG returned an incomplete login response")
	}
	userCtx := ragclient.WithUserAccessToken(ctx, tokens.AccessToken)
	user, err := s.rag.Me(userCtx)
	if err != nil {
		return "", Principal{}, err
	}
	rawID := uuid.NewString() + uuid.NewString()
	now := s.now().UTC()
	session := Session{
		ID: hashSessionID(rawID), UserID: tokens.UserID, TenantID: tokens.TenantID, Role: tokens.Role,
		Email: firstNonEmpty(user.Email, email), Name: user.Name,
		AccessExpiresAt: now.Add(time.Duration(tokens.ExpiresIn) * time.Second), ExpiresAt: now.Add(s.sessionTTL), CreatedAt: now, UpdatedAt: now,
	}
	if session.EncryptedAccessToken, err = s.encrypt(tokens.AccessToken); err != nil {
		return "", Principal{}, err
	}
	if session.EncryptedRefreshToken, err = s.encrypt(tokens.RefreshToken); err != nil {
		return "", Principal{}, err
	}
	if err := s.repository.CreateAuthSession(ctx, session); err != nil {
		return "", Principal{}, err
	}
	return rawID, Principal{UserID: session.UserID, TenantID: session.TenantID, Role: session.Role, Email: session.Email, Name: session.Name, AccessToken: tokens.AccessToken}, nil
}

func (s *Service) refresh(ctx context.Context, session *Session) error {
	refreshToken, err := s.decrypt(session.EncryptedRefreshToken)
	if err != nil {
		return err
	}
	tokens, err := s.rag.Refresh(ctx, refreshToken)
	if err != nil {
		return err
	}
	if tokens.UserID != session.UserID || tokens.TenantID != session.TenantID {
		return errors.New("refreshed token identity mismatch")
	}
	session.EncryptedAccessToken, err = s.encrypt(tokens.AccessToken)
	if err != nil {
		return err
	}
	session.EncryptedRefreshToken, err = s.encrypt(tokens.RefreshToken)
	if err != nil {
		return err
	}
	session.Role = tokens.Role
	session.AccessExpiresAt = s.now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	session.UpdatedAt = s.now().UTC()
	return s.repository.UpdateAuthSessionTokens(ctx, *session)
}

func (s *Service) encrypt(value string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate token nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Service) decrypt(value string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("invalid encrypted token")
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("decrypt token")
	}
	return string(plain), nil
}

func hashSessionID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
