package contextmanager

import (
	"errors"
	"testing"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

type runeCounter struct{}

func (runeCounter) Count(text string) int { return len([]rune(text)) }

func TestBoundedAssemblerKeepsNewestMessages(t *testing.T) {
	assembler := NewBoundedAssembler(18, 2, runeCounter{})
	result, err := assembler.Build([]agent.Message{
		{Role: "user", Content: "oldest"},
		{Role: "assistant", Content: "old"},
		{Role: "user", Content: "new"},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !result.Truncated || result.DroppedMessages != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Messages) != 2 || result.Messages[0].Content != "old" || result.Messages[1].Content != "new" {
		t.Fatalf("messages = %#v", result.Messages)
	}
}

func TestBoundedAssemblerRejectsOversizedCurrentMessage(t *testing.T) {
	assembler := NewBoundedAssembler(5, 1, runeCounter{})
	_, err := assembler.Build([]agent.Message{{Role: "user", Content: "too long"}})
	if !errors.Is(err, ErrContextBudgetTooSmall) {
		t.Fatalf("Build() error = %v", err)
	}
}
