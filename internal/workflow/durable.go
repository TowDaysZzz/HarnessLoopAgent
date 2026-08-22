package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	MinLeaseDuration      = time.Second
	MaxLeaseDuration      = 24 * time.Hour
	DefaultCheckpointSize = 1 << 20
)

type WorkflowOwner struct {
	TenantID uint64 `json:"tenant_id"`
	OwnerID  uint64 `json:"owner_id"`
}

func (o WorkflowOwner) Validate() error {
	if o.TenantID == 0 || o.OwnerID == 0 {
		return contractError("workflow owner requires tenant and owner ids", nil)
	}
	return nil
}

type ActorRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (a ActorRef) Validate() error {
	if !validIdentifier(a.Type) || !validIdentifier(a.ID) {
		return contractError("actor reference requires type and id", nil)
	}
	return nil
}

type Claim struct {
	Token      string    `json:"token"`
	LeaseUntil time.Time `json:"lease_until"`
}

func (c Claim) Validate(now time.Time) error {
	if !validIdentifier(c.Token) || !c.LeaseUntil.After(now) || c.LeaseUntil.Sub(now) > MaxLeaseDuration {
		return contractError("claim requires a token and bounded future lease", nil)
	}
	return nil
}

type WaitStatus string

const (
	WaitPending    WaitStatus = "pending"
	WaitProcessing WaitStatus = "processing"
	WaitResolved   WaitStatus = "resolved"
	WaitExpired    WaitStatus = "expired"
	WaitCancelled  WaitStatus = "cancelled"
)

func (s WaitStatus) Valid() bool {
	return s == WaitPending || s == WaitProcessing || s == WaitResolved || s == WaitExpired || s == WaitCancelled
}

func (s WaitStatus) Terminal() bool {
	return s == WaitResolved || s == WaitExpired || s == WaitCancelled
}

type StoredWait struct {
	Point          WaitPoint   `json:"point"`
	Status         WaitStatus  `json:"status"`
	RecordVersion  uint64      `json:"record_version"`
	Claim          *Claim      `json:"claim,omitempty"`
	ResolvedAction HumanAction `json:"resolved_action,omitempty"`
	ResolvedBy     *ActorRef   `json:"resolved_by,omitempty"`
	ResolvedAt     time.Time   `json:"resolved_at,omitempty"`
}

func (w *StoredWait) Transition(next WaitStatus) error {
	if w == nil || !w.Status.Valid() || !next.Valid() || !allowedWaitTransition(w.Status, next) {
		return stateError("invalid durable wait transition", nil)
	}
	w.Status = next
	w.RecordVersion++
	if next != WaitProcessing {
		w.Claim = nil
	}
	return nil
}

func allowedWaitTransition(from, to WaitStatus) bool {
	switch from {
	case WaitPending:
		return to == WaitProcessing || to == WaitExpired || to == WaitCancelled
	case WaitProcessing:
		return to == WaitProcessing || to == WaitResolved || to == WaitExpired || to == WaitCancelled
	default:
		return false
	}
}

type CheckpointEnvelope struct {
	SchemaID          string            `json:"schema_id"`
	SchemaVersion     uint64            `json:"schema_version"`
	DefinitionVersion DefinitionVersion `json:"definition_version"`
	Meta              RunMetadata       `json:"meta"`
	Control           ControlState      `json:"control"`
	Budget            BudgetState       `json:"budget"`
	Data              json.RawMessage   `json:"data"`
}

func (e CheckpointEnvelope) Validate() error {
	if !validIdentifier(e.SchemaID) || e.SchemaVersion == 0 || !validIdentifier(string(e.DefinitionVersion)) || len(e.Data) == 0 {
		return &Error{Code: CodeCodecIncompatible, Message: "checkpoint schema and data are required"}
	}
	if e.Meta.DefinitionVersion != e.DefinitionVersion {
		return &Error{Code: CodeCodecIncompatible, Message: "checkpoint definition versions do not match"}
	}
	state := WorkflowState[json.RawMessage]{Meta: e.Meta, Control: e.Control, Budget: e.Budget, Data: e.Data}
	if err := state.Validate(); err != nil {
		return &Error{Code: CodeCodecIncompatible, Message: "checkpoint state is invalid", Err: err}
	}
	return nil
}

