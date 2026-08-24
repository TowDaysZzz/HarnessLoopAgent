package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

const (
	EventStarted   agent.EventType = "skill.started"
	EventCache     agent.EventType = "skill.cache"
	EventStep      agent.EventType = "skill.step"
	EventCandidate agent.EventType = "skill.candidate"
)

type Execution struct {
	Ref    Ref
	Events <-chan agent.Event
}

type Executor struct{ registry *Registry }

func NewExecutor(registry *Registry) (*Executor, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: registry", ErrUnavailable)
	}
	return &Executor{registry: registry}, nil
}

func (e *Executor) Execute(ctx context.Context, request Request) (Execution, error) {
	if err := request.Invocation.Validate(); err != nil {
		return Execution{}, err
	}
	definition, err := e.registry.Resolve(request.Invocation.Skill)
	if err != nil {
		return Execution{}, err
	}
	if err := definition.InputCodec.Validate(request.Invocation.Arguments); err != nil {
		return Execution{}, fmt.Errorf("%w: input codec: %v", ErrInvalidInvocation, err)
	}
	if definition.Mode == ModeStreaming {
		return Execution{Ref: request.Invocation.Skill, Events: e.wrapStream(ctx, definition, definition.Streaming.Stream(ctx, request))}, nil
	}
	out := make(chan agent.Event, 4)
	go func() {
		defer close(out)
		if !send(ctx, out, agent.Event{Type: EventStarted, Delta: string(definition.ID)}) {
			return
		}
		var result Result
		var runErr error
		switch definition.Mode {
		case ModeDirect:
			result, runErr = definition.Direct.Execute(ctx, request)
		case ModeWorkflow:
			result, runErr = definition.Workflow.Run(ctx, request)
		case ModeDurableWorkflow:
			result, runErr = definition.Durable.Start(ctx, request)
		default:
			runErr = ErrUnavailable
		}
		if runErr != nil {
			send(ctx, out, agent.Event{Type: agent.EventRunFailed, Err: runErr})
			return
		}
		if err := validateResult(definition, result); err != nil {
			send(ctx, out, agent.Event{Type: agent.EventRunFailed, Err: err})
			return
		}
		if result.CacheState != "" && !send(ctx, out, agent.Event{Type: EventCache, Delta: result.CacheState}) {
			return
		}
		for _, step := range result.Steps {
			if !send(ctx, out, agent.Event{Type: EventStep, Delta: step}) {
				return
			}
		}
		candidateStatus := ""
		if result.Suspended {
			candidateStatus = "suspended"
		}
		if len(result.Candidate) > 0 && !send(ctx, out, agent.Event{Type: EventCandidate, Delta: string(result.Candidate), Status: candidateStatus}) {
			return
		}
		if result.Text != "" && !send(ctx, out, agent.Event{Type: agent.EventTextDelta, Delta: result.Text}) {
			return
		}
		// A durable workflow may be suspended for a long time, but its triggering
		// Chat Run completes after the bounded candidate has been delivered.
		send(ctx, out, agent.Event{Type: agent.EventRunCompleted})
	}()
	return Execution{Ref: request.Invocation.Skill, Events: out}, nil
}

func validateResult(definition Definition, result Result) error {
	if len(result.Text)+len(result.Candidate) > definition.Budget.MaxOutputBytes {
		return ErrOutputLimit
	}
	if len(result.Steps) > definition.Budget.MaxSteps {
		return ErrOutputLimit
	}
	for _, step := range result.Steps {
		if !validIdentifier(step) {
			return fmt.Errorf("%w: invalid skill step", ErrStreamProtocol)
		}
	}
	if len(result.Candidate) > 0 {
		if !json.Valid(result.Candidate) {
			return fmt.Errorf("%w: invalid candidate json", ErrStreamProtocol)
		}
		if err := definition.OutputCodec.Validate(result.Candidate); err != nil {
			return fmt.Errorf("%w: output codec: %v", ErrStreamProtocol, err)
		}
	}
	if result.Suspended && definition.Mode != ModeDurableWorkflow {
		return fmt.Errorf("%w: only durable workflow may suspend", ErrStreamProtocol)
	}
	return nil
}

func (e *Executor) wrapStream(ctx context.Context, definition Definition, source <-chan agent.Event) <-chan agent.Event {
	out := make(chan agent.Event, 1)
	go func() {
		defer close(out)
		if !send(ctx, out, agent.Event{Type: EventStarted, Delta: string(definition.ID)}) {
			return
		}
		if source == nil {
			send(ctx, out, agent.Event{Type: agent.EventRunFailed, Err: ErrStreamProtocol})
			return
		}
		terminal := false
		outputBytes := 0
		for event := range source {
			if terminal {
				return
			}
			if event.Type == agent.EventTextDelta || event.Type == EventCandidate {
				outputBytes += len(event.Delta)
				if outputBytes > definition.Budget.MaxOutputBytes {
					send(ctx, out, agent.Event{Type: agent.EventRunFailed, Err: ErrOutputLimit})
					return
				}
			}
			terminal = event.Type == agent.EventRunCompleted || event.Type == agent.EventRunFailed
			if !send(ctx, out, event) {
				return
			}
		}
		if !terminal && ctx.Err() == nil {
			send(ctx, out, agent.Event{Type: agent.EventRunFailed, Err: ErrStreamProtocol})
		}
	}()
	return out
}

func send(ctx context.Context, out chan<- agent.Event, event agent.Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func IsCode(err, target error) bool { return errors.Is(err, target) }
