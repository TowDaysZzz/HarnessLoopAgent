package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	einoagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent/eino"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/httpserver"
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

	httpServer := httpserver.New(cfg.HTTPAddr, func() bool { return agentRunner != nil })
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
