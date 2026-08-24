package reminder

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ReminderRef struct {
	ID         string `json:"id"`
	RowVersion uint64 `json:"row_version"`
}

func (r ReminderRef) Validate() error {
	if strings.TrimSpace(r.ID) == "" || r.RowVersion == 0 {
		return ErrInvalidInput
	}
	return nil
}

type CreateInput struct {
	Reminder       Reminder
	IdempotencyKey string
	InputHash      string
	Actor          string
	ReasonCode     string
}

type MutationInput struct {
	Owner               Owner
	Target              ReminderRef
	Content             string
	Timezone            string
	NextFireAt          time.Time
	MemoryRefs          []MemoryRef
	IdempotencyKey      string
	InputHash           string
	Actor               string
	ReasonCode          string
	OccurredAt          time.Time
	ReplacementHash     string
	ExpectedContentHash string
}

type MutationResult struct {
	Reminder Reminder
	Replayed bool
}

type Query struct {
	Owner    Owner
	Statuses []Status
	From     *time.Time
	Until    *time.Time
	Label    string
	CursorAt *time.Time
	CursorID string
	Limit    int
}

func (q Query) Validate() error {
	if !q.Owner.Valid() || q.Limit < 1 || q.Limit > MaxPageSize || len(q.Statuses) > 5 || len(q.Label) > MaxLabelBytes {
		return ErrInvalidInput
	}
	for _, status := range q.Statuses {
		if !status.Valid() {
			return ErrInvalidInput
		}
	}
	if q.From != nil && q.Until != nil && !q.From.Before(*q.Until) {
		return fmt.Errorf("%w: invalid time window", ErrInvalidInput)
	}
	if (q.CursorAt == nil) != (strings.TrimSpace(q.CursorID) == "") {
		return fmt.Errorf("%w: cursor fields must be paired", ErrInvalidInput)
	}
	return nil
}

type Page struct {
	Items     []Reminder `json:"items"`
	NextAt    *time.Time `json:"next_at,omitempty"`
	NextID    string     `json:"next_id,omitempty"`
	Truncated bool       `json:"truncated"`
}

type DueClaimRequest struct {
	Limit         int
	Now           time.Time
	LeaseDuration time.Duration
	Token         string
}

type CommitOccurrenceInput struct {
	ReminderID         string
	ExpectedRowVersion uint64
	ClaimToken         string
	OccurrenceID       string
	OccurredAt         time.Time
}

type DeliveryStatus string

const (
	DeliveryPending         DeliveryStatus = "pending"
	DeliveryProcessing      DeliveryStatus = "processing"
	DeliveryCompleted       DeliveryStatus = "completed"
	DeliveryPermanentFailed DeliveryStatus = "permanent_failed"
)

type Delivery struct {
	ID            string         `json:"id"`
	ReminderID    string         `json:"reminder_id"`
	Owner         Owner          `json:"-"`
	Content       string         `json:"content"`
	DeliveryKey   string         `json:"delivery_key"`
	Status        DeliveryStatus `json:"status"`
	Attempt       int            `json:"attempt"`
	AvailableAt   time.Time      `json:"available_at"`
	ClaimToken    string         `json:"-"`
	LeaseUntil    *time.Time     `json:"-"`
	LastErrorCode string         `json:"last_error_code,omitempty"`
}

type DeliveryFailure struct {
	ID, ClaimToken, ErrorCode string
	Now, NextAvailable        time.Time
	Permanent                 bool
}

type Repository interface {
	Create(context.Context, CreateInput) (MutationResult, error)
	Get(context.Context, Owner, string) (Reminder, error)
	List(context.Context, Query) (Page, error)
	Update(context.Context, MutationInput) (MutationResult, error)
	Cancel(context.Context, MutationInput) (MutationResult, error)
	ClaimDue(context.Context, DueClaimRequest) ([]Reminder, error)
	RenewClaim(context.Context, string, uint64, string, time.Time) error
	CommitOccurrence(context.Context, CommitOccurrenceInput) (Delivery, bool, error)
	ClaimDeliveries(context.Context, int, time.Time, time.Duration, string) ([]Delivery, error)
	CompleteDelivery(context.Context, string, string, time.Time) error
	FailDelivery(context.Context, DeliveryFailure) error
}
