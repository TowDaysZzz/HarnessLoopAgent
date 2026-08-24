package reminderworkflow

import (
	"context"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
)

type QueryService struct {
	Planner    Planner
	Repository reminder.Repository
	Now        func() time.Time
}

type QueryResult struct {
	Items         []reminder.Reminder     `json:"items"`
	NextAt        *time.Time              `json:"next_at,omitempty"`
	NextID        string                  `json:"next_id,omitempty"`
	Truncated     bool                    `json:"truncated"`
	Target        *reminder.ReminderRef   `json:"target,omitempty"`
	Choices       []reminder.ReminderRef  `json:"choices,omitempty"`
	Clarification *reminder.Clarification `json:"clarification,omitempty"`
}

func (s QueryService) Query(ctx context.Context, owner reminder.Owner, text string, limit int) (QueryResult, error) {
	if s.Planner == nil || s.Repository == nil || !owner.Valid() || limit < 1 || limit > reminder.MaxPageSize {
		return QueryResult{}, ErrInvalidCommandData
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	plan, err := s.Planner.Plan(ctx, text, now)
	if err != nil {
		return QueryResult{}, err
	}
	if plan.Action != reminder.ActionQuery || plan.Target == nil {
		return QueryResult{}, ErrInvalidCommandData
	}
	if plan.Clarification != nil && plan.Clarification.Needed {
		return QueryResult{Clarification: plan.Clarification}, nil
	}
	query, err := targetQuery(owner, plan.Target)
	if err != nil {
		return QueryResult{}, err
	}
	query.Limit = limit
	page, err := s.Repository.List(ctx, query)
	if err != nil {
		return QueryResult{}, err
	}
	return QueryResult{Items: page.Items, NextAt: page.NextAt, NextID: page.NextID, Truncated: page.Truncated}, nil
}

func (s QueryService) ResolveMutationTarget(ctx context.Context, owner reminder.Owner, text string) (QueryResult, error) {
	if s.Planner == nil || s.Repository == nil || !owner.Valid() {
		return QueryResult{}, ErrInvalidCommandData
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	plan, err := s.Planner.Plan(ctx, text, now)
	if err != nil {
		return QueryResult{}, err
	}
	if plan.Action != reminder.ActionUpdate && plan.Action != reminder.ActionCancel || plan.Target == nil {
		return QueryResult{}, ErrInvalidCommandData
	}
	query, err := targetQuery(owner, plan.Target)
	if err != nil {
		return QueryResult{}, err
	}
	page, err := s.Repository.List(ctx, query)
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Choices: make([]reminder.ReminderRef, 0, len(page.Items))}
	for _, item := range page.Items {
		result.Choices = append(result.Choices, reminder.ReminderRef{ID: item.ID, RowVersion: item.RowVersion})
	}
	if len(result.Choices) == 1 {
		result.Target = &result.Choices[0]
	} else {
		result.Clarification = &reminder.Clarification{Needed: true, Reason: "reminder_target_not_unique", Question: "请从候选中选择一个明确的提醒。"}
	}
	return result, nil
}
