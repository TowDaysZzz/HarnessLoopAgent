package notedraft

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDraftLifecycleIsolationReplacementAndIdempotency(t *testing.T) {
	repository := NewMemoryRepository()
	service, _ := NewService(repository, 24*time.Hour)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	owner := Owner{UserID: 1, TenantID: 2}
	first, err := service.Create(context.Background(), owner, "session-a", "Title", "Content")
	if err != nil || len(first.ContentHash) != 64 || first.Status != StatusPending {
		t.Fatalf("Create() = %#v, %v", first, err)
	}
	second, err := service.Create(context.Background(), owner, "session-a", "New", "Version")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Confirm(context.Background(), owner, "session-a", first.ID, first.ContentHash); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("old draft confirmation error = %v", err)
	}
	confirmed, replayed, err := service.Confirm(context.Background(), owner, "session-a", second.ID, second.ContentHash)
	if err != nil || replayed || confirmed.Status != StatusConfirmed {
		t.Fatalf("Confirm() = %#v, replayed=%v, err=%v", confirmed, replayed, err)
	}
	confirmed, replayed, err = repository.Transition(context.Background(), owner, "session-a", second.ID, second.ContentHash, StatusConfirmed, now)
	if err != nil || !replayed || confirmed.Status != StatusConfirmed {
		t.Fatalf("idempotent transition = %#v, replayed=%v, err=%v", confirmed, replayed, err)
	}
	for _, attempt := range []struct {
		owner   Owner
		session string
	}{
		{owner: Owner{UserID: 3, TenantID: 2}, session: "session-a"},
		{owner: owner, session: "session-b"},
	} {
		if _, _, err := repository.Transition(context.Background(), attempt.owner, attempt.session, second.ID, second.ContentHash, StatusCancelled, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-scope transition error = %v", err)
		}
	}
}

func TestDraftExpiry(t *testing.T) {
	service, _ := NewService(NewMemoryRepository(), time.Hour)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	owner := Owner{UserID: 1, TenantID: 2}
	draft, _ := service.Create(context.Background(), owner, "session", "Title", "Content")
	now = now.Add(2 * time.Hour)
	if _, _, err := service.Confirm(context.Background(), owner, "session", draft.ID, draft.ContentHash); !errors.Is(err, ErrExpired) {
		t.Fatalf("Confirm() error = %v", err)
	}
	if _, err := service.Latest(context.Background(), owner, "session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest() after expiration error = %v", err)
	}
}
