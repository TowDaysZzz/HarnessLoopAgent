package ragclient

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/google/uuid"
)

type doerFunc func(context.Context, *protocol.Request, *protocol.Response) error

func (f doerFunc) Do(ctx context.Context, request *protocol.Request, response *protocol.Response) error {
	return f(ctx, request, response)
}

func TestRetrieveDecodesTypedEnvelopeAndHeaders(t *testing.T) {
	client := newTestClient(t, doerFunc(func(_ context.Context, request *protocol.Request, response *protocol.Response) error {
		if got := request.Header.Get("Authorization"); got != "Bearer rag_test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := request.Header.Get("X-Request-ID"); got != "request-1" {
			t.Fatalf("X-Request-ID = %q", got)
		}
		if !strings.Contains(string(request.Body()), `"kb_ids":[2]`) {
			t.Fatalf("request body = %s", request.Body())
		}
		response.SetStatusCode(200)
		response.SetBodyString(`{"code":200,"message":"Success","data":{"request_id":"rag-1","items":[{"content":"Go GC uses concurrent marking.","score":0.91,"citation":{"kb_id":2,"document_id":3,"chunk_id":"chunk-1","file_name":"go_interview.md","chunk_index":7},"source":{"route":"hybrid","collection":"kb_2_docs","retriever_version":"v1"}}]}}`)
		return nil
	}))
	ctx := WithTraceHeaders(context.Background(), TraceHeaders{RequestID: "request-1"})
	result, err := client.Retrieve(ctx, RetrieveRequest{Query: "Go GC", KBIDs: []uint64{2}, TopK: 5})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if result.RequestID != "rag-1" || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Items[0].Citation.FileName != "go_interview.md" || result.Items[0].Source.Route != "hybrid" {
		t.Fatalf("typed item = %#v", result.Items[0])
	}
}

func TestRetrieveDecodesRefusal(t *testing.T) {
	client := newTestClient(t, staticResponse(200, `{"code":200,"message":"Success","data":{"request_id":"rag-2","items":[],"evidence_gate_result":"refused","refusal":{"reason":"no_retrieval_hit","message":"证据不足"}}}`))
	result, err := client.Retrieve(context.Background(), RetrieveRequest{Query: "missing", KBIDs: []uint64{2}, TopK: 5})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if result.Refusal == nil || result.Refusal.Message != "证据不足" || result.Items == nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestRetrieveReturnsStructuredAPIErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client := newTestClient(t, staticResponse(status, `{"code":`+strconv.Itoa(status)+`,"message":"denied","data":null}`))
			_, err := client.Retrieve(context.Background(), RetrieveRequest{Query: "query", KBIDs: []uint64{2}, TopK: 5})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.HTTPStatus != status {
				t.Fatalf("Retrieve() error = %v", err)
			}
			if apiErr.Temporary() != (status == 429 || status >= 500) {
				t.Fatalf("Temporary() = %v", apiErr.Temporary())
			}
		})
	}
}

