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
		assembler := contextmanager.NewBoundedAssembler(
			cfg.Context.MaxInputTokens, cfg.Context.MinRecentMessages, contextmanager.ApproxTokenCounter{},
		)
		chatService, err := chat.NewService(ctx, store, agentRunner, assembler, chat.ServiceOptions{
			MessageHistoryLimit: cfg.Context.MessageHistoryLimit,
			DefaultModel:        cfg.ActiveModel,
		})
		if err != nil {
			return err
		}
		serverOptions = append(serverOptions, httpserver.WithChatService(chatService))

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
			if cfg.Note.Enabled {
				noteService, err := note.NewService(store, rag, cfg.Note.KBID)
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
