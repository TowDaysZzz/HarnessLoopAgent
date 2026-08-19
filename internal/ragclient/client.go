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

func WithTraceHeaders(ctx context.Context, headers TraceHeaders) context.Context {
	return context.WithValue(ctx, traceHeadersKey{}, headers)
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
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("RAG API key is required")
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
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode RAG request: %w", err)
	}

	req := protocol.AcquireRequest()
	resp := protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)
	req.SetRequestURI(c.baseURL + "/v1/retrieve")
	req.Header.SetMethod(consts.MethodPost)
	req.Header.SetContentTypeBytes([]byte("application/json"))
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	setTraceHeaders(ctx, req)
	req.SetBody(body)

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.doer.Do(requestCtx, req, resp); err != nil {
		return nil, fmt.Errorf("execute RAG retrieve request: %w", err)
	}
	responseBody := resp.Body()
	if len(responseBody) > c.maxResponseBytes {
		return nil, &ResponseTooLargeError{Size: len(responseBody), Limit: c.maxResponseBytes}
	}

	var envelope responseEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, fmt.Errorf("decode RAG response: %w", err)
	}
	status := resp.StatusCode()
	if status < 200 || status >= 300 || envelope.Code != consts.StatusOK {
		return nil, &APIError{HTTPStatus: status, Code: envelope.Code, Message: envelope.Message, RetryAfter: parseRetryAfter(string(resp.Header.Peek("Retry-After")))}
	}
	if envelope.Data.Items == nil {
		envelope.Data.Items = []RetrieveItem{}
	}
	return &envelope.Data, nil
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
