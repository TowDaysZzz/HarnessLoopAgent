package intentexecutor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type MemoryCaptureStarter interface {
	Start(context.Context, memoryworkflow.StartCaptureInput) (memoryworkflow.CaptureDTO, error)
}

type MemoryCaptureHandler struct{ Service MemoryCaptureStarter }

func (h MemoryCaptureHandler) Execute(ctx context.Context, input routing.Input) (routing.Result, error) {
	if h.Service == nil {
		return routing.Result{}, errors.New("memory capture service is unavailable")
	}
	result, err := h.Service.Start(ctx, memoryworkflow.StartCaptureInput{Owner: workflow.WorkflowOwner{TenantID: input.Run.TenantID, OwnerID: input.Run.UserID}, Query: input.Content, IdempotencyKey: "chat:" + input.Run.RunID, Intent: memory.IntentUserStatement})
	if err != nil {
		return routing.Result{}, err
	}
	candidate := &routing.WorkflowCandidate{Kind: "memory.capture", RunID: result.RunID, Status: result.Status}
	if result.Review != nil {
		candidate.WaitID, candidate.Version, candidate.ContentHash = result.Review.WaitID, result.Review.Version, result.Review.ContentHash
		candidate.ExpiresAt = &result.Review.ExpiresAt
		candidate.AllowedActions = append([]workflow.HumanAction(nil), result.Review.AllowedActions...)
	}
	return routing.Result{Text: "已生成记忆候选，请审核后提交。", WorkflowCandidate: candidate}, nil
}

type MemoryRecallPlanner interface {
	PlanMemoryRecall(context.Context, string) (memory.StructuredRecallPlan, error)
}
type MemoryRecaller interface {
	Recall(context.Context, memory.RecallRequest, time.Time) (memory.RecallResult, error)
}
type MemoryRecallHandler struct {
	Planner MemoryRecallPlanner
	Recall  MemoryRecaller
	Now     func() time.Time
}

func (h MemoryRecallHandler) Execute(ctx context.Context, input routing.Input) (routing.Result, error) {
	if h.Planner == nil || h.Recall == nil {
		return routing.Result{}, errors.New("memory recall service is unavailable")
	}
	plan, err := h.Planner.PlanMemoryRecall(ctx, input.Content)
	if err != nil {
		return routing.Result{}, err
	}
	if !plan.Executable() {
		return routing.Result{Text: "请提供更明确的记忆范围或事实槽。"}, nil
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	result, err := h.Recall.Recall(ctx, memory.RecallRequest{Owner: memory.Owner{TenantID: input.Run.TenantID, UserID: input.Run.UserID}, Query: input.Content, Scope: memory.Scope{Type: memory.ScopeUser}, Plan: &plan, Target: 5, MaxContextChars: 2000}, now)
	if err != nil {
		return routing.Result{}, err
	}
	if len(result.Items) == 0 {
		return routing.Result{Text: "没有找到符合条件的记忆。"}, nil
	}
	var values []string
	for _, item := range result.Items {
		values = append(values, item.Memory.CanonicalText)
	}
	return routing.Result{Text: strings.Join(values, "\n")}, nil
}

type ReminderCommandStarter interface {
	Start(context.Context, reminderworkflow.StartInput) (reminderworkflow.CommandDTO, error)
}

type ReminderCommandHandler struct{ Service ReminderCommandStarter }

func (h ReminderCommandHandler) Execute(ctx context.Context, input routing.Input) (routing.Result, error) {
	if h.Service == nil {
		return routing.Result{}, errors.New("reminder command service is unavailable")
	}
	result, err := h.Service.Start(ctx, reminderworkflow.StartInput{Owner: workflow.WorkflowOwner{TenantID: input.Run.TenantID, OwnerID: input.Run.UserID}, Query: input.Content, IdempotencyKey: "chat:" + input.Run.RunID})
	if err != nil {
		return routing.Result{}, err
	}
	candidate := reminderCandidate(result)
	text := "已生成提醒候选，请确认绝对时间和内容后提交。"
	if candidate.Clarification != nil && candidate.Clarification.Needed {
		text = candidate.Clarification.Question
	}
	return routing.Result{Text: text, WorkflowCandidate: candidate}, nil
}

type ReminderQuerier interface {
	Query(context.Context, reminder.Owner, string, int) (reminderworkflow.QueryResult, error)
}

type ReminderQueryHandler struct {
	Service ReminderQuerier
	Limit   int
}

func (h ReminderQueryHandler) Execute(ctx context.Context, input routing.Input) (routing.Result, error) {
	if h.Service == nil {
		return routing.Result{}, errors.New("reminder query service is unavailable")
	}
	limit := h.Limit
	if limit <= 0 || limit > reminder.MaxPageSize {
		limit = 20
	}
	result, err := h.Service.Query(ctx, reminder.Owner{TenantID: input.Run.TenantID, UserID: input.Run.UserID}, input.Content, limit)
	if err != nil {
		return routing.Result{}, err
	}
	if result.Clarification != nil {
		return routing.Result{Text: result.Clarification.Question}, nil
	}
	if len(result.Items) == 0 {
		return routing.Result{Text: "没有找到符合条件的提醒。"}, nil
	}
	return routing.Result{Text: fmt.Sprintf("找到 %d 条提醒。", len(result.Items))}, nil
}

func reminderCandidate(result reminderworkflow.CommandDTO) *routing.WorkflowCandidate {
	candidate := &routing.WorkflowCandidate{Kind: "reminder.command", RunID: result.RunID, Status: result.Status}
	if review := result.Review; review != nil {
		candidate.WaitID, candidate.Version, candidate.ContentHash = review.WaitID, review.Version, review.ContentHash
		candidate.ExpiresAt = &review.ExpiresAt
		candidate.AllowedActions = append([]workflow.HumanAction(nil), review.AllowedActions...)
		candidate.Action, candidate.Content, candidate.ScheduledAt, candidate.Timezone = review.Action, review.Content, review.ScheduledAt, review.Timezone
		candidate.Target = review.Target
		candidate.TargetChoices = append([]reminder.ReminderRef(nil), review.TargetChoices...)
		candidate.MemorySummary = append([]reminder.MemorySummary(nil), review.MemorySummary...)
		candidate.Clarification = review.Clarification
	}
	return candidate
}