type StoredWorkflow struct {
	Owner          WorkflowOwner      `json:"owner"`
	IdempotencyKey string             `json:"idempotency_key"`
	Checkpoint     CheckpointEnvelope `json:"checkpoint"`
	Claim          *Claim             `json:"claim,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

func (r StoredWorkflow) Validate() error {
	if err := r.Owner.Validate(); err != nil {
		return err
	}
	if !validIdentifier(r.IdempotencyKey) || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return contractError("stored workflow requires idempotency key and timestamps", nil)
	}
	return r.Checkpoint.Validate()
}

type StateCodec[T any] interface {
	SchemaID() string
	SchemaVersion() uint64
	Encode(T) ([]byte, error)
	Decode([]byte) (T, error)
}

type CodecRegistry[T any] struct {
	mu     sync.RWMutex
	codecs map[string]StateCodec[T]
}

func NewCodecRegistry[T any]() *CodecRegistry[T] {
	return &CodecRegistry[T]{codecs: make(map[string]StateCodec[T])}
}

func (r *CodecRegistry[T]) Register(codec StateCodec[T]) error {
	if r == nil || codec == nil || !validIdentifier(codec.SchemaID()) || codec.SchemaVersion() == 0 {
		return &Error{Code: CodeCodecIncompatible, Message: "valid state codec is required"}
	}
	key := codecKey(codec.SchemaID(), codec.SchemaVersion())
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.codecs[key]; exists {
		return &Error{Code: CodeCodecIncompatible, Message: "state codec is already registered"}
	}
	r.codecs[key] = codec
	return nil
}

func (r *CodecRegistry[T]) Resolve(schemaID string, version uint64) (StateCodec[T], error) {
	if r == nil {
		return nil, &Error{Code: CodeCodecIncompatible, Message: "state codec registry is unavailable"}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	codec, exists := r.codecs[codecKey(schemaID, version)]
	if !exists {
		return nil, &Error{Code: CodeCodecIncompatible, Message: "state codec is not registered"}
	}
	return codec, nil
}

func codecKey(schemaID string, version uint64) string { return fmt.Sprintf("%s:%d", schemaID, version) }

type JSONStateCodec[T any] struct {
	ID            string
	Version       uint64
	ValidateData  func(T) error
	ForbidSecrets bool
}

func (c JSONStateCodec[T]) SchemaID() string      { return c.ID }
func (c JSONStateCodec[T]) SchemaVersion() uint64 { return c.Version }

func (c JSONStateCodec[T]) Encode(data T) ([]byte, error) {
	if c.ValidateData != nil {
		if err := c.ValidateData(data); err != nil {
			return nil, &Error{Code: CodeCodecIncompatible, Message: "business state validation failed", Err: err}
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, &Error{Code: CodeCodecIncompatible, Message: "encode business state", Err: err}
	}
	if err := rejectSensitiveJSON(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c JSONStateCodec[T]) Decode(raw []byte) (T, error) {
	var data T
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, &Error{Code: CodeCodecIncompatible, Message: "decode business state", Err: err}
	}
	if c.ValidateData != nil {
		if err := c.ValidateData(data); err != nil {
			return data, &Error{Code: CodeCodecIncompatible, Message: "business state validation failed", Err: err}
		}
	}
	return data, nil
}

func EncodeCheckpoint[T any](state WorkflowState[T], codec StateCodec[T], maxBytes int) (CheckpointEnvelope, error) {
	if codec == nil || !validIdentifier(codec.SchemaID()) || codec.SchemaVersion() == 0 {
		return CheckpointEnvelope{}, &Error{Code: CodeCodecIncompatible, Message: "valid state codec is required"}
	}
	if err := state.Validate(); err != nil {
		return CheckpointEnvelope{}, err
	}
	raw, err := codec.Encode(state.Data)
	if err != nil {
		return CheckpointEnvelope{}, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultCheckpointSize
	}
	if len(raw) > maxBytes {
		return CheckpointEnvelope{}, &Error{Code: CodeCheckpointTooLarge, Message: "workflow checkpoint exceeds configured size"}
	}
	envelope := CheckpointEnvelope{SchemaID: codec.SchemaID(), SchemaVersion: codec.SchemaVersion(), DefinitionVersion: state.Meta.DefinitionVersion, Meta: state.Meta, Control: state.Control, Budget: state.Budget, Data: append(json.RawMessage(nil), raw...)}
	return envelope, envelope.Validate()
}

func DecodeCheckpoint[T any](envelope CheckpointEnvelope, definition DefinitionVersion, codec StateCodec[T]) (WorkflowState[T], error) {
	var zero WorkflowState[T]
	if err := envelope.Validate(); err != nil {
		return zero, err
	}
	if codec == nil || envelope.SchemaID != codec.SchemaID() || envelope.SchemaVersion != codec.SchemaVersion() || envelope.DefinitionVersion != definition {
		return zero, &Error{Code: CodeCodecIncompatible, Message: "checkpoint codec or definition version is incompatible"}
	}
	data, err := codec.Decode(envelope.Data)
	if err != nil {
		return zero, err
	}
	return WorkflowState[T]{Meta: envelope.Meta, Control: envelope.Control, Budget: envelope.Budget, Data: data}, nil
}

func rejectSensitiveJSON(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return &Error{Code: CodeCodecIncompatible, Message: "inspect business state", Err: err}
	}
	forbidden := map[string]struct{}{"access_token": {}, "cookie": {}, "password": {}, "model_key": {}, "model_secret": {}, "prompt": {}, "raw_input": {}, "user_input": {}}
	var inspect func(any) error
	inspect = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.TrimSpace(key))
				if _, found := forbidden[normalized]; found || strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") {
					return &Error{Code: CodeCodecIncompatible, Message: fmt.Sprintf("business state contains forbidden field %q", key)}
				}
				if err := inspect(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := inspect(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(value)
}

type CreateStoredRun struct{ Run StoredWorkflow }

type ClaimRunRequest struct {
	Owner                WorkflowOwner
	RunID                WorkflowRunID
	ExpectedStateVersion uint64
	Claim                Claim
	Now                  time.Time
}

type ClaimWaitRequest struct {
	Owner                WorkflowOwner
	RunID                WorkflowRunID
	ExpectedStateVersion uint64
	Command              ResumeCommand
	Actor                ActorRef
	Claim                Claim
	Now                  time.Time
}

type RenewClaimRequest struct {
	Owner WorkflowOwner
	RunID WorkflowRunID
	Token string
	Until time.Time
	Now   time.Time
}

type CommitExecutionRequest struct {
	Owner                WorkflowOwner
	RunID                WorkflowRunID
	Token                string
	ExpectedStateVersion uint64
	Checkpoint           CheckpointEnvelope
	Events               []NodeEvent
	ResolvedWaitID       WaitID
	ResolvedAction       HumanAction
	Actor                *ActorRef
	Now                  time.Time
}

type DurableStore interface {
	CreateRun(context.Context, CreateStoredRun) (StoredWorkflow, bool, error)
	GetRun(context.Context, WorkflowOwner, WorkflowRunID) (StoredWorkflow, error)
	GetCurrentWait(context.Context, WorkflowOwner, WorkflowRunID) (StoredWait, error)
	ClaimRun(context.Context, ClaimRunRequest) (StoredWorkflow, error)
	ClaimWait(context.Context, ClaimWaitRequest) (StoredWorkflow, error)
	RenewClaim(context.Context, RenewClaimRequest) error
	CommitExecution(context.Context, CommitExecutionRequest) error
	ListNodeEvents(context.Context, WorkflowOwner, WorkflowRunID) ([]NodeEvent, error)
}
