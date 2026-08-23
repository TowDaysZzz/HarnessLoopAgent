package memoryworkflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

type editPayloadEntry struct {
	owner    memory.Owner
	draft    Draft
	expires  time.Time
	consumed bool
}
type editPayloadStoreFake struct {
	values map[string]editPayloadEntry
	puts   int
}

func (s *editPayloadStoreFake) PutMemoryEditPayload(_ context.Context, owner memory.Owner, id string, draft Draft, expires, _ time.Time) error {
	if s.values == nil {
		s.values = map[string]editPayloadEntry{}
	}
	s.values[id] = editPayloadEntry{owner: owner, draft: draft, expires: expires}
	s.puts++
	return nil
}
func (s *editPayloadStoreFake) ConsumeMemoryEditPayload(_ context.Context, owner memory.Owner, id string, now time.Time) (Draft, error) {
	value, ok := s.values[id]
	if !ok || value.owner != owner || value.consumed || !now.Before(value.expires) {
		return Draft{}, memory.ErrNotFound
	}
	value.consumed = true
	s.values[id] = value
	return value.draft, nil
}

func TestEditPayloadServiceOwnerExpiryAndOneTimeRead(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := &editPayloadStoreFake{}
	service := EditPayloadService{Store: store, Extractor: fakeExtractor{draft: captureDraft("coffee", "coffee")}, TTL: time.Hour, Now: func() time.Time { return now }, NewID: func() string { return "edit-1" }}
	owner := memory.Owner{TenantID: 1, UserID: 2}
	ref, err := service.Create(context.Background(), owner, "改成咖啡")
	if err != nil || ref != "memory-edit:edit-1" || store.puts != 1 {
		t.Fatalf("ref=%q puts=%d err=%v", ref, store.puts, err)
	}
	if _, err := service.LoadEditedMemoryDraft(context.Background(), memory.Owner{TenantID: 1, UserID: 3}, ref); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("cross owner err=%v", err)
	}
	draft, err := service.LoadEditedMemoryDraft(context.Background(), owner, ref)
	if err != nil || draft.SlotKey != "drink" {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	if _, err := service.LoadEditedMemoryDraft(context.Background(), owner, ref); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("repeat err=%v", err)
	}
	service.NewID = func() string { return "edit-expired" }
	ref, err = service.Create(context.Background(), owner, "改成水")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := service.LoadEditedMemoryDraft(context.Background(), owner, ref); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("expired err=%v", err)
	}
}

func TestEditPayloadServiceRejectsSecretsAndOversize(t *testing.T) {
	now := time.Now().UTC()
	store := &editPayloadStoreFake{}
	secret := captureDraft("Bearer abcdefghijklmnop", "tea")
	service := EditPayloadService{Store: store, Extractor: fakeExtractor{draft: secret}, TTL: time.Hour, Now: func() time.Time { return now }}
	if _, err := service.Create(context.Background(), memory.Owner{TenantID: 1, UserID: 2}, "secret"); !errors.Is(err, memory.ErrSensitiveContent) {
		t.Fatalf("secret err=%v", err)
	}
	if store.puts != 0 {
		t.Fatal("sensitive draft must not be persisted")
	}
	if _, err := service.Create(context.Background(), memory.Owner{TenantID: 1, UserID: 2}, strings.Repeat("x", MaxEditTextBytes+1)); !errors.Is(err, ErrInvalidEditPayload) {
		t.Fatalf("oversize err=%v", err)
	}
}
