package httpserver

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type Server struct {
	hertz *server.Hertz
}

func New(addr string, ready func() bool) *Server {
	h := server.Default(
		server.WithHostPorts(addr),
		server.WithHandleMethodNotAllowed(true),
		server.WithReadTimeout(5*time.Second),
	)
	h.GET("/healthz", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]string{"status": "ok"})
	})
	h.GET("/readyz", func(ctx context.Context, c *app.RequestContext) {
		if !ready() {
			c.JSON(consts.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		c.JSON(consts.StatusOK, map[string]string{"status": "ready"})
	})

	return &Server{hertz: h}
}

func (s *Server) Run() error {
	return s.hertz.Run()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.hertz.Shutdown(ctx)
}

func (s *Server) Hertz() *server.Hertz {
	return s.hertz
}
