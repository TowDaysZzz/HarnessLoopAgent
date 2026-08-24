package reminderworkflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
)

var ErrInvalidEditPayload = errors.New("invalid reminder edit payload")

type EditPayloadStore interface {
	PutReminderEditPayload(context.Context, reminder.Owner, string, string, time.Time, time.Time) error
	ConsumeReminderEditPayload(context.Context, reminder.Owner, string, time.Time) (string, error)
}

type EditLoader interface {
	LoadReminderEdit(context.Context, reminder.Owner, string) (string, error)
}

type EditPayloadService struct {
	Store EditPayloadStore
	TTL   time.Duration
	Now   func() time.Time
	NewID func() string
}

func (s EditPayloadService) Create(ctx context.Context, owner reminder.Owner, text string) (string, error) {
	text = strings.TrimSpace(text)
	if s.Store == nil || !owner.Valid() || s.TTL <= 0 || text == "" || len(text) > 4096 || containsCredential([]byte(text)) {
		return "", ErrInvalidEditPayload
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	id := uuid.NewString()
	if s.NewID != nil {
		id = strings.TrimSpace(s.NewID())
	}
	if id == "" || len(id) > 191 {
		return "", ErrInvalidEditPayload
	}
	if err := s.Store.PutReminderEditPayload(ctx, owner, id, text, now.Add(s.TTL), now); err != nil {
		return "", err
	}
	return "reminder-edit:" + id, nil
}

func (s EditPayloadService) LoadReminderEdit(ctx context.Context, owner reminder.Owner, ref string) (string, error) {
	if s.Store == nil || !owner.Valid() || !strings.HasPrefix(ref, "reminder-edit:") {
		return "", reminder.ErrNotFound
	}
	id := strings.TrimPrefix(ref, "reminder-edit:")
	if id == "" || len(id) > 191 {
		return "", reminder.ErrNotFound
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	return s.Store.ConsumeReminderEditPayload(ctx, owner, id, now)
}

type memoryEditValue struct {
	owner     reminder.Owner
	text      string
	expiresAt time.Time
	consumed  bool
}

type MemoryEditPayloadStore struct {
	mu     sync.Mutex
	values map[string]memoryEditValue
}

func NewMemoryEditPayloadStore() *MemoryEditPayloadStore {
	return &MemoryEditPayloadStore{values: map[string]memoryEditValue{}}
}

func (s *MemoryEditPayloadStore) PutReminderEditPayload(_ context.Context, owner reminder.Owner, id, text string, expiresAt, now time.Time) error {
	if s == nil || !owner.Valid() || id == "" || text == "" || !expiresAt.After(now) {
		return ErrInvalidEditPayload
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := editKey(owner, id)
	if _, exists := s.values[key]; exists {
		return reminder.ErrIdempotencyConflict
	}
	s.values[key] = memoryEditValue{owner: owner, text: text, expiresAt: expiresAt}
	return nil
}

func (s *MemoryEditPayloadStore) ConsumeReminderEditPayload(_ context.Context, owner reminder.Owner, id string, now time.Time) (string, error) {
	if s == nil {
		return "", reminder.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := editKey(owner, id)
	value, exists := s.values[key]
	if !exists || value.consumed || !value.expiresAt.After(now) {
		return "", reminder.ErrNotFound
	}
	value.consumed = true
	s.values[key] = value
	return value.text, nil
}

func editKey(owner reminder.Owner, id string) string {
	return strings.Join([]string{fmtUint(owner.TenantID), fmtUint(owner.UserID), id}, ":")
}

func fmtUint(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	i := len(buffer)
	for value > 0 {
		i--
		buffer[i] = digits[value%10]
		value /= 10
	}
	return string(buffer[i:])
}
