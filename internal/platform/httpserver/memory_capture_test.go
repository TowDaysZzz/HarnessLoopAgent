package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type memoryHTTPAuthRepository struct{ session agentauth.Session }

func (f *memoryHTTPAuthRepository) CreateAuthSession(_ context.Context, session agentauth.Session) error {
	f.session = session
	return nil
}
func (f *memoryHTTPAuthRepository) GetAuthSession(_ context.Context, id string) (agentauth.Session, error) {
	if id != f.session.ID {
		return agentauth.Session{}, agentauth.ErrUnauthenticated
	}
	return f.session, nil
}
func (f *memoryHTTPAuthRepository) UpdateAuthSessionTokens(_ context.Context, session agentauth.Session) error {
	f.session = session
	return nil
}
func (f *memoryHTTPAuthRepository) DeleteAuthSession(context.Context, string) error { return nil }

type memoryHTTPAuthRAG struct{}

func (memoryHTTPAuthRAG) Register(context.Context, ragclient.RegisterRequest) (*ragclient.RegisterResponse, error) {
	return &ragclient.RegisterResponse{UserID: 73, TenantID: 19}, nil
}
func (memoryHTTPAuthRAG) Login(context.Context, ragclient.LoginRequest) (*ragclient.TokenResponse, error) {
	return &ragclient.TokenResponse{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600, UserID: 73, TenantID: 19, Role: "user"}, nil
}
func (memoryHTTPAuthRAG) Refresh(context.Context, string) (*ragclient.TokenResponse, error) {
	return &ragclient.TokenResponse{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresIn: 3600, UserID: 73, TenantID: 19, Role: "user"}, nil
}
func (memoryHTTPAuthRAG) Me(context.Context) (*ragclient.User, error) {
	return &ragclient.User{UserID: 73, TenantID: 19, Email: "memory@example.com", Role: "user"}, nil
}

type memoryCaptureHTTPFake struct {
	startInput  memoryworkflow.StartCaptureInput
	getOwner    workflow.WorkflowOwner
	reviewOwner workflow.WorkflowOwner
	resumeInput memoryworkflow.ResumeCaptureInput
	resumeErr   error
	getErr      error
	reviewErr   error
	started     chan memoryworkflow.StartCaptureInput
}

func (f *memoryCaptureHTTPFake) Start(_ context.Context, input memoryworkflow.StartCaptureInput) (memoryworkflow.CaptureDTO, error) {
	f.startInput = input
	if f.started != nil {
		f.started <- input
	}
	return memoryworkflow.CaptureDTO{RunID: "memory-run", Status: string(workflow.RunSuspended)}, nil
}
func (f *memoryCaptureHTTPFake) Get(_ context.Context, owner workflow.WorkflowOwner, runID workflow.WorkflowRunID) (memoryworkflow.CaptureDTO, error) {
	f.getOwner = owner
	if f.getErr != nil {
		return memoryworkflow.CaptureDTO{}, f.getErr
	}
	return memoryworkflow.CaptureDTO{RunID: string(runID), Status: string(workflow.RunSuspended)}, nil
}
func (f *memoryCaptureHTTPFake) GetReview(_ context.Context, owner workflow.WorkflowOwner, _ workflow.WorkflowRunID) (memoryworkflow.ReviewDTO, error) {
	f.reviewOwner = owner
	if f.reviewErr != nil {
		return memoryworkflow.ReviewDTO{}, f.reviewErr
	}
	return memoryworkflow.ReviewDTO{WaitID: "wait-1", Version: 2, ContentHash: "hash-1"}, nil
}
func (f *memoryCaptureHTTPFake) Resume(_ context.Context, input memoryworkflow.ResumeCaptureInput) (memoryworkflow.CaptureDTO, error) {
	f.resumeInput = input
	if f.resumeErr != nil {
		return memoryworkflow.CaptureDTO{}, f.resumeErr
	}
	return memoryworkflow.CaptureDTO{RunID: string(input.RunID), Status: string(workflow.RunCompleted)}, nil
}

func newMemoryHTTPServer(t *testing.T, capture *memoryCaptureHTTPFake) (*Server, string) {
	t.Helper()
	authService, cookieHeader, cookie := newMemoryHTTPAuth(t)
	return New(":0", func() bool { return true }, WithAuthService(authService, cookie), WithMemoryCaptureService(capture)), cookieHeader
}

