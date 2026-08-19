package evals

import (
	"context"
	"testing"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type scriptedRunner struct{ events []appagent.Event }

func (r scriptedRunner) Stream(context.Context, string) <-chan appagent.Event {
	out := make(chan appagent.Event, len(r.events))
	for _, event := range r.events {
		out <- event
	}
	close(out)
	return out
}

func TestHarnessAssertions(t *testing.T) {
	result := RunCase(context.Background(), &Case{
		Name: "grounded answer", Prompt: "previous notes",
		Runner: scriptedRunner{events: []appagent.Event{
			{Type: appagent.EventToolCompleted, ToolName: "semantic_search_notes"},
			{Type: appagent.EventTextDelta, Delta: "go_interview.md doc-3-child-124"},
			{Type: appagent.EventRunCompleted},
		}},
		Assertions: []Assertion{
			ToolCalled{Name: "semantic_search_notes"},
			NoTextBeforeTool{Name: "semantic_search_notes"},
			OutputContains{Values: []string{"go_interview.md", "doc-3-child-124"}},
			NoRunError{},
		},
	})
	if !result.Passed {
		t.Fatalf("failures = %v", result.Failures)
	}
}
