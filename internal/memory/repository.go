package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const MaxExactQuerySelectors = 32

type SlotSelector struct {
	Namespace string
	SlotKey   string
}

type ExactQuery struct {
	Owner         Owner
	Scope         Scope
	Layers        []Layer
	Kinds         []Kind
	ActiveAt      *time.Time
	Slots         []SlotSelector
	Entities      []EntityRef
	ContentHashes []string
	ScopeOnly     bool

	// Deprecated single-value selectors are retained for source compatibility.
	Namespace   string
	SlotKey     string
	Entity      EntityRef
	ContentHash string
	Refs        []MemoryRef
	Limit       int
}

func (q ExactQuery) Validate() error {
	if !q.Owner.Valid() || q.Limit <= 0 || q.Limit > 200 {
		return ErrInvalidInput
	}
	if _, err := NormalizeScope(q.Scope); err != nil {
		return err
	}
	selectorCount := len(q.Slots) + len(q.Entities) + len(q.ContentHashes) + len(q.Refs)
	if q.ScopeOnly {
		selectorCount++
		if q.Scope.Type == ScopeUser {
			return fmt.Errorf("%w: user scope is not a bounded selector", ErrInvalidInput)
		}
	}
	if q.Namespace != "" || q.SlotKey != "" {
		selectorCount++
	}
	if !q.Entity.Empty() {
		selectorCount++
	}
	if q.ContentHash != "" {
		selectorCount++
	}
	if selectorCount == 0 {
		return nil
	}
	if selectorCount > MaxExactQuerySelectors || len(q.Layers) > MaxRecallFilterValues || len(q.Kinds) > MaxRecallFilterValues {
		return fmt.Errorf("%w: exact query boundary", ErrInvalidInput)
	}
	if (q.Namespace == "") != (q.SlotKey == "") {
		return fmt.Errorf("%w: namespace and slot_key must be paired", ErrInvalidInput)
	}
	for _, layer := range q.Layers {
		if _, err := NormalizeLayer(layer); err != nil {
			return err
		}
	}
	for _, kind := range q.Kinds {
		if _, err := NormalizeKind(kind); err != nil {
			return err
		}
	}
	for _, slot := range append(append([]SlotSelector(nil), q.Slots...), SlotSelector{Namespace: q.Namespace, SlotKey: q.SlotKey}) {
		if slot.Namespace == "" && slot.SlotKey == "" {
			continue
		}
		if _, err := NormalizeNamespace(slot.Namespace); err != nil {
			return err
		}
		if _, err := NormalizeSlotKey(slot.SlotKey); err != nil {
			return err
		}
	}
	for _, entity := range append(append([]EntityRef(nil), q.Entities...), q.Entity) {
		if entity.Empty() {
			continue
		}
		if _, err := NormalizeEntityRef(entity); err != nil {
			return err
		}
	}
	for _, hash := range append(append([]string(nil), q.ContentHashes...), q.ContentHash) {
		if hash == "" {
			continue
		}
		if !validSHA256(strings.ToLower(strings.TrimSpace(hash))) {
			return fmt.Errorf("%w: content hash", ErrInvalidInput)
		}
	}
	for _, ref := range q.Refs {
		if strings.TrimSpace(ref.ID) == "" || ref.LineageVersion == 0 || !validSHA256(ref.ContentHash) {
			return fmt.Errorf("%w: pinned memory ref", ErrInvalidInput)
		}
	}
	return nil
}

func (q ExactQuery) HasSelector() bool {
	return q.ScopeOnly || len(q.Slots)+len(q.Entities)+len(q.ContentHashes)+len(q.Refs) > 0 || (q.Namespace != "" && q.SlotKey != "") || !q.Entity.Empty() || q.ContentHash != ""
}

type RelationType string

const (
	RelationSupersedes    RelationType = "supersedes"
	RelationDuplicateOf   RelationType = "duplicate_of"
	RelationConflictsWith RelationType = "conflicts_with"
	RelationRefines       RelationType = "refines"
	RelationDerivedFrom   RelationType = "derived_from"
	RelationRelatedTo     RelationType = "related_to"
)

type Relation struct {
	FromID     string
	ToID       string
	Type       RelationType
	ReasonCode string
}

type MutationTarget struct {
	ID                 string
	ExpectedRowVersion uint64
	NewStatus          Status
}

type Mutation struct {
	Owner          Owner
	NewMemory      *Record
	Targets        []MutationTarget
	Relations      []Relation
	Actor          string
	ReasonCode     string
	IdempotencyKey string
	InputHash      string
	OccurredAt     time.Time
}

type MutationResult struct {
	Memory    Record
	Relations []Relation
	Replayed  bool
}

type CandidateActivation struct {
	Owner                           Owner
	CandidateID, SupersededID       string
	CandidateVersion, TargetVersion uint64
	Actor, ReasonCode               string
	IdempotencyKey, InputHash       string
	OccurredAt                      time.Time
}

type ProjectionStatus string

const (
	ProjectionPending         ProjectionStatus = "pending"
	ProjectionProcessing      ProjectionStatus = "processing"
	ProjectionCompleted       ProjectionStatus = "completed"
	ProjectionFailed          ProjectionStatus = "failed"
	ProjectionPermanentFailed ProjectionStatus = "permanent_failed"
)

type Projection struct {
	ID            string
	Owner         Owner
	MemoryID      string
	ContentHash   string
	ModelVersion  string
	Status        ProjectionStatus
	Attempt       int
	AvailableAt   time.Time
	ClaimedAt     *time.Time
	ProcessedAt   *time.Time
	LastErrorCode string
}

type Repository interface {
	FindExact(context.Context, ExactQuery) ([]Record, error)
	BatchGet(context.Context, Owner, []string) ([]Record, error)
	CommitMutation(context.Context, Mutation) (MutationResult, error)
	TransitionMemory(context.Context, Owner, string, uint64, Status, string, string, string, string, time.Time) (MutationResult, error)
	ActivateCandidateSuperseding(context.Context, CandidateActivation) (MutationResult, error)
	Expire(context.Context, Owner, time.Time, int) (int, error)
	ClaimProjections(context.Context, int, time.Time) ([]Projection, error)
	CompleteProjection(context.Context, Owner, string, time.Time) error
	FailProjection(context.Context, Owner, string, string, time.Time, bool) error
}

// MutationVersionReader exposes the owner-scoped monotonic version used by
// consumers that must invalidate snapshots when any memory visibility changes.
type MutationVersionReader interface {
	MutationVersion(context.Context, Owner) (uint64, error)
}

type ContextRefSource interface {
	ListActiveContextRefs(context.Context, Owner, []Kind, time.Time, int) ([]MemoryRef, error)
}
