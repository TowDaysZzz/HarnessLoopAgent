package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

func registerMemoryCaptureRoutes(h *server.Hertz, authService *agentauth.Service, service MemoryCaptureService, cookie AuthCookieConfig) {
	protect := func(next app.HandlerFunc) app.HandlerFunc { return authenticated(authService, cookie, next) }
	h.POST("/v1/memory-captures", protect(func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			Query    string          `json:"query"`
			Owner    json.RawMessage `json:"owner,omitempty"`
			TenantID uint64          `json:"tenant_id,omitempty"`
			UserID   uint64          `json:"user_id,omitempty"`
		}
		if !decodeJSON(c, &request) {
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := service.Start(ctx, memoryworkflow.StartCaptureInput{
			Owner:          workflowOwner(principal),
			Query:          request.Query,
			IdempotencyKey: string(c.GetHeader("Idempotency-Key")),
			Intent:         captureIntent(request.Query),
		})
		if err != nil {
			writeMemoryCaptureError(c, err)
			return
		}
		c.Response.Header.Set("Location", "/v1/memory-captures/"+result.RunID)
		c.JSON(consts.StatusAccepted, result)
	}))

	h.GET("/v1/memory-captures/:run_id", protect(func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := service.Get(ctx, workflowOwner(principal), workflow.WorkflowRunID(c.Param("run_id")))
		if err != nil {
			writeMemoryCaptureError(c, err)
			return
		}
		c.JSON(consts.StatusOK, result)
	}))

	h.GET("/v1/memory-captures/:run_id/review", protect(func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := service.GetReview(ctx, workflowOwner(principal), workflow.WorkflowRunID(c.Param("run_id")))
		if err != nil {
			writeMemoryCaptureError(c, err)
			return
		}
		c.JSON(consts.StatusOK, result)
	}))

	h.POST("/v1/memory-captures/:run_id/resume", protect(func(ctx context.Context, c *app.RequestContext) {
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
			writeError(c, consts.StatusBadRequest, "invalid_memory_action", "action 必须是 approve、reject 或 submit_edit")
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result, err := service.Resume(ctx, memoryworkflow.ResumeCaptureInput{
			Owner:       workflowOwner(principal),
			Actor:       workflow.ActorRef{Type: "user", ID: strconv.FormatUint(principal.UserID, 10)},
			RunID:       workflow.WorkflowRunID(c.Param("run_id")),
			WaitID:      workflow.WaitID(request.WaitID),
			Version:     request.Version,
			ContentHash: request.ContentHash,
			Action:      action,
			EditText:    request.Edit,
		})
		if err != nil {
			writeMemoryCaptureError(c, err)
			return
		}
		c.JSON(consts.StatusOK, result)
	}))
}

func workflowOwner(principal agentauth.Principal) workflow.WorkflowOwner {
	return workflow.WorkflowOwner{TenantID: principal.TenantID, OwnerID: principal.UserID}
}

func captureIntent(query string) memory.IntentAuthority {
	normalized := strings.ToLower(query)
	for _, marker := range []string{"修改", "更改", "纠正", "改成", "update", "correct"} {
		if strings.Contains(normalized, marker) {
			return memory.IntentUserCorrection
		}
	}
	return memory.IntentUserStatement
}

func writeMemoryCaptureError(c *app.RequestContext, err error) {
	switch {
	case workflow.IsCode(err, workflow.CodeNotFound), errors.Is(err, memory.ErrNotFound):
		writeError(c, consts.StatusNotFound, "memory_capture_not_found", "Memory Capture 不存在")
	case workflow.IsCode(err, workflow.CodeInvalidResume),
		workflow.IsCode(err, workflow.CodeWaitExpired),
		workflow.IsCode(err, workflow.CodeStateConflict),
		workflow.IsCode(err, workflow.CodeClaimConflict),
		workflow.IsCode(err, workflow.CodeInvalidState),
		workflow.IsCode(err, workflow.CodeIdempotencyConflict):
		writeError(c, consts.StatusConflict, "memory_capture_conflict", "Memory Capture 状态已变化，请刷新后重试")
	case errors.Is(err, memoryworkflow.ErrInvalidCaptureData), errors.Is(err, memoryworkflow.ErrInvalidEditPayload):
		writeError(c, consts.StatusBadRequest, "invalid_memory_capture", "Memory Capture 请求无效")
	default:
		writeError(c, consts.StatusInternalServerError, "internal_error", "服务内部错误")
	}
}
