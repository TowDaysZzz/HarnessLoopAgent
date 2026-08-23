package memory

import (
	"context"
	"time"
)

type ExactQuery struct {
	Owner       Owner
	Scope       Scope
	Namespace   string
	SlotKey     string
	Entity      EntityRef
	ContentHash string
	Refs        []MemoryRef
	Limit       int
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
	Expire(context.Context, Owner, time.Time, int) (int, error)
	ClaimProjections(context.Context, int, time.Time) ([]Projection, error)
	CompleteProjection(context.Context, Owner, string, time.Time) error
	FailProjection(context.Context, Owner, string, string, time.Time, bool) error
}
