package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidInput        = errors.New("invalid memory input")
	ErrNotFound            = errors.New("memory not found")
	ErrStateConflict       = errors.New("memory state conflict")
	ErrIdempotencyConflict = errors.New("memory idempotency conflict")
	ErrSensitiveContent    = errors.New("memory contains sensitive content")
)

type Owner struct {
	TenantID uint64 `json:"tenant_id"`
	UserID   uint64 `json:"user_id"`
}

func (o Owner) Valid() bool { return o.TenantID != 0 && o.UserID != 0 }

type Layer string

const (
	LayerWorking  Layer = "working"
	LayerSession  Layer = "session"
	LayerLongTerm Layer = "long_term"
)

type ScopeType string

const (
	ScopeUser     ScopeType = "user"
	ScopeSession  ScopeType = "session"
	ScopeWorkflow ScopeType = "workflow"
)

type Scope struct {
	Type ScopeType `json:"type"`
	ID   string    `json:"id,omitempty"`
}

type Kind string

const (
	KindPreference Kind = "preference"
	KindFact       Kind = "fact"
	KindGoal       Kind = "goal"
	KindConstraint Kind = "constraint"
	KindSummary    Kind = "summary"
	KindOutcome    Kind = "outcome"
)

type EntityRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (r EntityRef) Empty() bool { return r.Type == "" && r.ID == "" }

type SourceRef struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

type Authority string

const (
	AuthorityModelInferred Authority = "model_inferred"
	AuthorityTrustedSystem Authority = "trusted_system"
	AuthorityUserStated    Authority = "user_stated"
	AuthorityUserConfirmed Authority = "user_confirmed"
)

func (a Authority) Rank() int {
	switch a {
	case AuthorityUserConfirmed:
		return 4
	case AuthorityUserStated:
		return 3
	case AuthorityTrustedSystem:
		return 2
	case AuthorityModelInferred:
		return 1
	default:
		return 0
	}
}

type Status string

const (
	StatusCandidate  Status = "candidate"
	StatusActive     Status = "active"
	StatusRejected   Status = "rejected"
	StatusSuperseded Status = "superseded"
	StatusRevoked    Status = "revoked"
	StatusExpired    Status = "expired"
)

func (s Status) Obsolete() bool {
	return s == StatusRejected || s == StatusSuperseded || s == StatusRevoked || s == StatusExpired
}

func (s Status) CanTransition(to Status) bool {
	switch s {
	case StatusCandidate:
		return to == StatusActive || to == StatusRejected || to == StatusExpired
	case StatusActive:
		return to == StatusSuperseded || to == StatusRevoked || to == StatusExpired
	default:
		return false
	}
}

type StructuredValue struct {
	Schema  string         `json:"schema"`
	Version uint32         `json:"version"`
	Data    map[string]any `json:"data"`
}

type MemoryRef struct {
	ID             string `json:"id"`
	LineageVersion uint64 `json:"lineage_version"`
	ContentHash    string `json:"content_hash"`
}

