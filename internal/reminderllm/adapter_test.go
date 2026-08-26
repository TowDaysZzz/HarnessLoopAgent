package reminderllm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type scriptedRunner struct {
	outputs  []string
	messages [][]agent.Message
}

func (r *scriptedRunner) StreamConversation(_ context.Context, request agent.ConversationRequest) <-chan agent.Event {
	messages := request.Messages
	r.messages = append(r.messages, messages)
	out := make(chan agent.Event, 2)
	value := ""
	if len(r.outputs) > 0 {
		value = r.outputs[0]
		r.outputs = r.outputs[1:]
	}
	out <- agent.Event{Type: agent.EventTextDelta, Delta: value}
	out <- agent.Event{Type: agent.EventRunCompleted}
	close(out)
	return out
}

func TestAdapterPlansTomorrowNineAndRepairsInvalidJSON(t *testing.T) {
	valid := `{"version":"v1","action":"create","content":"提交周报","trigger":{"type":"at_time","at":"2026-08-25T09:00:00+08:00","timezone":"Asia/Shanghai"},"confidence":0.95}`
	runner := &scriptedRunner{outputs: []string{`not-json`, valid}}
	adapter, err := New(runner, Config{MaxResponseBytes: 4096, MaxRepairAttempts: 1, MinConfidence: .8, MaxHorizon: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adapter.Plan(context.Background(), "提醒我明天九点提交周报", time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	if err != nil || plan.Content != "提交周报" || len(runner.messages) != 2 {
		t.Fatalf("Plan=%+v calls=%d err=%v", plan, len(runner.messages), err)
	}
	if !strings.Contains(runner.messages[0][1].Content, "ANCHOR_UTC=2026-08-24T12:00:00Z") {
		t.Fatalf("anchor missing: %q", runner.messages[0][1].Content)
	}
}

func TestAdapterRejectsPrivilegedAndMultipleObjects(t *testing.T) {
	for _, output := range []string{`{"version":"v1","action":"query","target_selector":{},"confidence":1,"sql":"select *"}`, `{"version":"v1","action":"query","target_selector":{},"confidence":1} {}`} {
		runner := &scriptedRunner{outputs: []string{output}}
		adapter, _ := New(runner, Config{MaxResponseBytes: 4096, MinConfidence: .8, MaxHorizon: time.Hour})
		if _, err := adapter.Plan(context.Background(), "查询提醒", time.Now()); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
}
