package evals

import (
	"context"
	"strings"
	"time"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type Case struct {
	Name       string
	Prompt     string
	Runner     appagent.StreamRunner
	Assertions []Assertion
}

type Result struct {
	Case     *Case
	Events   []appagent.Event
	Answer   string
	Duration time.Duration
	RunError error
	Failures []error
	Passed   bool
}

type Assertion interface {
	Check(*Result) error
}

func RunCase(ctx context.Context, testCase *Case) Result {
	start := time.Now()
	result := Result{Case: testCase}
	var answer strings.Builder
	for event := range testCase.Runner.Stream(ctx, testCase.Prompt) {
		result.Events = append(result.Events, event)
		if event.Type == appagent.EventTextDelta {
			answer.WriteString(event.Delta)
		}
		if event.Type == appagent.EventRunFailed {
			result.RunError = event.Err
		}
	}
	result.Answer = answer.String()
	result.Duration = time.Since(start)
	for _, assertion := range testCase.Assertions {
		if err := assertion.Check(&result); err != nil {
			result.Failures = append(result.Failures, err)
		}
	}
	result.Passed = len(result.Failures) == 0
	return result
}
