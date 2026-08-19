package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	einoagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent/eino"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/httpserver"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
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
