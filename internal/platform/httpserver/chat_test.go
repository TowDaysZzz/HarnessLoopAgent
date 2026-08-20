package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
)

type immediateRunner struct{}

func (immediateRunner) StreamMessages(_ context.Context, _ []agent.Message) <-chan agent.Event {
	out := make(chan agent.Event, 2)
	out <- agent.Event{Type: agent.EventTextDelta, Delta: "hello"}
	out <- agent.Event{Type: agent.EventRunCompleted}
	close(out)
	return out
}

func TestChatHTTPCreateSessionAndRun(t *testing.T) {
	service := testChatService(t)
	server := New("127.0.0.1:0", func() bool { return true }, WithChatService(service))
	response := ut.PerformRequest(server.Hertz().Engine, consts.MethodPost, "/v1/sessions", &ut.Body{Body: bytes.NewBufferString(`{"title":"notes"}`), Len: -1}, ut.Header{Key: "Content-Type", Value: "application/json"}).Result()
	if response.StatusCode() != consts.StatusCreated {
		t.Fatalf("create session status = %d body=%s", response.StatusCode(), response.Body())
	}
	var session chat.Session
	if err := json.Unmarshal(response.Body(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	path := "/v1/sessions/" + session.ID + "/runs"
	response = ut.PerformRequest(server.Hertz().Engine, consts.MethodPost, path, &ut.Body{Body: bytes.NewBufferString(`{"message":"hello"}`), Len: -1},
		ut.Header{Key: "Content-Type", Value: "application/json"}, ut.Header{Key: "Idempotency-Key", Value: "request-1"}).Result()
	if response.StatusCode() != consts.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", response.StatusCode(), response.Body())
	}
}

func TestChatHTTPListsSessions(t *testing.T) {
	service := testChatService(t)
	server := New("127.0.0.1:0", func() bool { return true }, WithChatService(service))
	ut.PerformRequest(server.Hertz().Engine, consts.MethodPost, "/v1/sessions", &ut.Body{Body: bytes.NewBufferString(`{"title":"history"}`), Len: -1}, ut.Header{Key: "Content-Type", Value: "application/json"})
	response := ut.PerformRequest(server.Hertz().Engine, consts.MethodGet, "/v1/sessions?limit=50", nil).Result()
	if response.StatusCode() != consts.StatusOK || !strings.Contains(string(response.Body()), `"title":"history"`) {
		t.Fatalf("list sessions status = %d body=%s", response.StatusCode(), response.Body())
	}
}

func TestStreamEventsReplaysAfterLastEventID(t *testing.T) {
	service := testChatService(t)
	session, _ := service.CreateSession(context.Background(), "notes")
	created, err := service.CreateRun(context.Background(), chat.CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		run, _ := service.GetRun(context.Background(), created.Run.ID)
		if run.Status.Terminal() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	reader, writer := io.Pipe()
	go streamEvents(context.Background(), writer, service, created.Run.ID, 1)
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	text := string(body)
	if strings.Contains(text, "id: 1\n") || !strings.Contains(text, "id: 2\n") || !strings.Contains(text, "event: run.completed") {
		t.Fatalf("SSE body = %q", text)
	}
}

func TestWriteSSE(t *testing.T) {
	var output bytes.Buffer
	err := writeSSE(&output, chat.Event{Sequence: 7, Type: "run.status", Data: map[string]any{"status": "validating"}})
	if err != nil {
		t.Fatalf("writeSSE() error = %v", err)
	}
	if output.String() != "id: 7\nevent: run.status\ndata: {\"status\":\"validating\"}\n\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func testChatService(t *testing.T) *chat.Service {
	t.Helper()
	service, err := chat.NewService(context.Background(), chat.NewMemoryRepository(), immediateRunner{}, contextmanager.NewBoundedAssembler(1000, 2, nil), chat.ServiceOptions{DefaultModel: "test"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}
