package reminderworkflow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
)

func TestReadOnlyQueryServiceListsScheduledTimeWindowAndZeroHits(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tomorrowStart := time.Date(2026, 8, 25, 0, 0, 0, 0, time.FixedZone("+08", 8*60*60)).UTC()
	tomorrowEnd := tomorrowStart.Add(24 * time.Hour)
	planner := scriptedPlanner{plans: map[string]reminder.CommandPlan{
		"有哪些提醒":   {Version: reminder.CommandPlanVersion, Action: reminder.ActionQuery, Target: &reminder.TargetSelector{Statuses: []reminder.Status{reminder.StatusScheduled}}, Confidence: 1},
		"明天有什么提醒": {Version: reminder.CommandPlanVersion, Action: reminder.ActionQuery, Target: &reminder.TargetSelector{Statuses: []reminder.Status{reminder.StatusScheduled}, From: tomorrowStart.Format(time.RFC3339), Until: tomorrowEnd.Format(time.RFC3339)}, Confidence: 1},
		"没有的提醒":   {Version: reminder.CommandPlanVersion, Action: reminder.ActionQuery, Target: &reminder.TargetSelector{Label: "不存在"}, Confidence: 1},
	}}
	repo := reminder.NewFakeRepository()
	owner := reminder.Owner{TenantID: 1, UserID: 2}
	seedQueryReminder(t, repo, owner, "a", "明天周报", tomorrowStart.Add(time.Hour), now)
	seedQueryReminder(t, repo, owner, "b", "后天会议", tomorrowEnd.Add(time.Hour), now)
	service := QueryService{Planner: planner, Repository: repo, Now: func() time.Time { return now }}

	all, err := service.Query(context.Background(), owner, "有哪些提醒", 10)
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	tomorrow, err := service.Query(context.Background(), owner, "明天有什么提醒", 10)
	if err != nil || len(tomorrow.Items) != 1 || tomorrow.Items[0].Content != "明天周报" {
		t.Fatalf("tomorrow=%+v err=%v", tomorrow, err)
	}
	empty, err := service.Query(context.Background(), owner, "没有的提醒", 10)
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

func TestReadOnlyQueryServiceClarifiesMultipleMutationTargets(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	planner := scriptedPlanner{plans: map[string]reminder.CommandPlan{
		"把周报提醒改到十点": mutationPlan(reminder.ActionUpdate, "周报", now.Add(22*time.Hour)),
	}}
	planner.plans["把周报提醒改到十点"] = func() reminder.CommandPlan {
		plan := planner.plans["把周报提醒改到十点"]
		plan.Target.Label = "周报"
		return plan
	}()
	repo := reminder.NewFakeRepository()
	owner := reminder.Owner{TenantID: 3, UserID: 4}
	seedQueryReminder(t, repo, owner, "c", "研发周报", now.Add(time.Hour), now)
	seedQueryReminder(t, repo, owner, "d", "销售周报", now.Add(2*time.Hour), now)
	service := QueryService{Planner: planner, Repository: repo, Now: func() time.Time { return now }}
	result, err := service.ResolveMutationTarget(context.Background(), owner, "把周报提醒改到十点")
	if err != nil || result.Target != nil || len(result.Choices) != 2 || result.Clarification == nil || !result.Clarification.Needed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func seedQueryReminder(t *testing.T, repo reminder.Repository, owner reminder.Owner, suffix, content string, fireAt, now time.Time) reminder.Reminder {
	t.Helper()
	hash, err := reminder.ComputeContentHash(content, reminder.DefaultTimezone, fireAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := reminder.Reminder{ID: "reminder-" + suffix, Owner: owner, Content: content, ContentHash: hash, Timezone: reminder.DefaultTimezone, NextFireAt: fireAt, Status: reminder.StatusScheduled, RowVersion: 1, Source: reminder.SourceRef{Type: "test", ID: suffix}, CreatedAt: now, UpdatedAt: now}
	result, err := repo.Create(context.Background(), reminder.CreateInput{Reminder: value, IdempotencyKey: "seed-" + suffix, InputHash: hash, Actor: "test", ReasonCode: "seed"})
	if err != nil {
		t.Fatalf("seed %s: %v", suffix, err)
	}
	if result.Reminder.ID != value.ID {
		t.Fatal(fmt.Sprintf("seed id=%s", result.Reminder.ID))
	}
	return result.Reminder
}
