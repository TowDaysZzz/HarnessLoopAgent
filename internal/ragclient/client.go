package ragclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	appclient "github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const defaultMaxResponseBytes = 2 << 20

type ClientConfig struct {
	BaseURL          string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
}

type Retriever interface {
	Retrieve(ctx context.Context, request RetrieveRequest) (*RetrieveResponse, error)
}

type doer interface {
	Do(ctx context.Context, request *protocol.Request, response *protocol.Response) error
}

type Client struct {
	baseURL          string
	apiKey           string
	timeout          time.Duration
	maxResponseBytes int
	doer             doer
}

type TraceHeaders struct {
	RequestID  string
	AgentRunID string
	ToolCallID string
}

type traceHeadersKey struct{}
type accessTokenKey struct{}
type knowledgeBaseIDsKey struct{}

func WithTraceHeaders(ctx context.Context, headers TraceHeaders) context.Context {
	return context.WithValue(ctx, traceHeadersKey{}, headers)
}

// WithUserAccessToken attaches a server-side user credential to an outbound RAG call.
// It must only be called after the Agent authentication middleware resolved the user session.
func WithUserAccessToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, accessTokenKey{}, strings.TrimSpace(token))
}

func WithKnowledgeBaseIDs(ctx context.Context, ids []uint64) context.Context {
	return context.WithValue(ctx, knowledgeBaseIDsKey{}, append([]uint64(nil), ids...))
}

func KnowledgeBaseIDsFromContext(ctx context.Context) []uint64 {
	ids, _ := ctx.Value(knowledgeBaseIDsKey{}).([]uint64)
	return append([]uint64(nil), ids...)
}

func NewClient(config ClientConfig) (*Client, error) {
	hertzClient, err := appclient.NewClient()
	if err != nil {
		return nil, fmt.Errorf("create Hertz RAG client: %w", err)
	}
	return newClient(config, hertzClient)
}

func newClient(config ClientConfig, doer doer) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("RAG base URL must be an absolute HTTP(S) URL")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("RAG timeout must be greater than zero")
	}
	maxBytes := config.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxResponseBytes
	}
	if maxBytes < 1 {
		return nil, errors.New("RAG max response bytes must be greater than zero")
	}
	return &Client{
		baseURL:          baseURL,
		apiKey:           strings.TrimSpace(config.APIKey),
		timeout:          config.Timeout,
		maxResponseBytes: maxBytes,
		doer:             doer,
	}, nil
}

func (c *Client) Retrieve(ctx context.Context, request RetrieveRequest) (*RetrieveResponse, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return nil, errors.New("RAG query is required")
	}
	if len(request.KBIDs) == 0 {
		return nil, errors.New("at least one RAG knowledge base ID is required")
	}
	if request.TopK < 1 || request.TopK > 20 {
		return nil, errors.New("RAG top_k must be between 1 and 20")
	}
	var result RetrieveResponse
	if err := c.doJSON(ctx, consts.MethodPost, "/v1/retrieve", request, "", &result); err != nil {
		return nil, fmt.Errorf("retrieve from RAG: %w", err)
	}
	if result.Items == nil {
		result.Items = []RetrieveItem{}
	}
	return &result, nil
}

func (c *Client) ListKnowledgeBases(ctx context.Context) (*KnowledgeBaseList, error) {
	var result KnowledgeBaseList
	if err := c.doJSON(ctx, consts.MethodGet, "/api/kb/bases?page=1&page_size=100", nil, "user", &result); err != nil {
		return nil, fmt.Errorf("list RAG knowledge bases: %w", err)
	}
	if result.Items == nil {
		result.Items = []KnowledgeBase{}
	}
	return &result, nil
}

func (c *Client) CreateKnowledgeBase(ctx context.Context, request CreateKnowledgeBaseRequest) (*KnowledgeBase, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Description = strings.TrimSpace(request.Description)
	if request.Name == "" {
		return nil, errors.New("knowledge base name is required")
	}
	var result KnowledgeBase
	if err := c.doJSON(ctx, consts.MethodPost, "/api/kb/bases", request, "user", &result); err != nil {
		return nil, fmt.Errorf("create RAG knowledge base: %w", err)
	}
	return &result, nil
}

func (c *Client) Register(ctx context.Context, request RegisterRequest) (*RegisterResponse, error) {
	var result RegisterResponse
	if err := c.doJSON(ctx, consts.MethodPost, "/v1/auth/register", request, "none", &result); err != nil {
		return nil, fmt.Errorf("register RAG user: %w", err)
	}
	return &result, nil
}

