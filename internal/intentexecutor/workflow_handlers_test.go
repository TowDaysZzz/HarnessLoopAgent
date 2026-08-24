package intentexecutor

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type countingMemoryStarter struct{ calls atomic.Int32 }

func (s *countingMemoryStarter) Start(context.Context, memoryworkflow.StartCaptureInput) (memoryworkflow.CaptureDTO, error) {
	s.calls.Add(1)
	return memoryworkflow.CaptureDTO{RunID: "memory-run", Status: string(workflow.RunSuspended), Review: &memoryworkflow.ReviewDTO{WaitID: "memory-wait", Version: 1, ContentHash: "memory-hash", ExpiresAt: time.Now().Add(time.Hour), AllowedActions: []workflow.HumanAction{workflow.ActionApprove}}}, nil
}

type countingReminderStarter struct{ calls atomic.Int32 }

func (s *countingReminderStarter) Start(context.Context, reminderworkflow.StartInput) (reminderworkflow.CommandDTO, error) {
	s.calls.Add(1)
	at := time.Now().Add(time.Hour).UTC()
	return reminderworkflow.CommandDTO{RunID: "reminder-run", Status: string(workflow.RunSuspended), Review: &reminderworkflow.ReviewDTO{WaitID: "reminder-wait", Version: 1, ContentHash: "reminder-hash", ExpiresAt: at, AllowedActions: []workflow.HumanAction{workflow.ActionApprove}, Action: reminder.ActionCreate, Content: "提交周报", ScheduledAt: &at, Timezone: reminder.DefaultTimezone, MemorySummary: []reminder.MemorySummary{{ID: "memory-1", LineageVersion: 2, ContentHash: strings.Repeat("a", 64), UntrustedText: "[UNTRUSTED_MEMORY_SUMMARY] 简洁格式 [/UNTRUSTED_MEMORY_SUMMARY]"}}}}, nil
}

func TestFacadeStartsAtMostOneWriteWorkflowAndChatCompletesOutsideReviewWait(t *testing.T) {
	memoryStarter := &countingMemoryStarter{}
	reminderStarter := &countingReminderStarter{}
	facade, err := routing.NewFacade(routing.HandlerSet{MemoryCapture: MemoryCaptureHandler{Service: memoryStarter}, ReminderCreate: ReminderCommandHandler{Service: reminderStarter}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := facade.Execute(context.Background(), routing.Input{Run: routing.RunContext{RunID: "chat-run", TenantID: 1, UserID: 2, Decision: routing.RouteDecision{Intent: routing.IntentReminderCreate}}, Content: "提醒我明天九点提交周报"})
	if err != nil {
		t.Fatal(err)
	}
	var candidate, completed bool
	var candidateJSON string
	for event := range execution.Events {
		candidate = candidate || event.Type == agent.EventWorkflowCandidate
		if event.Type == agent.EventWorkflowCandidate {
			candidateJSON = event.Delta
		}
		completed = completed || event.Type == agent.EventRunCompleted
	}
	if !candidate || !completed || reminderStarter.calls.Load() != 1 || memoryStarter.calls.Load() != 0 {
		t.Fatalf("candidate=%v completed=%v reminder=%d memory=%d", candidate, completed, reminderStarter.calls.Load(), memoryStarter.calls.Load())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(candidateJSON), &payload); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"scheduled_at", "timezone", "wait_id", "version", "content_hash", "allowed_actions", "memory_summary"} {
		if _, ok := payload[required]; !ok {
			t.Fatalf("candidate missing %s: %s", required, candidateJSON)
		}
	}
	for _, forbidden := range []string{"owner", "tenant", "user_id", "token", "query", "chat_history"} {
		if strings.Contains(strings.ToLower(candidateJSON), forbidden) {
			t.Fatalf("candidate leaks %q: %s", forbidden, candidateJSON)
		}
	}
}
