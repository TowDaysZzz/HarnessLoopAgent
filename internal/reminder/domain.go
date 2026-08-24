package reminder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidInput        = errors.New("invalid reminder input")
	ErrNotFound            = errors.New("reminder not found")
	ErrStateConflict       = errors.New("reminder state conflict")
	ErrIdempotencyConflict = errors.New("reminder idempotency conflict")
	ErrLeaseLost           = errors.New("reminder lease lost")
	ErrSensitiveContent    = errors.New("reminder contains sensitive content")
)

const (
	MaxContentBytes    = 4096
	MaxLabelBytes      = 128
	MaxMemoryRefs      = 8
	MaxPageSize        = 100
	DefaultMaxHorizon  = 365 * 24 * time.Hour
	DefaultTimezone    = "Asia/Shanghai"
	ContentHashHexSize = 64
)

var sensitiveText = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/-]{8,}|access[_-]?token|refresh[_-]?token|authorization\s*[:=]|cookie\s*[:=]|password\s*[:=]|api[_-]?key\s*[:=]|-----begin [a-z ]*private key-----)`)

type Owner struct {
	TenantID uint64 `json:"tenant_id"`
	UserID   uint64 `json:"user_id"`
}

func (o Owner) Valid() bool { return o.TenantID != 0 && o.UserID != 0 }

type Status string

const (
	StatusScheduled  Status = "scheduled"
	StatusProcessing Status = "processing"
	StatusFired      Status = "fired"
	StatusCancelled  Status = "cancelled"
	StatusFailed     Status = "failed"
)

func (s Status) Valid() bool {
	switch s {
	case StatusScheduled, StatusProcessing, StatusFired, StatusCancelled, StatusFailed:
		return true
	default:
		return false
	}
}

func (s Status) Terminal() bool { return s == StatusFired || s == StatusCancelled || s == StatusFailed }

func (s Status) CanTransition(to Status) bool {
	switch s {
	case StatusScheduled:
		return to == StatusProcessing || to == StatusCancelled
	case StatusProcessing:
		return to == StatusFired || to == StatusFailed
	default:
		return false
	}
}

type SourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type MemoryRef struct {
	ID             string `json:"id"`
	LineageVersion uint64 `json:"lineage_version"`
	ContentHash    string `json:"content_hash"`
}

func (r MemoryRef) Validate() error {
	if strings.TrimSpace(r.ID) == "" || r.LineageVersion == 0 || !validHash(r.ContentHash) {
		return fmt.Errorf("%w: invalid memory reference", ErrInvalidInput)
	}
	return nil
}

type Claim struct {
	Token      string    `json:"-"`
	LeaseUntil time.Time `json:"lease_until"`
}

type Reminder struct {
	ID            string      `json:"id"`
	Owner         Owner       `json:"-"`
	Content       string      `json:"content"`
	ContentHash   string      `json:"content_hash"`
	Timezone      string      `json:"timezone"`
	NextFireAt    time.Time   `json:"next_fire_at"`
	Status        Status      `json:"status"`
	RowVersion    uint64      `json:"row_version"`
	MemoryRefs    []MemoryRef `json:"memory_refs,omitempty"`
	Source        SourceRef   `json:"source"`
	LastErrorCode string      `json:"last_error_code,omitempty"`
	Claim         *Claim      `json:"-"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

func NormalizeContent(content string) (string, error) {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if content == "" || len(content) > MaxContentBytes {
		return "", fmt.Errorf("%w: reminder content is empty or too large", ErrInvalidInput)
	}
	if sensitiveText.MatchString(content) {
		return "", ErrSensitiveContent
	}
	return content, nil
}

func ComputeContentHash(content, timezone string, fireAt time.Time, refs []MemoryRef) (string, error) {
	content, err := NormalizeContent(content)
	if err != nil {
		return "", err
	}
	if timezone != DefaultTimezone || fireAt.IsZero() {
		return "", fmt.Errorf("%w: timezone and trigger are required", ErrInvalidInput)
	}
	copyRefs := append([]MemoryRef(nil), refs...)
	if len(copyRefs) > MaxMemoryRefs {
		return "", fmt.Errorf("%w: too many memory references", ErrInvalidInput)
	}
	for _, ref := range copyRefs {
		if err := ref.Validate(); err != nil {
			return "", err
		}
	}
	sort.Slice(copyRefs, func(i, j int) bool { return copyRefs[i].ID < copyRefs[j].ID })
	var value strings.Builder
	value.WriteString(content)
	value.WriteByte(0)
	value.WriteString(timezone)
	value.WriteByte(0)
	value.WriteString(fireAt.UTC().Format(time.RFC3339Nano))
	for _, ref := range copyRefs {
		fmt.Fprintf(&value, "\x00%s:%d:%s", ref.ID, ref.LineageVersion, strings.ToLower(ref.ContentHash))
	}
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:]), nil
}

func (r Reminder) Validate(now time.Time, maxHorizon time.Duration) error {
	if !r.Owner.Valid() || strings.TrimSpace(r.ID) == "" || r.RowVersion == 0 || !r.Status.Valid() || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: owner, id, status, versions and timestamps are required", ErrInvalidInput)
	}
	content, err := NormalizeContent(r.Content)
	if err != nil {
		return err
	}
	if r.Timezone != DefaultTimezone || r.NextFireAt.IsZero() || strings.TrimSpace(r.Source.Type) == "" || strings.TrimSpace(r.Source.ID) == "" {
		return fmt.Errorf("%w: timezone, trigger and source are required", ErrInvalidInput)
	}
	if sensitiveText.MatchString(r.Source.Type) || sensitiveText.MatchString(r.Source.ID) {
		return ErrSensitiveContent
	}
	hash, err := ComputeContentHash(content, r.Timezone, r.NextFireAt, r.MemoryRefs)
	if err != nil || hash != strings.ToLower(r.ContentHash) {
		return fmt.Errorf("%w: content hash mismatch", ErrInvalidInput)
	}
	if maxHorizon <= 0 {
		maxHorizon = DefaultMaxHorizon
	}
	if r.Status == StatusScheduled && (!r.NextFireAt.After(now) || r.NextFireAt.After(now.Add(maxHorizon))) {
		return fmt.Errorf("%w: scheduled trigger is outside allowed horizon", ErrInvalidInput)
	}
	if r.Claim != nil && (strings.TrimSpace(r.Claim.Token) == "" || r.Claim.LeaseUntil.IsZero()) {
		return fmt.Errorf("%w: invalid claim", ErrInvalidInput)
	}
	return nil
}

func validHash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != ContentHashHexSize {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
