package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudwego/eino/components/tool"

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

	chatModel, err := einoagent.NewModel(ctx, cfg.Model)
	if err != nil {
		return err
	}
	echoTool, err := einoagent.NewEchoTool()
	if err != nil {
		return err
	}
	agentRunner, err := einoagent.NewRunner(ctx, chatModel, []tool.BaseTool{echoTool})
	if err != nil {
		return err
	}

	server := httpserver.New(cfg.HTTPAddr, func() bool { return agentRunner != nil })
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("note agent listening on %s", cfg.HTTPAddr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
