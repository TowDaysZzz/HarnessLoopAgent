package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	einoagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent/eino"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/intentexecutor"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/mcpfacade"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/httpserver"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/tools"
)

func main() {
	if err := run(); err != nil {
		log.Printf("note agent stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agentRunner, err := einoagent.NewConfiguredRunner(ctx, cfg)
	if err != nil {
		return err
	}

	var serverOptions []httpserver.Option
	var store *mysqlstore.Store
	if cfg.Database.Enabled {
		store, err = mysqlstore.Open(ctx, mysqlstore.Options{
			DSN: cfg.Database.DSN, MaxOpenConns: cfg.Database.MaxOpenConns,
			MaxIdleConns: cfg.Database.MaxIdleConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		})
		if err != nil {
			return err
		}
		defer store.Close()
		if cfg.Database.AutoMigrate {
			if err := store.Migrate(ctx); err != nil {
				return err
			}
		}
		var noteService *note.Service
		if cfg.Auth.Enabled {
			rag, err := ragclient.NewClient(ragclient.ClientConfig{BaseURL: cfg.RAG.BaseURL, APIKey: cfg.RAG.APIKey, Timeout: cfg.RAG.Timeout})
			if err != nil {
				return err
			}
			authService, err := agentauth.NewService(store, rag, cfg.Auth.SessionSecret, cfg.Auth.SessionTTL)
			if err != nil {
				return err
			}
			serverOptions = append(serverOptions, httpserver.WithAuthService(authService, httpserver.AuthCookieConfig{
				Name: cfg.Auth.CookieName, Secure: cfg.Auth.CookieSecure, MaxAge: cfg.Auth.SessionTTL,
			}))
			knowledgeBaseService, err := knowledgebase.NewService(store, rag)
			if err != nil {
				return err
			}
			serverOptions = append(serverOptions, httpserver.WithKnowledgeBaseService(knowledgeBaseService))
			if cfg.Note.Enabled {
				noteService, err = note.NewServiceWithResolver(store, rag, knowledgeBaseService)
				if err != nil {
					return err
				}
				serverOptions = append(serverOptions, httpserver.WithNoteService(noteService))
				registry := tools.NewRegistry()
				_ = registry.Register(tools.Definition{Name: "notes.list", Description: "List the authenticated user's notes", Roles: []string{"*"}, ReadOnly: true, Handler: func(toolCtx context.Context, _ []byte) ([]byte, error) {
					principal, ok := agentauth.PrincipalFromContext(toolCtx)
					if !ok {
						return nil, agentauth.ErrUnauthenticated
					}
					items, err := noteService.List(toolCtx, principal, 20, "")
					if err != nil {
						return nil, err
					}
					return json.Marshal(map[string]any{"items": items})
				}})
				facade, err := mcpfacade.New(registry)
				if err != nil {
					return err
				}
				serverOptions = append(serverOptions, httpserver.WithMCPFacade(facade))
			}
		}

		assembler := contextmanager.NewBoundedAssembler(
			cfg.Context.MaxInputTokens, cfg.Context.MinRecentMessages, contextmanager.ApproxTokenCounter{},
		)
		draftService, err := notedraft.NewService(store, cfg.Agent.NoteDraftTTL)
		if err != nil {
			return err
		}
		complexHandler, err := routing.NewComplexHandler(agentRunner, cfg.Agent.RunTimeout, cfg.Agent.MaxIterations)
		if err != nil {
			return err
		}
		var noteCreateHandler routing.DeterministicHandler = routing.StaticTextHandler{Text: "当前服务未启用笔记写入，请联系管理员检查 NOTE 配置。"}
		if noteService != nil {
			noteCreateHandler = intentexecutor.NoteCreateHandler{
				Notes: noteService, Projector: noteService, Drafts: draftService, Summarizer: intentexecutor.RunnerSummarizer{Runner: agentRunner},
			}
		}
		executor, err := routing.NewFacade(routing.HandlerSet{
			NoteCreate: noteCreateHandler, Clarification: routing.ClarificationHandler{}, DeleteRejected: routing.DeleteRejectedHandler{},
			SimpleChat: routing.ConversationHandler{Runner: agentRunner}, SimpleNoteQuery: routing.ConversationHandler{Runner: agentRunner},
			ComplexChat: complexHandler, ComplexNoteQuery: complexHandler,
		})
		if err != nil {
			return err
		}
		intentRouter := routing.Router{Classifier: routing.Classifier{
			ComplexThreshold: cfg.Agent.IntentComplexThreshold, MinWriteConfidence: cfg.Agent.IntentMinWriteConfidence,
		}, Drafts: draftService}
		chatService, err := chat.NewService(ctx, store, agentRunner, assembler, chat.ServiceOptions{
			MessageHistoryLimit: cfg.Context.MessageHistoryLimit, DefaultModel: cfg.ActiveModel,
			EnableIntentRouting: cfg.Agent.EnableIntentRouting, EnableLegacyRoutingFallback: cfg.Agent.EnableLegacyRoutingFallback,
			Router: intentRouter, Executor: executor,
		})
		if err != nil {
			return err
		}
		serverOptions = append(serverOptions, httpserver.WithChatService(chatService))
	}

	httpServer := httpserver.New(cfg.HTTPAddr, func() bool { return agentRunner != nil && (!cfg.Database.Enabled || store != nil) }, serverOptions...)
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("note agent listening on %s with Hertz", cfg.HTTPAddr)
		serverErr <- httpServer.Run()
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serverErr
	}
}
