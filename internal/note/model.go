package note

import "time"

type Status string

const (
	StatusDraft         Status = "draft"
	StatusIndexing      Status = "indexing"
	StatusIndexed       Status = "indexed"
	StatusIndexFailed   Status = "index_failed"
	StatusDeletePending Status = "delete_pending"
	StatusDeleted       Status = "deleted"
)

type Note struct {
	ID             string     `json:"id"`
	UserID         uint64     `json:"-"`
	TenantID       uint64     `json:"-"`
	ExternalNoteID string     `json:"external_note_id"`
	Title          string     `json:"title"`
	Content        string     `json:"content"`
	Tags           []string   `json:"tags"`
	OccurredAt     *time.Time `json:"occurred_at,omitempty"`
	Status         Status     `json:"status"`
	RAGKBID        uint64     `json:"rag_kb_id"`
	RAGDocumentID  uint64     `json:"rag_document_id,omitempty"`
	RAGJobID       uint64     `json:"rag_job_id,omitempty"`
	RAGStatus      string     `json:"rag_status,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	ContentHash    string     `json:"-"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type OutboxEvent struct {
	ID          string
	NoteID      string
	UserID      uint64
	TenantID    uint64
	EventType   string
	Attempt     int
	CreatedAt   time.Time
	AvailableAt time.Time
}

type CreateInput struct {
	Title          string
	Content        string
	Tags           []string
	OccurredAt     *time.Time
	IdempotencyKey string
}
