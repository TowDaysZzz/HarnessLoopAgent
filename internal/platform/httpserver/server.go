package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/mcpfacade"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type Server struct {
	hertz *server.Hertz
}

type Option func(*serverOptions)

type serverOptions struct {
	chat            *chat.Service
	auth            *agentauth.Service
	note            *note.Service
	mcp             *mcpfacade.Facade
	knowledgeBase   *knowledgebase.Service
	memoryCapture   MemoryCaptureService
	reminderCommand ReminderCommandService
	reminderRepo    reminder.Repository
	authCookie      AuthCookieConfig
}

type MemoryCaptureService interface {
	Start(context.Context, memoryworkflow.StartCaptureInput) (memoryworkflow.CaptureDTO, error)
	Get(context.Context, workflow.WorkflowOwner, workflow.WorkflowRunID) (memoryworkflow.CaptureDTO, error)
	GetReview(context.Context, workflow.WorkflowOwner, workflow.WorkflowRunID) (memoryworkflow.ReviewDTO, error)
	Resume(context.Context, memoryworkflow.ResumeCaptureInput) (memoryworkflow.CaptureDTO, error)
}

type ReminderCommandService interface {
	Start(context.Context, reminderworkflow.StartInput) (reminderworkflow.CommandDTO, error)
	Get(context.Context, workflow.WorkflowOwner, workflow.WorkflowRunID) (reminderworkflow.CommandDTO, error)
	Resume(context.Context, reminderworkflow.ResumeInput) (reminderworkflow.CommandDTO, error)
}

type AuthCookieConfig struct {
	Name   string
	Secure bool
	MaxAge time.Duration
}

func WithChatService(service *chat.Service) Option {
	return func(options *serverOptions) { options.chat = service }
}

func WithAuthService(service *agentauth.Service, cookie AuthCookieConfig) Option {
	return func(options *serverOptions) {
		options.auth = service
		options.authCookie = cookie
	}
}

func WithNoteService(service *note.Service) Option {
	return func(options *serverOptions) { options.note = service }
}

func WithMCPFacade(facade *mcpfacade.Facade) Option {
	return func(options *serverOptions) { options.mcp = facade }
}

func WithKnowledgeBaseService(service *knowledgebase.Service) Option {
	return func(options *serverOptions) { options.knowledgeBase = service }
}

func WithMemoryCaptureService(service MemoryCaptureService) Option {
	return func(options *serverOptions) { options.memoryCapture = service }
}

func WithReminderServices(command ReminderCommandService, repository reminder.Repository) Option {
	return func(options *serverOptions) {
		options.reminderCommand, options.reminderRepo = command, repository
	}
}

func WithMemoryChatIntentPilot(enabled bool) Option {
	// Deprecated compatibility option. Chat side effects are owned by the
	// routing Executor; the HTTP transport never starts a Memory workflow.
	return func(options *serverOptions) {}
}

