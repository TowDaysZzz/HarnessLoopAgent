package ragclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	appclient "github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
)

const defaultMaxResponseBytes = 2 << 20

type ClientConfig struct {
	BaseURL          string
	APIKey           string
	OwnerClaimSecret string
	Timeout          time.Duration
	MaxResponseBytes int
}

type Retriever interface {
	Retrieve(ctx context.Context, request RetrieveRequest) (*RetrieveResponse, error)
}

type MemoryClient interface {
	IndexMemory(context.Context, MemoryIndexRequest) (*MemoryIndexResponse, error)
	SearchMemory(context.Context, MemorySearchRequest) (*MemorySearchResponse, error)
	StartMemoryGeneration(context.Context, MemoryGenerationRequest) (*MemoryGenerationResponse, error)
	ValidateMemoryGeneration(context.Context, string) (*MemoryGenerationResponse, error)
	SwitchMemoryGeneration(context.Context, string) (*MemoryGenerationResponse, error)
}

type doer interface {
	Do(ctx context.Context, request *protocol.Request, response *protocol.Response) error
}

type Client struct {
	baseURL          string
	apiKey           string
	ownerClaimSecret string
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
type memoryOwnerKey struct{}

type TrustedMemoryOwner struct {
	TenantID uint64
	UserID   uint64
}

// WithTrustedMemoryOwner is only for authenticated service or durable workflow boundaries.
// Memory request bodies intentionally contain no owner or collection fields.
func WithTrustedMemoryOwner(ctx context.Context, tenantID, userID uint64) context.Context {
	return context.WithValue(ctx, memoryOwnerKey{}, TrustedMemoryOwner{TenantID: tenantID, UserID: userID})
}

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
		ownerClaimSecret: strings.TrimSpace(config.OwnerClaimSecret),
		timeout:          config.Timeout,
		maxResponseBytes: maxBytes,
		doer:             doer,
	}, nil
}

func (c *Client) IndexMemory(ctx context.Context, request MemoryIndexRequest) (*MemoryIndexResponse, error) {
	if _, err := uuid.Parse(request.MemoryID); err != nil || strings.TrimSpace(request.CanonicalText) == "" || len(request.CanonicalText) > 16*1024 || len(request.ContentHash) != 64 || request.Layer == "" || request.Kind == "" || request.CreatedAt.IsZero() || strings.TrimSpace(request.ProjectionVersion) == "" {
		return nil, errors.New("invalid RAG memory index request")
	}
	var result MemoryIndexResponse
	if err := c.doMemoryJSON(ctx, consts.MethodPost, "/v1/memories/index", request, &result, request.MemoryID+":"+request.ContentHash+":"+request.ProjectionVersion); err != nil {
		return nil, fmt.Errorf("index RAG memory: %w", err)
	}
	if result.MemoryID != request.MemoryID {
		return nil, errors.New("RAG memory index returned mismatched memory ID")
	}
	return &result, nil
}

func (c *Client) SearchMemory(ctx context.Context, request MemorySearchRequest) (*MemorySearchResponse, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || len(request.Query) > 16*1024 || request.Limit < 1 || request.Limit > 200 || len(request.Cursor) > 512 || len(request.Layers) > 8 || len(request.Kinds) > 32 {
		return nil, errors.New("invalid RAG memory search request")
	}
	var result MemorySearchResponse
	if err := c.doMemoryJSON(ctx, consts.MethodPost, "/v1/memories/search", request, &result, ""); err != nil {
		return nil, fmt.Errorf("search RAG memory: %w", err)
	}
	if len(result.Candidates) > request.Limit || len(result.NextCursor) > 512 {
		return nil, errors.New("invalid RAG memory search response bounds")
	}
	seen := map[string]struct{}{}
	for _, candidate := range result.Candidates {
		if _, err := uuid.Parse(candidate.MemoryID); err != nil || candidate.Score < 0 || candidate.Score > 1 {
			return nil, errors.New("invalid RAG memory search candidate")
		}
		if _, ok := seen[candidate.MemoryID]; ok {
			return nil, errors.New("duplicate RAG memory search candidate")
		}
		seen[candidate.MemoryID] = struct{}{}
	}
	if result.Candidates == nil {
		result.Candidates = []MemorySearchCandidate{}
	}
	return &result, nil
}

