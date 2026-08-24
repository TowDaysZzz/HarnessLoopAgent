package httpserver

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type reminderHTTPFake struct {
	started reminderworkflow.StartInput
	resumed reminderworkflow.ResumeInput
	error   error
}

func (f *reminderHTTPFake) Start(_ context.Context, input reminderworkflow.StartInput) (reminderworkflow.CommandDTO, error) {
	f.started = input
	if f.error != nil {
		return reminderworkflow.CommandDTO{}, f.error
	}
	return reminderworkflow.CommandDTO{RunID: "reminder-run", Status: string(workflow.RunSuspended)}, nil
}
func (f *reminderHTTPFake) Get(_ context.Context, owner workflow.WorkflowOwner, _ workflow.WorkflowRunID) (reminderworkflow.CommandDTO, error) {
	if owner.OwnerID != 73 {
		return reminderworkflow.CommandDTO{}, workflow.ErrNotFound
	}
	return reminderworkflow.CommandDTO{RunID: "reminder-run", Status: string(workflow.RunSuspended)}, nil
}
func (f *reminderHTTPFake) Resume(_ context.Context, input reminderworkflow.ResumeInput) (reminderworkflow.CommandDTO, error) {
	f.resumed = input
	if f.error != nil {
		return reminderworkflow.CommandDTO{}, f.error
	}
	return reminderworkflow.CommandDTO{RunID: string(input.RunID), Status: string(workflow.RunCompleted)}, nil
}

func TestReminderHTTPUsesPrincipalAndEnforcesPaginationConflictAndIsolation(t *testing.T) {
	authService, cookieHeader, cookie := newMemoryHTTPAuth(t)
	repo := reminder.NewFakeRepository()
	fake := &reminderHTTPFake{}
	server := New(":0", func() bool { return true }, WithAuthService(authService, cookie), WithReminderServices(fake, repo))
	status, _ := performMemoryRequest(server, consts.MethodPost, "/v1/reminder-commands", `{"query":"提醒我明天九点提交周报"}`, cookieHeader, ut.Header{Key: "Idempotency-Key", Value: "reminder-key"})
	if status != consts.StatusAccepted || fake.started.Owner != (workflow.WorkflowOwner{TenantID: 19, OwnerID: 73}) || fake.started.IdempotencyKey != "reminder-key" {
		t.Fatalf("status=%d input=%+v", status, fake.started)
	}
	status, _ = performMemoryRequest(server, consts.MethodGet, "/v1/reminders?limit=101", "", cookieHeader)
	if status != consts.StatusBadRequest {
		t.Fatalf("limit status=%d", status)
	}

	now := time.Now().UTC()
	hash, _ := reminder.ComputeContentHash("别人的提醒", reminder.DefaultTimezone, now.Add(time.Hour), nil)
	_, err := repo.Create(context.Background(), reminder.CreateInput{Reminder: reminder.Reminder{ID: "foreign", Owner: reminder.Owner{TenantID: 19, UserID: 999}, Content: "别人的提醒", ContentHash: hash, Timezone: reminder.DefaultTimezone, NextFireAt: now.Add(time.Hour), Status: reminder.StatusScheduled, RowVersion: 1, Source: reminder.SourceRef{Type: "test", ID: "foreign"}, CreatedAt: now, UpdatedAt: now}, IdempotencyKey: "foreign", InputHash: hash, Actor: "test", ReasonCode: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	status, _ = performMemoryRequest(server, consts.MethodGet, "/v1/reminders/foreign", "", cookieHeader)
	if status != consts.StatusNotFound {
		t.Fatalf("cross-owner status=%d", status)
	}

	fake.error = reminder.ErrStateConflict
	status, _ = performMemoryRequest(server, consts.MethodPost, "/v1/reminder-commands/reminder-run/resume", `{"wait_id":"wait","version":1,"content_hash":"hash","action":"approve"}`, cookieHeader)
	if status != consts.StatusConflict {
		t.Fatalf("conflict status=%d", status)
	}
}
