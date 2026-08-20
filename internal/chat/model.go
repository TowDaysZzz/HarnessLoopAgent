package chat

import "time"

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	RunID     string    `json:"run_id,omitempty"`
	Sequence  int64     `json:"sequence"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type RunStatus string

const (
	RunQueued      RunStatus = "queued"
	RunRunning     RunStatus = "running"
	RunCompleted   RunStatus = "completed"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunTimedOut    RunStatus = "timed_out"
	RunInterrupted RunStatus = "interrupted"
)

func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCancelled || s == RunTimedOut || s == RunInterrupted
}

type Run struct {
	ID             string     `json:"id"`
	SessionID      string     `json:"session_id"`
	Status         RunStatus  `json:"status"`
	Model          string     `json:"model,omitempty"`
	IdempotencyKey string     `json:"-"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type Event struct {
	RunID     string         `json:"run_id"`
	Sequence  int64          `json:"sequence"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
	CreatedAt time.Time      `json:"created_at"`
}

type CreateRunInput struct {
	SessionID        string
	Content          string
	Model            string
	IdempotencyKey   string
	UserAccessToken  string
	KnowledgeBaseIDs []uint64
}

type CreatedRun struct {
	Run     Run
	Created bool
}