func TestRetrieveRejectsMalformedAndLargeResponses(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		client := newTestClient(t, staticResponse(200, `{not-json}`))
		_, err := client.Retrieve(context.Background(), RetrieveRequest{Query: "query", KBIDs: []uint64{2}, TopK: 5})
		if err == nil || !strings.Contains(err.Error(), "decode RAG response") {
			t.Fatalf("Retrieve() error = %v", err)
		}
	})
	t.Run("large", func(t *testing.T) {
		client, err := newClient(ClientConfig{BaseURL: "http://rag.test", APIKey: "rag_test", Timeout: time.Second, MaxResponseBytes: 8}, staticResponse(200, `{"code":200}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Retrieve(context.Background(), RetrieveRequest{Query: "query", KBIDs: []uint64{2}, TopK: 5})
		var tooLarge *ResponseTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("Retrieve() error = %v", err)
		}
	})
}

func TestRetrieveHonorsTimeout(t *testing.T) {
	client, err := newClient(ClientConfig{BaseURL: "http://rag.test", APIKey: "rag_test", Timeout: 10 * time.Millisecond}, doerFunc(func(ctx context.Context, _ *protocol.Request, _ *protocol.Response) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Retrieve(context.Background(), RetrieveRequest{Query: "query", KBIDs: []uint64{2}, TopK: 5})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Retrieve() error = %v", err)
	}
}

func TestUserNoteAPIsRequireAndForwardAccessToken(t *testing.T) {
	client := newTestClient(t, doerFunc(func(_ context.Context, request *protocol.Request, response *protocol.Response) error {
		if got := request.Header.Get("Authorization"); got != "Bearer user-jwt" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "note-1" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		response.SetStatusCode(200)
		response.SetBodyString(`{"code":200,"message":"Success","data":{"document_id":10,"job_id":20,"external_note_id":"note-1","status":"pending","reused":false}}`)
		return nil
	}))
	_, err := client.CreateNote(context.Background(), CreateNoteRequest{KBID: 5, ExternalNoteID: "note-1", Title: "title", Content: "content"})
	if err == nil || !strings.Contains(err.Error(), "user access token is required") {
		t.Fatalf("CreateNote() without token error = %v", err)
	}
	ctx := WithUserAccessToken(context.Background(), "user-jwt")
	created, err := client.CreateNote(ctx, CreateNoteRequest{KBID: 5, ExternalNoteID: "note-1", Title: "title", Content: "content"})
	if err != nil {
		t.Fatalf("CreateNote() error = %v", err)
	}
	if created.DocumentID != 10 || created.JobID != 20 || created.Status != "pending" {
		t.Fatalf("CreateNote() = %#v", created)
	}
}

func TestKnowledgeBaseAPIsUseUserToken(t *testing.T) {
	calls := 0
	client := newTestClient(t, doerFunc(func(_ context.Context, request *protocol.Request, response *protocol.Response) error {
		calls++
		if got := request.Header.Get("Authorization"); got != "Bearer user-jwt" {
			t.Fatalf("Authorization = %q", got)
		}
		switch calls {
		case 1:
			if !strings.Contains(string(request.URI().Path()), "/api/kb/bases") {
				t.Fatalf("list path = %s", request.URI().Path())
			}
			response.SetStatusCode(200)
			response.SetBodyString(`{"code":200,"message":"Success","data":{"items":[],"total":0,"page":1,"page_size":100}}`)
		case 2:
			if !strings.Contains(string(request.Body()), `"name":"我的笔记"`) {
				t.Fatalf("create body = %s", request.Body())
			}
			response.SetStatusCode(200)
			response.SetBodyString(`{"code":200,"message":"Success","data":{"id":9,"tenant_id":4,"user_id":3,"name":"我的笔记","status":"active"}}`)
		}
		return nil
	}))
	ctx := WithUserAccessToken(context.Background(), "user-jwt")
	listed, err := client.ListKnowledgeBases(ctx)
	if err != nil || listed.Total != 0 || listed.Items == nil {
		t.Fatalf("ListKnowledgeBases() = %#v, %v", listed, err)
	}
	created, err := client.CreateKnowledgeBase(ctx, CreateKnowledgeBaseRequest{Name: "我的笔记"})
	if err != nil || created.ID != 9 || created.UserID != 3 {
		t.Fatalf("CreateKnowledgeBase() = %#v, %v", created, err)
	}
}

func TestAuthAPIsDoNotSendStaticAPIKey(t *testing.T) {
	client := newTestClient(t, doerFunc(func(_ context.Context, request *protocol.Request, response *protocol.Response) error {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q", got)
		}
		response.SetStatusCode(200)
		response.SetBodyString(`{"code":200,"message":"Success","data":{"access_token":"access","refresh_token":"refresh","expires_in":7200,"user_id":3,"role":"owner","tenant_id":3}}`)
		return nil
	}))
	result, err := client.Login(context.Background(), LoginRequest{Email: "user@example.com", Password: "secret"})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if result.AccessToken != "access" || result.UserID != 3 || result.TenantID != 3 {
		t.Fatalf("Login() = %#v", result)
	}
}

func TestMemoryIndexUsesTrustedOwnerAndStableID(t *testing.T) {
	id := uuid.NewString()
	client := newMemoryTestClient(t, doerFunc(func(_ context.Context, request *protocol.Request, response *protocol.Response) error {
		body := string(request.Body())
		if strings.Contains(body, "tenant_id") || strings.Contains(body, "user_id") || strings.Contains(body, "collection") {
			t.Fatalf("untrusted scope leaked into body: %s", body)
		}
		if request.Header.Get("Authorization") != "Bearer rag_test" || request.Header.Get("X-Memory-Tenant-ID") != "7" || request.Header.Get("X-Memory-User-ID") != "9" || request.Header.Get("X-Memory-Owner-Claim") == "" {
			t.Fatalf("memory auth headers missing: authorization=%q tenant=%q user=%q claim_present=%v", request.Header.Get("Authorization"), request.Header.Get("X-Memory-Tenant-ID"), request.Header.Get("X-Memory-User-ID"), request.Header.Get("X-Memory-Owner-Claim") != "")
		}
		if request.Header.Get("Idempotency-Key") != id+":"+strings.Repeat("a", 64)+":v1" {
			t.Fatalf("idempotency=%q", request.Header.Get("Idempotency-Key"))
		}
		response.SetStatusCode(200)
		response.SetBodyString(`{"code":200,"message":"Success","data":{"memory_id":"` + id + `","indexed":true}}`)
		return nil
	}))
	request := MemoryIndexRequest{MemoryID: id, CanonicalText: "user likes tea", ContentHash: strings.Repeat("a", 64), Layer: "long_term", Kind: "preference", CreatedAt: time.Now().UTC(), ProjectionVersion: "v1"}
	if _, err := client.IndexMemory(context.Background(), request); err == nil || !strings.Contains(err.Error(), "trusted memory owner") {
		t.Fatalf("missing owner error=%v", err)
	}
	result, err := client.IndexMemory(WithTrustedMemoryOwner(context.Background(), 7, 9), request)
	if err != nil || !result.Indexed {
		t.Fatalf("IndexMemory=%+v err=%v", result, err)
	}
}

func TestMemorySearchValidatesUntrustedResponse(t *testing.T) {
	validID := uuid.NewString()
	ctx := WithTrustedMemoryOwner(context.Background(), 7, 9)
	tests := []struct{ name, body string }{
		{"unknown id", `{"code":200,"message":"Success","data":{"candidates":[{"memory_id":"bad","score":0.5}]}}`},
		{"score", `{"code":200,"message":"Success","data":{"candidates":[{"memory_id":"` + validID + `","score":1.1}]}}`},
		{"cursor", `{"code":200,"message":"Success","data":{"candidates":[],"next_cursor":"` + strings.Repeat("x", 513) + `"}}`},
		{"duplicate", `{"code":200,"message":"Success","data":{"candidates":[{"memory_id":"` + validID + `","score":0.8},{"memory_id":"` + validID + `","score":0.7}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMemoryTestClient(t, staticResponse(200, tt.body))
			if _, err := client.SearchMemory(ctx, MemorySearchRequest{Query: "tea", Limit: 20}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	client := newMemoryTestClient(t, staticResponse(200, `{"code":200,"message":"Success","data":{"request_id":"r1","candidates":[{"memory_id":"`+validID+`","score":0.8}],"next_cursor":"page2"}}`))
	result, err := client.SearchMemory(ctx, MemorySearchRequest{Query: "tea", Layers: []MemoryLayer{"long_term"}, Limit: 20})
	if err != nil || len(result.Candidates) != 1 || result.NextCursor != "page2" {
		t.Fatalf("SearchMemory=%+v err=%v", result, err)
	}
}

func TestMemorySearchClassifiesTemporaryErrorsAndBounds(t *testing.T) {
	ctx := WithTrustedMemoryOwner(context.Background(), 1, 2)
	for _, status := range []int{400, 429, 503} {
		client := newMemoryTestClient(t, staticResponse(status, `{"code":`+strconv.Itoa(status)+`,"message":"failed","data":null}`))
		_, err := client.SearchMemory(ctx, MemorySearchRequest{Query: "x", Limit: 20})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Temporary() != (status == 429 || status >= 500) {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
	client := newMemoryTestClient(t, staticResponse(200, `{"code":200,"message":"Success","data":{"candidates":[]}}`))
	if _, err := client.SearchMemory(ctx, MemorySearchRequest{Query: "x", Limit: 201}); err == nil {
		t.Fatal("expected limit error")
	}
}

func TestMemoryGenerationLifecycle(t *testing.T) {
	ctx := WithTrustedMemoryOwner(context.Background(), 1, 2)
	calls := 0
	client := newMemoryTestClient(t, doerFunc(func(_ context.Context, request *protocol.Request, response *protocol.Response) error {
		calls++
		path := string(request.URI().Path())
		switch calls {
		case 1:
			if path != "/v1/memories/generations" {
				t.Fatalf("path=%s", path)
			}
		case 2:
			if path != "/v1/memories/generations/gen-v2/validate" {
				t.Fatalf("path=%s", path)
			}
		case 3:
			if path != "/v1/memories/generations/gen-v2/switch" {
				t.Fatalf("path=%s", path)
			}
		}
		response.SetStatusCode(200)
		response.SetBodyString(`{"code":200,"message":"Success","data":{"generation":"gen-v2","status":"ready"}}`)
		return nil
	}))
	if _, err := client.StartMemoryGeneration(ctx, MemoryGenerationRequest{Generation: "gen-v2", EmbeddingModel: "embed-v2", ProjectionVersion: "v2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ValidateMemoryGeneration(ctx, "gen-v2"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SwitchMemoryGeneration(ctx, "gen-v2"); err != nil {
		t.Fatal(err)
	}
}

func newTestClient(t *testing.T, doer doer) *Client {
	t.Helper()
	client, err := newClient(ClientConfig{BaseURL: "http://rag.test", APIKey: "rag_test", Timeout: time.Second}, doer)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	return client
}

func newMemoryTestClient(t *testing.T, doer doer) *Client {
	t.Helper()
	client, err := newClient(ClientConfig{BaseURL: "http://rag.test", APIKey: "rag_test", OwnerClaimSecret: "owner-secret", Timeout: time.Second}, doer)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func staticResponse(status int, body string) doerFunc {
	return func(_ context.Context, _ *protocol.Request, response *protocol.Response) error {
		response.SetStatusCode(status)
		response.SetBodyString(body)
		return nil
	}
}
