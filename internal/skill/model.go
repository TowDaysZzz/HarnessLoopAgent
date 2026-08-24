package skill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidDefinition = errors.New("invalid skill definition")
	ErrInvalidInvocation = errors.New("invalid skill invocation")
	ErrNotFound          = errors.New("skill not found")
	ErrUnavailable       = errors.New("skill unavailable")
	ErrOutputLimit       = errors.New("skill output limit exceeded")
	ErrStreamProtocol    = errors.New("invalid skill stream protocol")
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

type ID string
type Version string

func validIdentifier(value string) bool {
	return len(value) <= 128 && identifierPattern.MatchString(value)
}

func validOpaqueID(value string) bool {
	return len(value) <= 128 && opaqueIDPattern.MatchString(value)
}

type ExecutionMode string

const (
	ModeDirect          ExecutionMode = "direct"
	ModeStreaming       ExecutionMode = "stream"
	ModeWorkflow        ExecutionMode = "workflow"
	ModeDurableWorkflow ExecutionMode = "durable_workflow"
)

func (m ExecutionMode) Valid() bool {
	switch m {
	case ModeDirect, ModeStreaming, ModeWorkflow, ModeDurableWorkflow:
		return true
	default:
		return false
	}
}

type RiskLevel string

const (
	RiskReadOnly RiskLevel = "read_only"
	RiskWrite    RiskLevel = "write"
	RiskHITL     RiskLevel = "hitl"
)

func (r RiskLevel) Valid() bool {
	return r == RiskReadOnly || r == RiskWrite || r == RiskHITL
}

type Dependency string

type Budget struct {
	Timeout         time.Duration
	MaxSteps        int
	MaxResumes      int
	MaxModelCalls   int
	MaxToolCalls    int
	MaxContextBytes int
	MaxOutputBytes  int
}

func (b Budget) Validate() error {
	if b.Timeout <= 0 || b.MaxSteps < 1 || b.MaxResumes < 0 || b.MaxModelCalls < 0 || b.MaxToolCalls < 0 || b.MaxContextBytes < 1 || b.MaxOutputBytes < 1 {
		return fmt.Errorf("%w: invalid budget", ErrInvalidDefinition)
	}
	return nil
}

type Owner struct {
	TenantID uint64
	UserID   uint64
}

func (o Owner) Valid() bool { return o.TenantID != 0 && o.UserID != 0 }

type Ref struct {
	ID      ID      `json:"id"`
	Version Version `json:"version"`
}

func (r Ref) Validate() error {
	if !validIdentifier(string(r.ID)) || !validIdentifier(string(r.Version)) {
		return fmt.Errorf("%w: invalid skill ref", ErrInvalidInvocation)
	}
	return nil
}

type InvocationStatus string

const (
	InvocationPending   InvocationStatus = "pending"
	InvocationRunning   InvocationStatus = "running"
	InvocationSuspended InvocationStatus = "suspended"
	InvocationCompleted InvocationStatus = "completed"
	InvocationFailed    InvocationStatus = "failed"
	InvocationCancelled InvocationStatus = "cancelled"
)

func (s InvocationStatus) Valid() bool {
	switch s {
	case InvocationPending, InvocationRunning, InvocationSuspended, InvocationCompleted, InvocationFailed, InvocationCancelled:
		return true
	default:
		return false
	}
}

type Invocation struct {
	ID            string
	Owner         Owner
	SessionID     string
	ChatRunID     string
	Skill         Ref
	Arguments     json.RawMessage
	ArgumentsHash string
	Status        InvocationStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type InvocationRepository interface {
	CreateInvocation(context.Context, Invocation) (Invocation, bool, error)
	GetInvocation(context.Context, Owner, string) (Invocation, error)
	TransitionInvocation(context.Context, Owner, string, InvocationStatus, InvocationStatus, string, time.Time) (Invocation, error)
}

func CanTransitionInvocation(from, to InvocationStatus) bool {
	switch from {
	case InvocationPending:
		return to == InvocationRunning || to == InvocationCancelled || to == InvocationFailed
	case InvocationRunning:
		return to == InvocationSuspended || to == InvocationCompleted || to == InvocationFailed || to == InvocationCancelled
	case InvocationSuspended:
		return to == InvocationRunning || to == InvocationCompleted || to == InvocationFailed || to == InvocationCancelled
	default:
		return false
	}
}

func NewInvocation(id string, owner Owner, sessionID, chatRunID string, ref Ref, arguments json.RawMessage, now time.Time) (Invocation, error) {
	normalized, hash, err := NormalizeArguments(arguments, 8*1024)
	if err != nil {
		return Invocation{}, err
	}
	value := Invocation{ID: strings.TrimSpace(id), Owner: owner, SessionID: strings.TrimSpace(sessionID), ChatRunID: strings.TrimSpace(chatRunID), Skill: ref, Arguments: normalized, ArgumentsHash: hash, Status: InvocationPending, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if err := value.Validate(); err != nil {
		return Invocation{}, err
	}
	return value, nil
}

func (i Invocation) Validate() error {
	if !validOpaqueID(i.ID) || !i.Owner.Valid() || !validOpaqueID(i.SessionID) || !validOpaqueID(i.ChatRunID) || i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() || !i.Status.Valid() {
		return fmt.Errorf("%w: invalid invocation identity", ErrInvalidInvocation)
	}
	if err := i.Skill.Validate(); err != nil {
		return err
	}
	normalized, hash, err := NormalizeArguments(i.Arguments, 8*1024)
	if err != nil || !bytes.Equal(normalized, i.Arguments) || hash != i.ArgumentsHash {
		return fmt.Errorf("%w: arguments are not canonical", ErrInvalidInvocation)
	}
	return nil
}

var forbiddenArgumentKeys = map[string]struct{}{
	"tenant": {}, "tenant_id": {}, "user": {}, "user_id": {}, "owner": {}, "owner_id": {},
	"access_token": {}, "authorization": {}, "cookie": {}, "password": {}, "kb_id": {}, "knowledge_base_ids": {},
}

func NormalizeArguments(raw json.RawMessage, maxBytes int) (json.RawMessage, string, error) {
	if maxBytes < 2 || len(raw) == 0 || len(raw) > maxBytes {
		return nil, "", fmt.Errorf("%w: arguments size", ErrInvalidInvocation)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", fmt.Errorf("%w: arguments json", ErrInvalidInvocation)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return nil, "", fmt.Errorf("%w: multiple json values", ErrInvalidInvocation)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("%w: arguments must be an object", ErrInvalidInvocation)
	}
	if err := rejectForbidden(object, 0); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > maxBytes {
		return nil, "", fmt.Errorf("%w: arguments encoding", ErrInvalidInvocation)
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func rejectForbidden(value any, depth int) error {
	if depth > 8 {
		return fmt.Errorf("%w: arguments nesting", ErrInvalidInvocation)
	}
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if _, denied := forbiddenArgumentKeys[strings.ToLower(strings.TrimSpace(key))]; denied {
				return fmt.Errorf("%w: forbidden argument %q", ErrInvalidInvocation, key)
			}
			if err := rejectForbidden(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(current) > 64 {
			return fmt.Errorf("%w: arguments collection", ErrInvalidInvocation)
		}
		for _, child := range current {
			if err := rejectForbidden(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