func (c *Client) StartMemoryGeneration(ctx context.Context, request MemoryGenerationRequest) (*MemoryGenerationResponse, error) {
	if !validGeneration(request.Generation) || strings.TrimSpace(request.EmbeddingModel) == "" || strings.TrimSpace(request.ProjectionVersion) == "" {
		return nil, errors.New("invalid memory generation request")
	}
	var result MemoryGenerationResponse
	if err := c.doMemoryJSON(ctx, consts.MethodPost, "/v1/memories/generations", request, &result, "generation:"+request.Generation); err != nil {
		return nil, fmt.Errorf("start memory generation: %w", err)
	}
	return &result, nil
}

func (c *Client) ValidateMemoryGeneration(ctx context.Context, generation string) (*MemoryGenerationResponse, error) {
	return c.memoryGenerationAction(ctx, generation, "validate")
}
func (c *Client) SwitchMemoryGeneration(ctx context.Context, generation string) (*MemoryGenerationResponse, error) {
	return c.memoryGenerationAction(ctx, generation, "switch")
}
func (c *Client) memoryGenerationAction(ctx context.Context, generation, action string) (*MemoryGenerationResponse, error) {
	if !validGeneration(generation) {
		return nil, errors.New("invalid memory generation")
	}
	var result MemoryGenerationResponse
	if err := c.doMemoryJSON(ctx, consts.MethodPost, "/v1/memories/generations/"+generation+"/"+action, nil, &result, action+":"+generation); err != nil {
		return nil, fmt.Errorf("%s memory generation: %w", action, err)
	}
	return &result, nil
}
func validGeneration(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (c *Client) doMemoryJSON(ctx context.Context, method, path string, body, result any, idempotencyKey string) error {
	owner, ok := ctx.Value(memoryOwnerKey{}).(TrustedMemoryOwner)
	if !ok || owner.TenantID == 0 || owner.UserID == 0 {
		return errors.New("trusted memory owner is required")
	}
	if c.apiKey == "" || c.ownerClaimSecret == "" {
		return errors.New("RAG memory service authorization is not configured")
	}
	issued := time.Now().UTC().Unix()
	payload := fmt.Sprintf("%d:%d:%d:memory", owner.TenantID, owner.UserID, issued)
	mac := hmac.New(sha256.New, []byte(c.ownerClaimSecret))
	_, _ = mac.Write([]byte(payload))
	claim := fmt.Sprintf("%s:%x", payload, mac.Sum(nil))
	ctx = context.WithValue(ctx, memoryClaimHeadersKey{}, memoryClaimHeaders{TenantID: owner.TenantID, UserID: owner.UserID, Claim: claim})
	keys := []string{}
	if idempotencyKey != "" {
		keys = []string{idempotencyKey}
	}
	return c.doJSON(ctx, method, path, body, "service", result, keys...)
}

type memoryClaimHeadersKey struct{}
type memoryClaimHeaders struct {
	TenantID, UserID uint64
	Claim            string
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
	if memoryHeaders, ok := ctx.Value(memoryClaimHeadersKey{}).(memoryClaimHeaders); ok {
		request.Header.Set("X-Memory-Tenant-ID", strconv.FormatUint(memoryHeaders.TenantID, 10))
		request.Header.Set("X-Memory-User-ID", strconv.FormatUint(memoryHeaders.UserID, 10))
		request.Header.Set("X-Memory-Owner-Claim", memoryHeaders.Claim)
	}
}

var _ MemoryClient = (*Client)(nil)
