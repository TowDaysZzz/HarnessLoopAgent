package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

func registerAuthRoutes(h *server.Hertz, service *agentauth.Service, cookie AuthCookieConfig) {
	h.POST("/v1/auth/register", func(ctx context.Context, c *app.RequestContext) {
		var request ragclient.RegisterRequest
		if !decodeJSON(c, &request) {
			return
		}
		sessionID, principal, err := service.Register(ctx, request)
		if err != nil {
			writeAuthError(c, err)
			return
		}
		setSessionCookie(c, cookie, sessionID)
		c.JSON(consts.StatusCreated, publicPrincipal(principal))
	})
	h.POST("/v1/auth/login", func(ctx context.Context, c *app.RequestContext) {
		var request ragclient.LoginRequest
		if !decodeJSON(c, &request) {
			return
		}
		sessionID, principal, err := service.Login(ctx, request)
		if err != nil {
			writeAuthError(c, err)
			return
		}
		setSessionCookie(c, cookie, sessionID)
		c.JSON(consts.StatusOK, publicPrincipal(principal))
	})
	h.POST("/v1/auth/refresh", func(ctx context.Context, c *app.RequestContext) {
		principal, err := service.Refresh(ctx, rawSessionCookie(c, cookie))
		if err != nil {
			clearSessionCookie(c, cookie)
			writeAuthError(c, err)
			return
		}
		c.JSON(consts.StatusOK, publicPrincipal(principal))
	})
	h.POST("/v1/auth/logout", func(ctx context.Context, c *app.RequestContext) {
		_ = service.Logout(ctx, rawSessionCookie(c, cookie))
		clearSessionCookie(c, cookie)
		c.Status(consts.StatusNoContent)
	})
	h.GET("/v1/auth/me", authenticated(service, cookie, func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		c.JSON(consts.StatusOK, publicPrincipal(principal))
	}))
}

func registerNoteRoutes(h *server.Hertz, authService *agentauth.Service, service *note.Service, cookie AuthCookieConfig) {
	h.POST("/v1/notes", authenticated(authService, cookie, func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			Title      string     `json:"title"`
			Content    string     `json:"content"`
			Tags       []string   `json:"tags"`
			OccurredAt *time.Time `json:"occurred_at"`
		}
		if !decodeJSON(c, &request) {
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		created, replayed, err := service.Create(ctx, principal, note.CreateInput{
			Title: request.Title, Content: request.Content, Tags: request.Tags, OccurredAt: request.OccurredAt,
			IdempotencyKey: string(c.GetHeader("Idempotency-Key")),
		})
		if err != nil {
			writeNoteError(c, err)
			return
		}
		dispatchProjection(service, principal)
		c.JSON(consts.StatusAccepted, map[string]any{"note": created, "idempotent_replay": replayed})
	}))
	h.GET("/v1/notes", authenticated(authService, cookie, func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		limit, _ := strconv.Atoi(string(c.Query("limit")))
		items, err := service.List(ctx, principal, limit, string(c.Query("cursor")))
		if err != nil {
			writeNoteError(c, err)
			return
		}
		if items == nil {
			items = []note.Note{}
		}
		c.JSON(consts.StatusOK, map[string]any{"items": items})
	}))
	h.GET("/v1/notes/:note_id", authenticated(authService, cookie, getNoteHandler(service, false)))
	h.GET("/v1/notes/:note_id/status", authenticated(authService, cookie, getNoteHandler(service, true)))
	h.DELETE("/v1/notes/:note_id", authenticated(authService, cookie, func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		value, replayed, err := service.Delete(ctx, principal, c.Param("note_id"), string(c.GetHeader("Idempotency-Key")))
		if err != nil {
			writeNoteError(c, err)
			return
		}
		dispatchProjection(service, principal)
		c.JSON(consts.StatusAccepted, map[string]any{"note": value, "idempotent_replay": replayed})
	}))
}

func getNoteHandler(service *note.Service, dispatch bool) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		if dispatch {
			dispatchProjection(service, principal)
		}
		value, err := service.RefreshStatus(ctx, principal, c.Param("note_id"))
		if err != nil {
			writeNoteError(c, err)
			return
		}
		c.JSON(consts.StatusOK, value)
	}
}

func authenticated(service *agentauth.Service, cookie AuthCookieConfig, next app.HandlerFunc) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		principal, err := service.Resolve(ctx, rawSessionCookie(c, cookie))
		if err != nil {
			clearSessionCookie(c, cookie)
			writeAuthError(c, err)
			return
		}
		next(agentauth.WithPrincipal(ctx, principal), c)
	}
}

func dispatchProjection(service *note.Service, principal agentauth.Principal) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = service.ProjectPending(ctx, principal, 5)
	}()
}

func decodeJSON(c *app.RequestContext, target any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(c.Request.Body())))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(c, consts.StatusBadRequest, "invalid_json", "请求体必须是合法 JSON，且不能包含未知字段")
		return false
	}
	return true
}

func rawSessionCookie(c *app.RequestContext, config AuthCookieConfig) string {
	return string(c.Cookie(config.Name))
}

func setSessionCookie(c *app.RequestContext, config AuthCookieConfig, value string) {
	maxAge := int(config.MaxAge.Seconds())
	c.SetCookie(config.Name, value, maxAge, "/", "", protocol.CookieSameSiteLaxMode, config.Secure, true)
}

func clearSessionCookie(c *app.RequestContext, config AuthCookieConfig) {
	c.SetCookie(config.Name, "", -1, "/", "", protocol.CookieSameSiteLaxMode, config.Secure, true)
}

func publicPrincipal(principal agentauth.Principal) map[string]any {
	return map[string]any{"user_id": principal.UserID, "tenant_id": principal.TenantID, "role": principal.Role, "email": principal.Email, "name": principal.Name}
}

func writeAuthError(c *app.RequestContext, err error) {
	var apiErr *ragclient.APIError
	switch {
	case errors.Is(err, agentauth.ErrUnauthenticated):
		writeError(c, consts.StatusUnauthorized, "unauthorized", "登录状态已失效")
	case errors.Is(err, agentauth.ErrInvalidInput):
		writeError(c, consts.StatusBadRequest, "invalid_request", "邮箱和密码不能为空")
	case errors.As(err, &apiErr) && (apiErr.HTTPStatus == 400 || apiErr.HTTPStatus == 401 || apiErr.HTTPStatus == 409):
		writeError(c, apiErr.HTTPStatus, "rag_auth_rejected", apiErr.Message)
	default:
		writeError(c, consts.StatusBadGateway, "auth_upstream_error", "认证服务暂时不可用")
	}
}

func writeNoteError(c *app.RequestContext, err error) {
	var apiErr *ragclient.APIError
	switch {
	case errors.Is(err, note.ErrNotFound):
		writeError(c, consts.StatusNotFound, "note_not_found", "笔记不存在")
	case errors.Is(err, note.ErrInvalidInput):
		writeError(c, consts.StatusBadRequest, "invalid_note", "笔记标题、内容和 Idempotency-Key 必填")
	case errors.Is(err, note.ErrConflict):
		writeError(c, consts.StatusConflict, "idempotency_conflict", "Idempotency-Key 已用于不同内容的笔记")
	case errors.Is(err, agentauth.ErrUnauthenticated):
		writeError(c, consts.StatusUnauthorized, "unauthorized", "登录状态已失效")
	case errors.As(err, &apiErr):
		writeError(c, consts.StatusBadGateway, "rag_projection_error", "笔记搜索索引暂时不可用")
	default:
		writeError(c, consts.StatusInternalServerError, "internal_error", "服务内部错误")
	}
}