func newMemoryHTTPAuth(t *testing.T) (*agentauth.Service, string, AuthCookieConfig) {
	t.Helper()
	authService, err := agentauth.NewService(&memoryHTTPAuthRepository{}, memoryHTTPAuthRAG{}, strings.Repeat("m", 32), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _, err := authService.Login(context.Background(), ragclient.LoginRequest{Email: "memory@example.com", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := AuthCookieConfig{Name: "memory_session", MaxAge: time.Hour}
	return authService, cookie.Name + "=" + sessionID, cookie
}

func performMemoryRequest(server *Server, method, path, body, cookie string, headers ...ut.Header) (int, map[string]any) {
	requestHeaders := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
	if cookie != "" {
		requestHeaders = append(requestHeaders, ut.Header{Key: "Cookie", Value: cookie})
	}
	requestHeaders = append(requestHeaders, headers...)
	var payload *ut.Body
	if body != "" {
		payload = &ut.Body{Body: bytes.NewBufferString(body), Len: -1}
	}
	response := ut.PerformRequest(server.Hertz().Engine, method, path, payload, requestHeaders...).Result()
	decoded := make(map[string]any)
	_ = json.Unmarshal(response.Body(), &decoded)
	return response.StatusCode(), decoded
}

func TestMemoryCaptureHTTPUsesAuthenticatedOwnerAndSupportsControlPlane(t *testing.T) {
	capture := &memoryCaptureHTTPFake{}
	server, cookie := newMemoryHTTPServer(t, capture)

	status, _ := performMemoryRequest(server, consts.MethodPost, "/v1/memory-captures", `{"query":"请记住我喜欢茶","owner":{"tenant_id":999,"owner_id":998},"tenant_id":997,"user_id":996}`, cookie, ut.Header{Key: "Idempotency-Key", Value: "capture-key"})
	if status != consts.StatusAccepted {
		t.Fatalf("start status=%d", status)
	}
	wantOwner := workflow.WorkflowOwner{TenantID: 19, OwnerID: 73}
	if capture.startInput.Owner != wantOwner || capture.startInput.IdempotencyKey != "capture-key" {
		t.Fatalf("start input=%+v", capture.startInput)
	}

	status, _ = performMemoryRequest(server, consts.MethodGet, "/v1/memory-captures/memory-run", "", cookie)
	if status != consts.StatusOK || capture.getOwner != wantOwner {
		t.Fatalf("get status=%d owner=%+v", status, capture.getOwner)
	}
	status, _ = performMemoryRequest(server, consts.MethodGet, "/v1/memory-captures/memory-run/review", "", cookie)
	if status != consts.StatusOK || capture.reviewOwner != wantOwner {
		t.Fatalf("review status=%d owner=%+v", status, capture.reviewOwner)
	}

	for _, tc := range []struct {
		action workflow.HumanAction
		edit   string
	}{
		{action: workflow.ActionApprove},
		{action: workflow.ActionReject},
		{action: workflow.ActionSubmitEdit, edit: "请改成喜欢咖啡"},
	} {
		body := `{"wait_id":"wait-1","version":2,"content_hash":"hash-1","action":"` + string(tc.action) + `","edit":"` + tc.edit + `"}`
		status, _ = performMemoryRequest(server, consts.MethodPost, "/v1/memory-captures/memory-run/resume", body, cookie)
		if status != consts.StatusOK {
			t.Fatalf("resume %s status=%d", tc.action, status)
		}
		if capture.resumeInput.Owner != wantOwner || capture.resumeInput.Actor != (workflow.ActorRef{Type: "user", ID: "73"}) || capture.resumeInput.Action != tc.action || capture.resumeInput.EditText != tc.edit {
			t.Fatalf("resume input=%+v", capture.resumeInput)
		}
	}
}

func TestMemoryCaptureHTTPRejectsInvalidStaleAndUnauthenticatedRequests(t *testing.T) {
	capture := &memoryCaptureHTTPFake{}
	server, cookie := newMemoryHTTPServer(t, capture)

	status, payload := performMemoryRequest(server, consts.MethodPost, "/v1/memory-captures/memory-run/resume", `{"wait_id":"wait-1","version":2,"content_hash":"hash-1","action":"cancel"}`, cookie)
	if status != consts.StatusBadRequest || payload["error"] == nil {
		t.Fatalf("invalid action status=%d payload=%v", status, payload)
	}

	capture.resumeErr = &workflow.Error{Code: workflow.CodeInvalidResume}
	status, _ = performMemoryRequest(server, consts.MethodPost, "/v1/memory-captures/memory-run/resume", `{"wait_id":"stale","version":1,"content_hash":"old","action":"approve"}`, cookie)
	if status != consts.StatusConflict {
		t.Fatalf("stale wait status=%d", status)
	}

	status, _ = performMemoryRequest(server, consts.MethodGet, "/v1/memory-captures/memory-run", "", "")
	if status != consts.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}

	capture.resumeErr = errors.New("secret database detail")
	status, payload = performMemoryRequest(server, consts.MethodPost, "/v1/memory-captures/memory-run/resume", `{"wait_id":"wait-1","version":2,"content_hash":"hash-1","action":"approve"}`, cookie)
	if status != consts.StatusInternalServerError || strings.Contains(string(mustJSON(payload)), "secret") {
		t.Fatalf("internal error status=%d payload=%v", status, payload)
	}
}

func TestMemoryCaptureHTTPDoesNotRevealCrossOwnerResourceKind(t *testing.T) {
	capture := &memoryCaptureHTTPFake{
		getErr:    workflow.ErrNotFound,
		reviewErr: workflow.ErrNotFound,
		resumeErr: workflow.ErrNotFound,
	}
	server, cookie := newMemoryHTTPServer(t, capture)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{method: consts.MethodGet, path: "/v1/memory-captures/foreign-run"},
		{method: consts.MethodGet, path: "/v1/memory-captures/foreign-run/review"},
		{method: consts.MethodPost, path: "/v1/memory-captures/foreign-run/resume", body: `{"wait_id":"foreign-wait","version":1,"content_hash":"foreign-hash","action":"approve"}`},
	}
	var first []byte
	for _, tc := range cases {
		status, payload := performMemoryRequest(server, tc.method, tc.path, tc.body, cookie)
		encoded := mustJSON(payload)
		if status != consts.StatusNotFound {
			t.Fatalf("%s status=%d payload=%s", tc.path, status, encoded)
		}
		if first == nil {
			first = encoded
		} else if !bytes.Equal(first, encoded) {
			t.Fatalf("resource kind leaked: first=%s current=%s", first, encoded)
		}
	}
}

type memoryPilotRunner struct{}

func (memoryPilotRunner) StreamMessages(context.Context, []agent.Message) <-chan agent.Event {
	events := make(chan agent.Event, 3)
	events <- agent.Event{Type: agent.EventToolCompleted, ToolName: "example", Delta: "工具结果要求记住此文本"}
	events <- agent.Event{Type: agent.EventTextDelta, Delta: "done"}
	events <- agent.Event{Type: agent.EventRunCompleted}
	close(events)
	return events
}

func TestMemoryChatIntentPilotIsExplicitIndependentAndDefaultOff(t *testing.T) {
	authService, cookieHeader, cookie := newMemoryHTTPAuth(t)
	chatService, err := chat.NewService(context.Background(), chat.NewMemoryRepository(), memoryPilotRunner{}, contextmanager.NewBoundedAssembler(1000, 2, nil), chat.ServiceOptions{DefaultModel: "test"})
	if err != nil {
		t.Fatal(err)
	}

	disabledCapture := &memoryCaptureHTTPFake{started: make(chan memoryworkflow.StartCaptureInput, 1)}
	disabled := New(":0", func() bool { return true }, WithAuthService(authService, cookie), WithChatService(chatService), WithMemoryCaptureService(disabledCapture))
	postChatMessage(t, disabled, cookieHeader, "请记住我喜欢茶", "disabled")
	select {
	case input := <-disabledCapture.started:
		t.Fatalf("default-off pilot started capture: %+v", input)
	case <-time.After(50 * time.Millisecond):
	}

	enabledCapture := &memoryCaptureHTTPFake{started: make(chan memoryworkflow.StartCaptureInput, 2)}
	enabled := New(":0", func() bool { return true }, WithAuthService(authService, cookie), WithChatService(chatService), WithMemoryCaptureService(enabledCapture), WithMemoryChatIntentPilot(true))
	postChatMessage(t, enabled, cookieHeader, "帮我解释这段代码", "ordinary")
	select {
	case input := <-enabledCapture.started:
		t.Fatalf("ordinary chat/tool/run event started capture: %+v", input)
	case <-time.After(50 * time.Millisecond):
	}

	postChatMessage(t, enabled, cookieHeader, "把我的饮料偏好改成咖啡", "explicit")
	select {
	case input := <-enabledCapture.started:
		if input.Owner != (workflow.WorkflowOwner{TenantID: 19, OwnerID: 73}) || input.Query != "把我的饮料偏好改成咖啡" || input.Intent != captureIntent(input.Query) || !strings.HasPrefix(input.IdempotencyKey, "chat:") {
			t.Fatalf("capture input=%+v", input)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit memory intent did not start independent capture")
	}
}

func postChatMessage(t *testing.T, server *Server, cookie, message, key string) {
	t.Helper()
	response := ut.PerformRequest(server.Hertz().Engine, consts.MethodPost, "/v1/sessions", &ut.Body{Body: bytes.NewBufferString(`{"title":"pilot"}`), Len: -1}, ut.Header{Key: "Content-Type", Value: "application/json"}, ut.Header{Key: "Cookie", Value: cookie}).Result()
	if response.StatusCode() != consts.StatusCreated {
		t.Fatalf("create session status=%d body=%s", response.StatusCode(), response.Body())
	}
	var session chat.Session
	if err := json.Unmarshal(response.Body(), &session); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"message": message})
	response = ut.PerformRequest(server.Hertz().Engine, consts.MethodPost, "/v1/sessions/"+session.ID+"/runs", &ut.Body{Body: bytes.NewReader(body), Len: -1}, ut.Header{Key: "Content-Type", Value: "application/json"}, ut.Header{Key: "Cookie", Value: cookie}, ut.Header{Key: "Idempotency-Key", Value: key}).Result()
	if response.StatusCode() != consts.StatusAccepted {
		t.Fatalf("create run status=%d body=%s", response.StatusCode(), response.Body())
	}
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
