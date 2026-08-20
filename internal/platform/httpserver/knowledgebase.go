package httpserver

import (
	"context"
	"errors"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
)

func registerKnowledgeBaseRoutes(h *server.Hertz, authService *agentauth.Service, service *knowledgebase.Service, cookie AuthCookieConfig) {
	h.GET("/v1/knowledge-base", authenticated(authService, cookie, func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		binding, err := service.Get(ctx, principal)
		if errors.Is(err, knowledgebase.ErrNotConfigured) {
			c.JSON(consts.StatusOK, map[string]any{"configured": false})
			return
		}
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(consts.StatusOK, map[string]any{"configured": true, "knowledge_base": binding})
	}))

	h.POST("/v1/knowledge-base", authenticated(authService, cookie, func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			Name string `json:"name"`
		}
		if len(c.Request.Body()) > 0 && !decodeJSON(c, &request) {
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		binding, created, err := service.Ensure(ctx, principal, request.Name)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		status := consts.StatusOK
		if created {
			status = consts.StatusCreated
		}
		c.JSON(status, map[string]any{"configured": true, "created": created, "knowledge_base": binding})
	}))
}
