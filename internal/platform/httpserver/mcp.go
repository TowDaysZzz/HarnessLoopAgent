package httpserver

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/mcpfacade"
)

func registerMCPRoute(h *server.Hertz, authService *agentauth.Service, facade *mcpfacade.Facade, cookie AuthCookieConfig) {
	h.POST("/v1/mcp", authenticated(authService, cookie, func(ctx context.Context, c *app.RequestContext) {
		principal, _ := agentauth.PrincipalFromContext(ctx)
		result := facade.Handle(ctx, principal.Role, c.Request.Body())
		c.Response.Header.SetContentTypeBytes([]byte("application/json"))
		c.Response.SetBody(result)
		c.Response.SetStatusCode(consts.StatusOK)
	}))
}