func (c *Client) Login(ctx context.Context, request LoginRequest) (*TokenResponse, error) {
	var result TokenResponse
	if err := c.doJSON(ctx, consts.MethodPost, "/v1/auth/login", request, "none", &result); err != nil {
		return nil, fmt.Errorf("login RAG user: %w", err)
	}
	return &result, nil
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	var result TokenResponse
	if err := c.doJSON(ctx, consts.MethodPost, "/v1/auth/refresh", RefreshRequest{RefreshToken: strings.TrimSpace(refreshToken)}, "none", &result); err != nil {
		return nil, fmt.Errorf("refresh RAG user token: %w", err)
	}
	return &result, nil
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	var result User
	if err := c.doJSON(ctx, consts.MethodGet, "/v1/auth/me", nil, "user", &result); err != nil {
		return nil, fmt.Errorf("get RAG user: %w", err)
	}
	return &result, nil
}

func (c *Client) CreateNote(ctx context.Context, request CreateNoteRequest) (*CreateNoteResponse, error) {
	request.ExternalNoteID = strings.TrimSpace(request.ExternalNoteID)
	if request.KBID == 0 || request.ExternalNoteID == "" || strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Content) == "" {
		return nil, errors.New("RAG note requires kb_id, external_note_id, title and content")
	}
	var result CreateNoteResponse
	if err := c.doJSON(ctx, consts.MethodPost, "/v1/notes", request, "user", &result, request.ExternalNoteID); err != nil {
		return nil, fmt.Errorf("create RAG note: %w", err)
	}
	return &result, nil
}

func (c *Client) GetNoteJob(ctx context.Context, jobID uint64) (*NoteJobResponse, error) {
	if jobID == 0 {
		return nil, errors.New("RAG note job ID is required")
	}
	var result NoteJobResponse
	if err := c.doJSON(ctx, consts.MethodGet, fmt.Sprintf("/v1/notes/jobs/%d", jobID), nil, "user", &result); err != nil {
		return nil, fmt.Errorf("get RAG note job: %w", err)
	}
	return &result, nil
}

func (c *Client) DeleteNote(ctx context.Context, documentID uint64, idempotencyKey string) (*DeleteNoteResponse, error) {
	if documentID == 0 {
		return nil, errors.New("RAG document ID is required")
	}
	var result DeleteNoteResponse
	if err := c.doJSON(ctx, consts.MethodDelete, fmt.Sprintf("/v1/notes/%d", documentID), nil, "user", &result, strings.TrimSpace(idempotencyKey)); err != nil {
		return nil, fmt.Errorf("delete RAG note: %w", err)
	}
	return &result, nil
}

type rawEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, authMode string, result any, idempotencyKey ...string) error {
	req := protocol.AcquireRequest()
	resp := protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)
	req.SetRequestURI(c.baseURL + path)
	req.Header.SetMethod(method)
	req.Header.SetContentTypeBytes([]byte("application/json"))
	if authMode != "none" {
		token := c.apiKey
		if userToken, _ := ctx.Value(accessTokenKey{}).(string); userToken != "" {
			token = userToken
		}
		if authMode == "user" && userTokenFromContext(ctx) == "" {
			return errors.New("user access token is required")
		}
		if token == "" {
			return errors.New("RAG authorization token is required")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey[0])
	}
	setTraceHeaders(ctx, req)
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode RAG request: %w", err)
		}
		req.SetBody(encoded)
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.doer.Do(requestCtx, req, resp); err != nil {
		return fmt.Errorf("execute RAG request: %w", err)
	}
	responseBody := resp.Body()
	if len(responseBody) > c.maxResponseBytes {
		return &ResponseTooLargeError{Size: len(responseBody), Limit: c.maxResponseBytes}
	}
	var envelope rawEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return fmt.Errorf("decode RAG response: %w", err)
	}
	status := resp.StatusCode()
	if status < 200 || status >= 300 || envelope.Code != consts.StatusOK {
		return &APIError{HTTPStatus: status, Code: envelope.Code, Message: envelope.Message, RetryAfter: parseRetryAfter(string(resp.Header.Peek("Retry-After")))}
	}
	if result != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return fmt.Errorf("decode RAG response data: %w", err)
		}
	}
	return nil
}

func userTokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(accessTokenKey{}).(string)
	return strings.TrimSpace(token)
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil {
		return seconds
	}
	if deadline, err := time.Parse(time.RFC1123, value); err == nil {
		if delay := time.Until(deadline); delay > 0 {
			return delay
		}
	}
	return 0
}

func setTraceHeaders(ctx context.Context, request *protocol.Request) {
	headers, _ := ctx.Value(traceHeadersKey{}).(TraceHeaders)
	for key, value := range map[string]string{
		"X-Request-ID":   headers.RequestID,
		"X-Agent-Run-ID": headers.AgentRunID,
		"X-Tool-Call-ID": headers.ToolCallID,
	} {
		if value != "" {
			request.Header.Set(key, value)
		}
	}
}
