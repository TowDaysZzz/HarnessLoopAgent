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
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/mcpfacade"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/httpserver"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
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
		if cfg.Memory.Enabled && cfg.Memory.WorkflowPilotEnabled {
			return memoryStartupError("structured model")
		}
		return err
	}

	var serverOptions []httpserver.Option
	var store *mysqlstore.Store
	var memoryRuntime *memoryRuntimeAssembly
	var reminderRuntime *reminderRuntimeAssembly
	if cfg.Database.Enabled {
		store, err = mysqlstore.Open(ctx, mysqlstore.Options{
			DSN: cfg.Database.DSN, MaxOpenConns: cfg.Database.MaxOpenConns,
			MaxIdleConns: cfg.Database.MaxIdleConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
			ProjectionVersion: cfg.Memory.ProjectionVersion,
		})
		if err != nil {
			if cfg.Memory.Enabled {
				return memoryStartupError("database")
			}
			return err
		}
		defer store.Close()
		if cfg.Database.AutoMigrate {
			if err := store.Migrate(ctx); err != nil {
				if cfg.Memory.Enabled {
					return memoryStartupError("database migration")
				}
				return err
			}
		}
		if cfg.Memory.Enabled {
			var memoryRAG ragclient.MemoryClient
			if cfg.Memory.RAGEnabled {
				memoryRAG, err = ragclient.NewClient(ragclient.ClientConfig{
					BaseURL: cfg.Memory.RAGEndpoint, APIKey: cfg.Memory.RAGServiceToken,
					OwnerClaimSecret: cfg.Memory.OwnerClaimSecret, Timeout: cfg.Memory.RAGTimeout,
				})
				if err != nil {
					return memoryStartupError("rag")
				}
			}
			var memoryErr error
			memoryRuntime, memoryErr = assembleMemoryRuntime(cfg.Memory, memoryRuntimeDependencies{
				Repository: store, WorkflowStore: store.WorkflowStore(), EditPayloadStore: store,
				ProjectionBacklog: store, Runner: agentRunner, RAG: memoryRAG,
			})
			if memoryErr != nil {
				return memoryErr
			}
			if memoryRuntime.Capture != nil {
				serverOptions = append(serverOptions,
					httpserver.WithMemoryCaptureService(memoryRuntime.Capture),
				)
			}
		}
		if cfg.Reminder.Enabled {
			reminderRuntime, err = assembleReminderRuntime(cfg.Reminder, reminderRuntimeDependencies{Repository: store, MemoryRepository: store, WorkflowStore: store.WorkflowStore(), EditPayloadStore: store, Runner: agentRunner})
			if err != nil {
				return err
			}
			reminderRuntime.Start(ctx)
			if reminderRuntime.Command != nil {
				serverOptions = append(serverOptions, httpserver.WithReminderServices(reminderRuntime.Command, store))
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

		assembler := chat.NewBoundedAssembler(
			cfg.Context.MaxInputTokens, cfg.Context.MinRecentMessages, chat.ApproxTokenCounter{},
		)
		chatService, err := chat.NewService(ctx, store, agentRunner, assembler, chat.ServiceOptions{
			MessageHistoryLimit: cfg.Context.MessageHistoryLimit, DefaultModel: cfg.ActiveModel,
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
