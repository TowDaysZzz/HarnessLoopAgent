package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	einoagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent/eino"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "note agent cli: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("note-agent-cli", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "YAML 配置文件路径，默认读取 config.yaml")
	modelProfile := flags.String("model", "", "MODELS 中的模型配置档案名称")
	showEvents := flags.Bool("events", false, "输出 text.delta 等语义事件名称")
	if err := flags.Parse(args); err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" {
		return errors.New("缺少对话内容；用法：note-agent-cli [--model profile] [--events] \"你的问题\"")
	}

	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: *configPath, Model: *modelProfile})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runner, err := einoagent.NewConfiguredRunner(ctx, cfg)
	if err != nil {
		return err
	}

	profile := cfg.ActiveModel
	if profile == "" {
		profile = "default"
	}
	fmt.Fprintf(stderr, "model=%s name=%s base_url=%s\n", profile, cfg.Model.Name, cfg.Model.BaseURL)
	for event := range runner.Stream(ctx, prompt) {
		if err := writeEvent(stdout, stderr, event, *showEvents); err != nil {
			return err
		}
	}
	return nil
}

func writeEvent(stdout, stderr io.Writer, event appagent.Event, showEvents bool) error {
	if showEvents {
		fmt.Fprintf(stderr, "[%s]", event.Type)
		if event.ToolName != "" {
			fmt.Fprintf(stderr, " tool=%s", event.ToolName)
		}
		fmt.Fprintln(stderr)
	}
	switch event.Type {
	case appagent.EventTextDelta:
		_, err := fmt.Fprint(stdout, event.Delta)
		return err
	case appagent.EventToolCompleted:
		_, err := fmt.Fprintf(stderr, "\n[tool.completed] %s: %s\n", event.ToolName, event.Delta)
		return err
	case appagent.EventRunCompleted:
		_, err := fmt.Fprintln(stdout)
		return err
	case appagent.EventRunFailed:
		if event.Err == nil {
			return errors.New("agent run failed")
		}
		return event.Err
	case appagent.EventStatus:
		if showEvents {
			_, err := fmt.Fprintf(stderr, "status=%s\n", event.Status)
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown agent event %q", event.Type)
	}
}
