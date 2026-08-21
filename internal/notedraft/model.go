package notedraft

import "time"

type Status string

const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
	StatusExpired   Status = "expired"
)

type Owner struct {
	UserID   uint64
	TenantID uint64
}

type Draft struct {
	ID          string    `json:"id"`
	UserID      uint64    `json:"-"`
	TenantID    uint64    `json:"-"`
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	Status      Status    `json:"status"`
	ContentHash string    `json:"content_hash"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
