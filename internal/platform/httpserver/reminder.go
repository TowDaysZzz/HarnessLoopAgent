package httpserver

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

func registerReminderRoutes(h *server.Hertz, authService *agentauth.Service, commands ReminderCommandService, repository reminder.Repository, cookie AuthCookieConfig) {
	protect := func(next app.HandlerFunc) app.HandlerFunc { return authenticated(authService, cookie, next) }
	h.POST("/v1/reminder-commands", protect(func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			Query string `json:"query"`
		}
		if !decodeJSON(c, &request) {
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := commands.Start(ctx, reminderworkflow.StartInput{Owner: workflowOwner(principal), Query: request.Query, IdempotencyKey: string(c.GetHeader("Idempotency-Key"))})
		if err != nil {
			writeReminderError(c, err)
			return
		}
		c.Response.Header.Set("Location", "/v1/reminder-commands/"+result.RunID)
		c.JSON(consts.StatusAccepted, result)
	}))
	h.GET("/v1/reminder-commands/:run_id", protect(func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := commands.Get(ctx, workflowOwner(principal), workflow.WorkflowRunID(c.Param("run_id")))
		if err != nil {
			writeReminderError(c, err)
			return
		}
		c.JSON(consts.StatusOK, result)
	}))
	h.POST("/v1/reminder-commands/:run_id/resume", protect(func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			WaitID      string `json:"wait_id"`
			Version     uint64 `json:"version"`
			ContentHash string `json:"content_hash"`
			Action      string `json:"action"`
			Edit        string `json:"edit,omitempty"`
		}
		if !decodeJSON(c, &request) {
			return
		}
		action := workflow.HumanAction(request.Action)
		if action != workflow.ActionApprove && action != workflow.ActionReject && action != workflow.ActionSubmitEdit {
			writeError(c, consts.StatusBadRequest, "invalid_reminder_action", "action 必须是 approve、reject 或 submit_edit")
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := commands.Resume(ctx, reminderworkflow.ResumeInput{Owner: workflowOwner(principal), Actor: workflow.ActorRef{Type: "user", ID: strconv.FormatUint(principal.UserID, 10)}, RunID: workflow.WorkflowRunID(c.Param("run_id")), WaitID: workflow.WaitID(request.WaitID), Version: request.Version, ContentHash: request.ContentHash, Action: action, EditText: request.Edit})
		if err != nil {
			writeReminderError(c, err)
			return
		}
		c.JSON(consts.StatusOK, result)
	}))
	h.GET("/v1/reminders", protect(func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		query, err := reminderQueryFromHTTP(principal, c)
		if err != nil {
			writeReminderError(c, err)
			return
		}
		page, err := repository.List(ctx, query)
		if err != nil {
			writeReminderError(c, err)
			return
		}
		c.JSON(consts.StatusOK, page)
	}))
	h.GET("/v1/reminders/:reminder_id", protect(func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		value, err := repository.Get(ctx, reminderOwner(principal), c.Param("reminder_id"))
		if err != nil {
			writeReminderError(c, err)
			return
		}
		c.JSON(consts.StatusOK, value)
	}))
	startMutation := func(cancel bool) app.HandlerFunc {
		return protect(func(ctx context.Context, c *app.RequestContext) {
			var request struct {
				Query      string `json:"query"`
				RowVersion uint64 `json:"row_version"`
			}
			if !decodeJSON(c, &request) {
				return
			}
			if cancel && strings.TrimSpace(request.Query) == "" {
				request.Query = "取消这个提醒"
			}
			principal, _ := agentauth.PrincipalFromContext(ctx)
			result, err := commands.Start(ctx, reminderworkflow.StartInput{Owner: workflowOwner(principal), Query: request.Query, IdempotencyKey: string(c.GetHeader("Idempotency-Key")), TrustedTarget: &reminder.ReminderRef{ID: c.Param("reminder_id"), RowVersion: request.RowVersion}})
			if err != nil {
				writeReminderError(c, err)
				return
			}
			c.JSON(consts.StatusAccepted, result)
		})
	}
	h.PATCH("/v1/reminders/:reminder_id", startMutation(false))
	h.DELETE("/v1/reminders/:reminder_id", startMutation(true))
}

func reminderQueryFromHTTP(principal agentauth.Principal, c *app.RequestContext) (reminder.Query, error) {
	query := reminder.Query{Owner: reminderOwner(principal), Label: string(c.Query("label")), CursorID: string(c.Query("cursor_id")), Limit: parsePositiveInt(string(c.Query("limit")), 20)}
	if query.Limit > reminder.MaxPageSize {
		return reminder.Query{}, reminder.ErrInvalidInput
	}
	for _, value := range strings.Split(string(c.Query("status")), ",") {
		if value = strings.TrimSpace(value); value != "" {
			query.Statuses = append(query.Statuses, reminder.Status(value))
		}
	}
	for raw, target := range map[string]**time.Time{"from": &query.From, "until": &query.Until, "cursor_at": &query.CursorAt} {
		if value := strings.TrimSpace(string(c.Query(raw))); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return reminder.Query{}, reminder.ErrInvalidInput
			}
			parsed = parsed.UTC()
			*target = &parsed
		}
	}
	return query, query.Validate()
}

func reminderOwner(principal agentauth.Principal) reminder.Owner {
	return reminder.Owner{TenantID: principal.TenantID, UserID: principal.UserID}
}

func writeReminderError(c *app.RequestContext, err error) {
	switch {
	case workflow.IsCode(err, workflow.CodeNotFound), errors.Is(err, reminder.ErrNotFound):
		writeError(c, consts.StatusNotFound, "reminder_not_found", "Reminder 不存在")
	case workflow.IsCode(err, workflow.CodeInvalidResume), workflow.IsCode(err, workflow.CodeWaitExpired), workflow.IsCode(err, workflow.CodeStateConflict), workflow.IsCode(err, workflow.CodeClaimConflict), workflow.IsCode(err, workflow.CodeIdempotencyConflict), errors.Is(err, reminder.ErrStateConflict), errors.Is(err, reminder.ErrIdempotencyConflict):
		writeError(c, consts.StatusConflict, "reminder_conflict", "Reminder 状态已变化，请刷新后重试")
	case errors.Is(err, reminder.ErrInvalidInput), errors.Is(err, reminderworkflow.ErrInvalidCommandData), errors.Is(err, reminderworkflow.ErrInvalidEditPayload):
		writeError(c, consts.StatusBadRequest, "invalid_reminder", "Reminder 请求无效")
	default:
		writeError(c, consts.StatusInternalServerError, "internal_error", "服务内部错误")
	}
}
