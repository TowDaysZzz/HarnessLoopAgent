package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

var ErrAmbiguous = fmt.Errorf("%w: ambiguous skill match", ErrInvalidInvocation)

type MatchInput struct {
	Content   string
	SessionID string
	NowUnix   int64
}

type Match struct {
	Arguments  json.RawMessage
	Confidence float64
	Reason     string
}

type Matcher interface {
	Match(context.Context, MatchInput) (Match, bool, error)
}

type Codec interface {
	Validate(json.RawMessage) error
}

type Request struct {
	Invocation Invocation
	Content    string
	Messages   []agent.Message
}

type Result struct {
	Text       string
	Candidate  json.RawMessage
	Suspended  bool
	CacheState string
	Steps      []string
}

type DirectHandler interface {
	Execute(context.Context, Request) (Result, error)
}

type StreamingHandler interface {
	Stream(context.Context, Request) <-chan agent.Event
}

type WorkflowHandler interface {
	Run(context.Context, Request) (Result, error)
}

type DurableWorkflowHandler interface {
	Start(context.Context, Request) (Result, error)
}

type Definition struct {
	ID           ID
	Version      Version
	Mode         ExecutionMode
	Risk         RiskLevel
	Dependencies []Dependency
	Budget       Budget
	Matcher      Matcher
	InputCodec   Codec
	OutputCodec  Codec
	Direct       DirectHandler
	Streaming    StreamingHandler
	Workflow     WorkflowHandler
	Durable      DurableWorkflowHandler
}

func (d Definition) Validate(available map[Dependency]bool) error {
	if !validIdentifier(string(d.ID)) || !validIdentifier(string(d.Version)) || !d.Mode.Valid() || !d.Risk.Valid() || d.Matcher == nil || d.InputCodec == nil || d.OutputCodec == nil {
		return fmt.Errorf("%w: invalid metadata", ErrInvalidDefinition)
	}
	if err := d.Budget.Validate(); err != nil {
		return err
	}
	seen := map[Dependency]struct{}{}
	for _, dependency := range d.Dependencies {
		if !validIdentifier(string(dependency)) {
			return fmt.Errorf("%w: invalid dependency", ErrInvalidDefinition)
		}
		if _, duplicate := seen[dependency]; duplicate {
			return fmt.Errorf("%w: duplicate dependency", ErrInvalidDefinition)
		}
		seen[dependency] = struct{}{}
		if !available[dependency] {
			return fmt.Errorf("%w: missing dependency %q", ErrUnavailable, dependency)
		}
	}
	handlers := 0
	if d.Direct != nil {
		handlers++
	}
	if d.Streaming != nil {
		handlers++
	}
	if d.Workflow != nil {
		handlers++
	}
	if d.Durable != nil {
		handlers++
	}
	if handlers != 1 || d.Mode == ModeDirect && d.Direct == nil || d.Mode == ModeStreaming && d.Streaming == nil || d.Mode == ModeWorkflow && d.Workflow == nil || d.Mode == ModeDurableWorkflow && d.Durable == nil {
		return fmt.Errorf("%w: execution handler does not match mode", ErrInvalidDefinition)
	}
	return nil
}

type Registry struct {
	definitions map[string]Definition
	ordered     []Definition
}

type ResolvedMatch struct {
	Ref           Ref
	Arguments     json.RawMessage
	ArgumentsHash string
	Confidence    float64
	Reason        string
}

func NewRegistry(definitions []Definition, available map[Dependency]bool) (*Registry, error) {
	registry := &Registry{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		if err := definition.Validate(available); err != nil {
			return nil, err
		}
		key := registryKey(definition.ID, definition.Version)
		if _, exists := registry.definitions[key]; exists {
			return nil, fmt.Errorf("%w: duplicate %s", ErrInvalidDefinition, key)
		}
		definition.Dependencies = append([]Dependency(nil), definition.Dependencies...)
		registry.definitions[key] = definition
		registry.ordered = append(registry.ordered, definition)
	}
	sort.Slice(registry.ordered, func(i, j int) bool {
		return registryKey(registry.ordered[i].ID, registry.ordered[i].Version) < registryKey(registry.ordered[j].ID, registry.ordered[j].Version)
	})
	return registry, nil
}

func (r *Registry) Resolve(ref Ref) (Definition, error) {
	if r == nil || ref.Validate() != nil {
		return Definition{}, ErrNotFound
	}
	definition, ok := r.definitions[registryKey(ref.ID, ref.Version)]
	if !ok {
		return Definition{}, ErrNotFound
	}
	return definition, nil
}

func (r *Registry) Definitions() []Definition {
	if r == nil {
		return nil
	}
	return append([]Definition(nil), r.ordered...)
}

func (r *Registry) Match(ctx context.Context, input MatchInput) (ResolvedMatch, bool, error) {
	if r == nil {
		return ResolvedMatch{}, false, nil
	}
	var matches []ResolvedMatch
	for _, definition := range r.ordered {
		candidate, ok, err := definition.Matcher.Match(ctx, input)
		if err != nil {
			return ResolvedMatch{}, false, err
		}
		if !ok {
			continue
		}
		normalized, hash, err := NormalizeArguments(candidate.Arguments, 8*1024)
		if err != nil {
			return ResolvedMatch{}, false, err
		}
		if err := definition.InputCodec.Validate(normalized); err != nil {
			return ResolvedMatch{}, false, fmt.Errorf("%w: matcher input: %v", ErrInvalidInvocation, err)
		}
		matches = append(matches, ResolvedMatch{Ref: Ref{ID: definition.ID, Version: definition.Version}, Arguments: normalized, ArgumentsHash: hash, Confidence: candidate.Confidence, Reason: candidate.Reason})
	}
	if len(matches) == 0 {
		return ResolvedMatch{}, false, nil
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Confidence > matches[j].Confidence })
	if len(matches) > 1 && matches[0].Confidence == matches[1].Confidence && (!bytes.Equal(matches[0].Arguments, matches[1].Arguments) || matches[0].Ref != matches[1].Ref) {
		return ResolvedMatch{}, false, ErrAmbiguous
	}
	return matches[0], true, nil
}

func registryKey(id ID, version Version) string { return string(id) + "@" + string(version) }
