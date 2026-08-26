package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
)

type immediateRunner struct{}

func (immediateRunner) StreamConversation(_ context.Context, _ agent.ConversationRequest) <-chan agent.Event {
	out := make(chan agent.Event, 2)
	out <- agent.Event{Type: agent.EventTextDelta, Delta: "hello"}
	out <- agent.Event{Type: agent.EventRunCompleted}
	close(out)
	return out
}

type reconnectRunner struct {
	first   chan struct{}
	release chan struct{}
}

func (r reconnectRunner) StreamConversation(ctx context.Context, _ agent.ConversationRequest) <-chan agent.Event {
	out := make(chan agent.Event, 3)
	go func() {
		defer close(out)
		out <- agent.Event{Type: agent.EventTextDelta, Delta: "part-1"}
		close(r.first)
		select {
		case <-ctx.Done():
			out <- agent.Event{Type: agent.EventRunFailed, Err: ctx.Err()}
			return
		case <-r.release:
		}
		out <- agent.Event{Type: agent.EventTextDelta, Delta: "part-2"}
		out <- agent.Event{Type: agent.EventRunCompleted}
	}()
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
	var created struct {
		Run              chat.Run `json:"run"`
		EventsURL        string   `json:"events_url"`
		IdempotentReplay bool     `json:"idempotent_replay"`
	}
	if err := json.Unmarshal(response.Body(), &created); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if created.Run.SessionID != session.ID || created.EventsURL != "/v1/runs/"+created.Run.ID+"/events" || created.IdempotentReplay {
		t.Fatalf("run response = %#v", created)
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
	messages, err := service.ListMessages(context.Background(), session.ID, 100)
	if err != nil || len(messages) != 2 || messages[1].Content != "hello" {
		t.Fatalf("background run messages = %#v, %v", messages, err)
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

func TestStreamEventsReconnectsWithoutStoppingBackgroundRun(t *testing.T) {
	runner := reconnectRunner{first: make(chan struct{}), release: make(chan struct{})}
	service, err := chat.NewService(context.Background(), chat.NewMemoryRepository(), runner, chat.NewBoundedAssembler(1000, 2, nil), chat.ServiceOptions{DefaultModel: "test"})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := service.CreateSession(context.Background(), "reconnect")
	created, err := service.CreateRun(context.Background(), chat.CreateRunInput{SessionID: session.ID, Content: "hello", IdempotencyKey: "reconnect"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.first:
	case <-time.After(time.Second):
		t.Fatal("runner did not emit first delta")
	}

	clientCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	reader, writer := io.Pipe()
	go streamEvents(clientCtx, writer, service, created.Run.ID, 0)
	scanner := bufio.NewScanner(reader)
	var lastID int64
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			lastID, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
		}
		if line == "event: text.delta" {
			disconnect()
			break
		}
	}
	_ = reader.Close()
	if lastID == 0 {
		t.Fatal("first client did not consume a persisted event")
	}
	close(runner.release)
	waitForHTTPRunStatus(t, service, created.Run.ID, chat.RunCompleted)

	reconnectedReader, reconnectedWriter := io.Pipe()
	go streamEvents(context.Background(), reconnectedWriter, service, created.Run.ID, lastID)
	body, err := io.ReadAll(reconnectedReader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "id: "+strconv.FormatInt(lastID, 10)+"\n") || !strings.Contains(text, "part-2") || !strings.Contains(text, "event: run.completed") {
		t.Fatalf("reconnected SSE body = %q", text)
	}
	messages, _ := service.ListMessages(context.Background(), session.ID, 100)
	if len(messages) != 2 || messages[1].Content != "part-1part-2" {
		t.Fatalf("persisted messages = %#v", messages)
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
	service, err := chat.NewService(context.Background(), chat.NewMemoryRepository(), immediateRunner{}, chat.NewBoundedAssembler(1000, 2, nil), chat.ServiceOptions{DefaultModel: "test"})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func waitForHTTPRunStatus(t *testing.T, service *chat.Service, runID string, want chat.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		run, err := service.GetRun(context.Background(), runID)
		if err == nil && run.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %s did not reach %s", runID, want)
}
