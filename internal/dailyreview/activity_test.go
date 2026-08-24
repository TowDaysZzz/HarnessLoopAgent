package dailyreview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

type activitySourceStub struct {
	mu                           sync.Mutex
	chat                         []ChatRef
	notes                        []NoteRef
	chatBodies                   map[string]string
	noteBodies                   map[string]string
	chatTruncated, noteTruncated bool
	version                      uint64
	loads                        int
}

func (s *activitySourceStub) SnapshotChat(context.Context, skill.Owner, Window, int, int) ([]ChatRef, bool, error) {
	return append([]ChatRef(nil), s.chat...), s.chatTruncated, nil
}
func (s *activitySourceStub) SnapshotNotes(context.Context, skill.Owner, Window, int) ([]NoteRef, bool, error) {
	return append([]NoteRef(nil), s.notes...), s.noteTruncated, nil
}
func (s *activitySourceStub) MemoryMutationVersion(context.Context, skill.Owner) (uint64, error) {
	return s.version, nil
}
func (s *activitySourceStub) LoadChatPinned(_ context.Context, _ skill.Owner, refs []ChatRef) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	for _, r := range refs {
		if ContentHash(s.chatBodies[r.ID]) != r.ContentHash {
			return nil, ErrStaleSnapshot
		}
	}
	return s.chatBodies, nil
}
func (s *activitySourceStub) LoadNotesPinned(_ context.Context, _ skill.Owner, refs []NoteRef) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	for _, r := range refs {
		if ContentHash(s.noteBodies[r.ID]) != r.ContentHash {
			return nil, ErrStaleSnapshot
		}
	}
	return s.noteBodies, nil
}

func TestResolveWindowUsesLocalDayAndDST(t *testing.T) {
	w, err := ResolveWindow("2026-03-08", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.End.Sub(w.Start); got != 23*time.Hour {
		t.Fatalf("DST window=%v", got)
	}
}

func TestSnapshotDigestIsStableForEquivalentOrdering(t *testing.T) {
	w, _ := ResolveWindow("2026-08-24", "Asia/Shanghai")
	owner := skill.Owner{TenantID: 1, UserID: 2}
	at := w.Start.Add(time.Hour)
	a := SourceSnapshot{Owner: owner, Window: w, OptionsHash: ContentHash("options"), MemoryMutationVersion: 3, Chat: []ChatRef{{ID: "b", SessionID: "s", Role: "assistant", Sequence: 2, ContentHash: ContentHash("b"), CreatedAt: at.Add(time.Second)}, {ID: "a", SessionID: "s", Role: "user", Sequence: 1, ContentHash: ContentHash("a"), CreatedAt: at}}, Notes: []NoteRef{{ID: "n", Status: "indexed", Version: 1, ContentHash: ContentHash("n"), OccurredAt: at, UpdatedAt: at}}}
	b := a
	b.Chat = []ChatRef{a.Chat[1], a.Chat[0]}
	if err := a.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := b.Normalize(); err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest || a.ChatDigest != b.ChatDigest {
		t.Fatalf("unstable digest: %s %s", a.Digest, b.Digest)
	}
}

func TestActivityReaderIsBoundedAndPinnedLoadDetectsStale(t *testing.T) {
	w, _ := ResolveWindow("2026-08-24", "Asia/Shanghai")
	at := w.Start.Add(time.Hour)
	stub := &activitySourceStub{version: 7, chatTruncated: true, noteTruncated: true, chatBodies: map[string]string{"c": "changed"}, noteBodies: map[string]string{"n": "note"}, chat: []ChatRef{{ID: "c", SessionID: "s", Role: "user", Sequence: 1, ContentHash: ContentHash("original"), CreatedAt: at}}, notes: []NoteRef{{ID: "n", Status: "indexed", Version: 1, ContentHash: ContentHash("note"), OccurredAt: at, UpdatedAt: at}}}
	r := ActivityReader{Chat: stub, Notes: stub, Memory: stub}
	snapshot, err := r.Snapshot(context.Background(), skill.Owner{TenantID: 1, UserID: 2}, w, Options{MaxChatMessages: 2, PerSession: 1, MaxNotes: 1, IncludeMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryMutationVersion != 7 || len(snapshot.Warnings) != 2 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if _, err := r.LoadPinned(context.Background(), snapshot); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("load error=%v", err)
	}
}

func TestSnapshotRejectsOutOfWindowAndLimits(t *testing.T) {
	w, _ := ResolveWindow("2026-08-24", "Asia/Shanghai")
	s := SourceSnapshot{Owner: skill.Owner{TenantID: 1, UserID: 2}, Window: w, OptionsHash: ContentHash("o"), Chat: []ChatRef{{ID: "c", SessionID: "s", Role: "user", ContentHash: ContentHash("x"), CreatedAt: w.End}}}
	if err := s.Normalize(); err == nil {
		t.Fatal("expected window boundary rejection")
	}
	if _, err := (Options{MaxChatMessages: 1001, PerSession: 1, MaxNotes: 1}).Normalize(); err == nil {
		t.Fatal("expected limit rejection")
	}
}