type Record struct {
	ID              string          `json:"id"`
	Owner           Owner           `json:"owner"`
	Layer           Layer           `json:"layer"`
	Kind            Kind            `json:"kind"`
	Scope           Scope           `json:"scope"`
	Namespace       string          `json:"namespace"`
	SlotKey         string          `json:"slot_key,omitempty"`
	Entity          EntityRef       `json:"entity,omitempty"`
	LineageID       string          `json:"lineage_id"`
	LineageVersion  uint64          `json:"lineage_version"`
	RowVersion      uint64          `json:"row_version"`
	CanonicalText   string          `json:"canonical_text"`
	StructuredValue StructuredValue `json:"structured_value"`
	ContentHash     string          `json:"content_hash"`
	Authority       Authority       `json:"authority"`
	Confidence      float64         `json:"confidence"`
	Salience        float64         `json:"salience"`
	Source          SourceRef       `json:"source"`
	Status          Status          `json:"status"`
	SupersedesID    string          `json:"supersedes_id,omitempty"`
	SupersededBy    string          `json:"superseded_by,omitempty"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (r Record) Ref() MemoryRef {
	return MemoryRef{ID: r.ID, LineageVersion: r.LineageVersion, ContentHash: r.ContentHash}
}

func (r Record) IsActiveAt(now time.Time) bool {
	return r.Status == StatusActive && (r.ExpiresAt == nil || now.Before(*r.ExpiresAt))
}

func (r Record) Validate(now time.Time) error {
	if !r.Owner.Valid() || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.LineageID) == "" || r.LineageVersion == 0 || r.RowVersion == 0 {
		return fmt.Errorf("%w: owner, id and versions are required", ErrInvalidInput)
	}
	if r.Layer == LayerWorking {
		return fmt.Errorf("%w: working memory is not persistent", ErrInvalidInput)
	}
	switch r.Layer {
	case LayerSession:
		if (r.Scope.Type != ScopeSession && r.Scope.Type != ScopeWorkflow) || strings.TrimSpace(r.Scope.ID) == "" || r.ExpiresAt == nil || !r.ExpiresAt.After(now) {
			return fmt.Errorf("%w: session memory requires session/workflow scope and future expiry", ErrInvalidInput)
		}
	case LayerLongTerm:
		if r.Scope.Type != ScopeUser || strings.TrimSpace(r.Scope.ID) != "" {
			return fmt.Errorf("%w: long-term memory requires user scope", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: unknown layer", ErrInvalidInput)
	}
	if strings.TrimSpace(r.Namespace) == "" || strings.TrimSpace(r.CanonicalText) == "" || r.ContentHash == "" {
		return fmt.Errorf("%w: namespace, text and content hash are required", ErrInvalidInput)
	}
	if r.Confidence < 0 || r.Confidence > 1 || r.Salience < 0 || r.Salience > 1 || r.Authority.Rank() == 0 {
		return fmt.Errorf("%w: invalid scores or authority", ErrInvalidInput)
	}
	if r.Status != StatusCandidate && r.Status != StatusActive {
		return fmt.Errorf("%w: new memory must be candidate or active", ErrInvalidInput)
	}
	return ValidateContent(r.CanonicalText, r.StructuredValue, r.Source)
}

const (
	MaxCanonicalTextBytes = 16 * 1024
	MaxStructuredBytes    = 16 * 1024
	MaxStructuredDepth    = 8
)

func NormalizeContent(text string, value StructuredValue) (string, StructuredValue, string, error) {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || len(text) > MaxCanonicalTextBytes {
		return "", StructuredValue{}, "", fmt.Errorf("%w: canonical text is empty or too large", ErrInvalidInput)
	}
	if value.Schema == "" || value.Version == 0 || value.Data == nil {
		return "", StructuredValue{}, "", fmt.Errorf("%w: structured value schema, version and data are required", ErrInvalidInput)
	}
	if err := validateJSONValue(value.Data, 1); err != nil {
		return "", StructuredValue{}, "", err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxStructuredBytes {
		return "", StructuredValue{}, "", fmt.Errorf("%w: structured value is not bounded JSON", ErrInvalidInput)
	}
	var canonical StructuredValue
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return "", StructuredValue{}, "", fmt.Errorf("%w: structured value", ErrInvalidInput)
	}
	h := sha256.Sum256(append(append([]byte(text), 0), raw...))
	return text, canonical, hex.EncodeToString(h[:]), nil
}

func validateJSONValue(value any, depth int) error {
	if depth > MaxStructuredDepth {
		return fmt.Errorf("%w: structured value depth exceeded", ErrInvalidInput)
	}
	switch v := value.(type) {
	case nil, bool, string, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return nil
	case []any:
		if len(v) > 128 {
			return fmt.Errorf("%w: too many array values", ErrInvalidInput)
		}
		for _, item := range v {
			if err := validateJSONValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(v) > 128 {
			return fmt.Errorf("%w: too many object fields", ErrInvalidInput)
		}
		for key, item := range v {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("%w: empty object key", ErrInvalidInput)
			}
			if err := validateJSONValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported structured value type %T", ErrInvalidInput, value)
	}
}
