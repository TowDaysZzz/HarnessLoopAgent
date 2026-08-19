package evals

import (
	"fmt"
	"strings"
	"time"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type ToolCalled struct{ Name string }

func (a ToolCalled) Check(result *Result) error {
	for _, event := range result.Events {
		if event.Type == appagent.EventToolCompleted && event.ToolName == a.Name {
			return nil
		}
	}
	return fmt.Errorf("tool %q was not called", a.Name)
}

type NoTextBeforeTool struct{ Name string }

func (a NoTextBeforeTool) Check(result *Result) error {
	toolSeen := false
	for _, event := range result.Events {
		if event.Type == appagent.EventToolCompleted && event.ToolName == a.Name {
			toolSeen = true
		}
		if event.Type == appagent.EventTextDelta && !toolSeen && event.Delta != "" {
			return fmt.Errorf("text was emitted before tool %q completed", a.Name)
		}
	}
	return nil
}

type OutputContains struct{ Values []string }

func (a OutputContains) Check(result *Result) error {
	for _, value := range a.Values {
		if !strings.Contains(result.Answer, value) {
			return fmt.Errorf("answer does not contain %q", value)
		}
	}
	return nil
}

type MaxDuration struct{ Duration time.Duration }

func (a MaxDuration) Check(result *Result) error {
	if result.Duration > a.Duration {
		return fmt.Errorf("duration %s exceeds %s", result.Duration, a.Duration)
	}
	return nil
}

type NoRunError struct{}

func (NoRunError) Check(result *Result) error {
	return result.RunError
}