func New(addr string, ready func() bool, options ...Option) *Server {
	var configured serverOptions
	for _, option := range options {
		option(&configured)
	}
	h := server.Default(
		server.WithHostPorts(addr),
		server.WithHandleMethodNotAllowed(true),
		server.WithReadTimeout(5*time.Second),
	)
	h.GET("/healthz", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]string{"status": "ok"})
	})
	if configured.chat != nil {
		registerChatRoutes(h, configured.chat, configured.auth, configured.authCookie, configured.knowledgeBase)
	}
	if configured.auth != nil {
		registerAuthRoutes(h, configured.auth, configured.authCookie)
		if configured.knowledgeBase != nil {
			registerKnowledgeBaseRoutes(h, configured.auth, configured.knowledgeBase, configured.authCookie)
		}
		if configured.note != nil {
			registerNoteRoutes(h, configured.auth, configured.note, configured.authCookie)
		}
		if configured.mcp != nil {
			registerMCPRoute(h, configured.auth, configured.mcp, configured.authCookie)
		}
		if configured.memoryCapture != nil {
			registerMemoryCaptureRoutes(h, configured.auth, configured.memoryCapture, configured.authCookie)
		}
		if configured.reminderCommand != nil && configured.reminderRepo != nil {
			registerReminderRoutes(h, configured.auth, configured.reminderCommand, configured.reminderRepo, configured.authCookie)
		}
	}
	h.GET("/readyz", func(ctx context.Context, c *app.RequestContext) {
		if !ready() {
			c.JSON(consts.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		c.JSON(consts.StatusOK, map[string]string{"status": "ready"})
	})

	return &Server{hertz: h}
}

func registerChatRoutes(h *server.Hertz, service *chat.Service, authService *agentauth.Service, cookie AuthCookieConfig, knowledgeBaseService *knowledgebase.Service) {
	protect := func(handler app.HandlerFunc) app.HandlerFunc {
		if authService == nil {
			return handler
		}
		return authenticated(authService, cookie, handler)
	}
	h.POST("/v1/sessions", protect(func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			Title string `json:"title"`
		}
		if len(c.Request.Body()) > 0 {
			if err := json.Unmarshal(c.Request.Body(), &request); err != nil {
				writeError(c, consts.StatusBadRequest, "invalid_json", "请求体必须是合法 JSON")
				return
			}
		}
		session, err := service.CreateSession(ctx, request.Title)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(consts.StatusCreated, session)
	}))
	h.GET("/v1/sessions", protect(func(ctx context.Context, c *app.RequestContext) {
		limit := parsePositiveInt(string(c.Query("limit")), 50)
		sessions, err := service.ListSessions(ctx, limit)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		if sessions == nil {
			sessions = []chat.Session{}
		}
		c.JSON(consts.StatusOK, map[string]any{"items": sessions})
	}))

	h.GET("/v1/sessions/:session_id", protect(func(ctx context.Context, c *app.RequestContext) {
		session, err := service.GetSession(ctx, c.Param("session_id"))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(consts.StatusOK, session)
	}))

	h.GET("/v1/sessions/:session_id/messages", protect(func(ctx context.Context, c *app.RequestContext) {
		limit := parsePositiveInt(string(c.Query("limit")), 100)
		messages, err := service.ListMessages(ctx, c.Param("session_id"), limit)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(consts.StatusOK, map[string]any{"items": messages})
	}))

	h.POST("/v1/sessions/:session_id/runs", protect(func(ctx context.Context, c *app.RequestContext) {
		var request struct {
			Message string `json:"message"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(c.Request.Body(), &request); err != nil {
			writeError(c, consts.StatusBadRequest, "invalid_json", "请求体必须是合法 JSON")
			return
		}
		principal, _ := agentauth.PrincipalFromContext(ctx)
		var knowledgeBaseIDs []uint64
		if knowledgeBaseService != nil {
			binding, bindingErr := knowledgeBaseService.Get(ctx, principal)
			if bindingErr != nil {
				if errors.Is(bindingErr, knowledgebase.ErrNotConfigured) {
					writeError(c, consts.StatusConflict, "knowledge_base_required", "请先创建并绑定个人知识库")
					return
				}
				writeServiceError(c, bindingErr)
				return
			}
			knowledgeBaseIDs = []uint64{binding.RAGKBID}
		}
		created, err := service.CreateRun(ctx, chat.CreateRunInput{
			SessionID: c.Param("session_id"), Content: request.Message, Model: request.Model,
			IdempotencyKey: string(c.GetHeader("Idempotency-Key")), UserAccessToken: principal.AccessToken, KnowledgeBaseIDs: knowledgeBaseIDs,
		})
		if err != nil {
			writeServiceError(c, err)
			return
		}
		status := consts.StatusAccepted
		if !created.Created {
			status = consts.StatusOK
		}
		c.JSON(status, map[string]any{
			"run":               created.Run,
			"events_url":        "/v1/runs/" + created.Run.ID + "/events",
			"idempotent_replay": !created.Created,
		})
	}))

	h.GET("/v1/runs/:run_id", protect(func(ctx context.Context, c *app.RequestContext) {
		run, err := service.GetRun(ctx, c.Param("run_id"))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(consts.StatusOK, run)
	}))

	h.POST("/v1/runs/:run_id/cancel", protect(func(ctx context.Context, c *app.RequestContext) {
		run, err := service.CancelRun(ctx, c.Param("run_id"))
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(consts.StatusOK, run)
	}))

	h.GET("/v1/runs/:run_id/events", protect(func(ctx context.Context, c *app.RequestContext) {
		runID := c.Param("run_id")
		if _, err := service.GetRun(ctx, runID); err != nil {
			writeServiceError(c, err)
			return
		}
		after, err := parseLastEventID(string(c.GetHeader("Last-Event-ID")))
		if err != nil {
			writeError(c, consts.StatusBadRequest, "invalid_last_event_id", err.Error())
			return
		}
		reader, writer := io.Pipe()
		c.Response.Header.SetContentTypeBytes([]byte("text/event-stream; charset=utf-8"))
		c.Response.Header.Set("Cache-Control", "no-cache")
		c.Response.Header.Set("X-Accel-Buffering", "no")
		c.Response.SetBodyStream(reader, -1)
		go streamEvents(ctx, writer, service, runID, after)
	}))
}

func streamEvents(ctx context.Context, writer *io.PipeWriter, service *chat.Service, runID string, after int64) {
	defer writer.Close()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		wake, unsubscribe := service.Subscribe(runID)
		events, err := service.ListEvents(ctx, runID, after, 100)
		if err != nil {
			unsubscribe()
			return
		}
		for _, event := range events {
			if err := writeSSE(writer, event); err != nil {
				unsubscribe()
				return
			}
			after = event.Sequence
		}
		run, err := service.GetRun(ctx, runID)
		if err != nil || run.Status.Terminal() {
			unsubscribe()
			return
		}
		select {
		case <-ctx.Done():
			unsubscribe()
			return
		case <-wake:
		case <-heartbeat.C:
			unsubscribe()
			if _, err := io.WriteString(writer, ": keep-alive\n\n"); err != nil {
				return
			}
		}
	}
}

func writeSSE(writer io.Writer, event chat.Event) error {
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, payload)
	return err
}

func parseLastEventID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("Last-Event-ID 必须是非负整数")
	}
	return value, nil
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func writeServiceError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, chat.ErrNotFound):
		writeError(c, consts.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, chat.ErrActiveRun):
		writeError(c, consts.StatusConflict, "active_run_exists", "当前会话已有正在执行的 Run")
	case errors.Is(err, chat.ErrInvalidState):
		writeError(c, consts.StatusConflict, "invalid_run_state", "Run 当前状态不允许该操作")
	case errors.Is(err, chat.ErrInvalidInput):
		writeError(c, consts.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(c, consts.StatusInternalServerError, "internal_error", "服务内部错误")
	}
}

func writeError(c *app.RequestContext, status int, code, message string) {
	c.JSON(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
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
