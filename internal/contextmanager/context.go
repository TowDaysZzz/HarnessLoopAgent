package contextmanager

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

var ErrContextBudgetTooSmall = errors.New("context budget cannot fit the current user message")

type TokenCounter interface {
	Count(text string) int
}

// ApproxTokenCounter is conservative for Chinese and mixed-language notes.
// A model-specific tokenizer can replace it without changing the assembler.
type ApproxTokenCounter struct{}

func (ApproxTokenCounter) Count(text string) int {
	var ascii, other int
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

type BuildResult struct {
	Messages        []agent.Message
	EstimatedTokens int
	Truncated       bool
	DroppedMessages int
}

type Assembler interface {
	Build(messages []agent.Message) (BuildResult, error)
}

type BoundedAssembler struct {
	MaxInputTokens    int
	MinRecentMessages int
	Counter           TokenCounter
}

func NewBoundedAssembler(maxInputTokens, minRecentMessages int, counter TokenCounter) *BoundedAssembler {
	if counter == nil {
		counter = ApproxTokenCounter{}
	}
	return &BoundedAssembler{MaxInputTokens: maxInputTokens, MinRecentMessages: minRecentMessages, Counter: counter}
}

func (a *BoundedAssembler) Build(messages []agent.Message) (BuildResult, error) {
	if len(messages) == 0 {
		return BuildResult{}, errors.New("context requires at least one message")
	}
	last := messages[len(messages)-1]
	if last.Role != "user" || strings.TrimSpace(last.Content) == "" {
		return BuildResult{}, errors.New("context must end with a non-empty user message")
	}
	if a.MaxInputTokens < 1 {
		return BuildResult{}, errors.New("context token budget must be positive")
	}

	costs := make([]int, len(messages))
	for i, message := range messages {
		costs[i] = a.Counter.Count(message.Content) + 4
	}
	if costs[len(costs)-1] > a.MaxInputTokens {
		return BuildResult{}, ErrContextBudgetTooSmall
	}

	start, used := len(messages)-1, costs[len(costs)-1]
	for start > 0 {
		next := costs[start-1]
		mustKeep := len(messages)-start < a.MinRecentMessages
		if used+next > a.MaxInputTokens && !mustKeep {
			break
		}
		if used+next > a.MaxInputTokens {
			break
		}
		start--
		used += next
	}
	selected := append([]agent.Message(nil), messages[start:]...)
	return BuildResult{
		Messages: selected, EstimatedTokens: used,
		Truncated: start > 0, DroppedMessages: start,
	}, nil
}
