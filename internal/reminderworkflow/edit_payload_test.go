package reminderworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
)

func TestEditPayloadIsOwnerScopedExpiringAndSingleUse(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	store := NewMemoryEditPayloadStore()
	service := EditPayloadService{Store: store, TTL: time.Hour, Now: func() time.Time { return now }, NewID: func() string { return "edit-1" }}
	owner := reminder.Owner{TenantID: 1, UserID: 2}
	ref, err := service.Create(context.Background(), owner, "改到明天十点")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoadReminderEdit(context.Background(), reminder.Owner{TenantID: 1, UserID: 3}, ref); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatalf("cross owner err=%v", err)
	}
	text, err := service.LoadReminderEdit(context.Background(), owner, ref)
	if err != nil || text != "改到明天十点" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if _, err := service.LoadReminderEdit(context.Background(), owner, ref); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatalf("replay err=%v", err)
	}
	service.NewID = func() string { return "edit-2" }
	ref, err = service.Create(context.Background(), owner, "改到后天十点")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := service.LoadReminderEdit(context.Background(), owner, ref); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatalf("expired err=%v", err)
	}
}
