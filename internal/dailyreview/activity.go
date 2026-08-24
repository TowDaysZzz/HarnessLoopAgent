package dailyreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

var ErrStaleSnapshot = errors.New("daily review stale snapshot")

type Window struct {
	LocalDate string    `json:"local_date"`
	Timezone  string    `json:"timezone"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
}

func ResolveWindow(date, timezone string) (Window, error) {
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return Window{}, err
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), loc)
	if err != nil {
		return Window{}, err
	}
	return Window{LocalDate: day.Format("2006-01-02"), Timezone: loc.String(), Start: day.UTC(), End: day.AddDate(0, 0, 1).UTC()}, nil
}

func (w Window) Valid() bool {
	return w.LocalDate != "" && w.Timezone != "" && !w.Start.IsZero() && w.End.After(w.Start) && w.End.Sub(w.Start) >= 23*time.Hour && w.End.Sub(w.Start) <= 25*time.Hour
}

type Options struct {
	MaxChatMessages int  `json:"max_chat_messages"`
	PerSession      int  `json:"per_session"`
	MaxNotes        int  `json:"max_notes"`
	IncludeMemory   bool `json:"include_memory"`
}

func (o Options) Normalize() (Options, error) {
	if o.MaxChatMessages == 0 {
		o.MaxChatMessages = 200
	}
	if o.PerSession == 0 {
		o.PerSession = 50
	}
	if o.MaxNotes == 0 {
		o.MaxNotes = 100
	}
	if o.MaxChatMessages < 1 || o.MaxChatMessages > 1000 || o.PerSession < 1 || o.PerSession > o.MaxChatMessages || o.MaxNotes < 1 || o.MaxNotes > 500 {
		return Options{}, skill.ErrInvalidInvocation
	}
	return o, nil
}

type ChatRef struct {
	ID, SessionID, RunID, Role, ContentHash string
	Sequence                                int64
	CreatedAt                               time.Time
}

type NoteRef struct {
	ID, Status, ContentHash string
	Version                 uint64
	OccurredAt, UpdatedAt   time.Time
}

type MemoryRef struct {
	ID, ContentHash string
	LineageVersion  uint64
	ExpiresAt       *time.Time
}

type CoverageWarning struct {
	Source, Code        string
	Included, Available int
}

type SourceSnapshot struct {
	Owner                          skill.Owner
	Window                         Window
	OptionsHash                    string
	Chat                           []ChatRef
	Notes                          []NoteRef
	MemoryMutationVersion          uint64
	Warnings                       []CoverageWarning
	ChatDigest, NoteDigest, Digest string
}

func OptionsHash(options Options) (string, error) {
	normalized, err := options.Normalize()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(normalized)
	return hashBytes(b), nil
}

func (s *SourceSnapshot) Normalize() error {
	if !s.Owner.Valid() || !s.Window.Valid() || len(s.Chat) > 1000 || len(s.Notes) > 500 || len(s.Warnings) > 8 {
		return skill.ErrInvalidInvocation
	}
	sort.Slice(s.Chat, func(i, j int) bool {
		a, b := s.Chat[i], s.Chat[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Sequence != b.Sequence {
			return a.Sequence < b.Sequence
		}
		return a.ID < b.ID
	})
	sort.Slice(s.Notes, func(i, j int) bool {
		a, b := s.Notes[i], s.Notes[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		return a.ID < b.ID
	})
	for _, ref := range s.Chat {
		if ref.ID == "" || ref.SessionID == "" || ref.ContentHash == "" || ref.CreatedAt.Before(s.Window.Start) || !ref.CreatedAt.Before(s.Window.End) {
			return skill.ErrInvalidInvocation
		}
	}
	for _, ref := range s.Notes {
		if ref.ID == "" || ref.Version == 0 || ref.ContentHash == "" || ref.OccurredAt.Before(s.Window.Start) || !ref.OccurredAt.Before(s.Window.End) {
			return skill.ErrInvalidInvocation
		}
	}
	chat, _ := json.Marshal(s.Chat)
	notes, _ := json.Marshal(s.Notes)
	s.ChatDigest, s.NoteDigest = hashBytes(chat), hashBytes(notes)
	identity := struct {
		Owner                               skill.Owner
		Window                              Window
		OptionsHash, ChatDigest, NoteDigest string
		MemoryVersion                       uint64
		Warnings                            []CoverageWarning
	}{s.Owner, s.Window, s.OptionsHash, s.ChatDigest, s.NoteDigest, s.MemoryMutationVersion, s.Warnings}
	b, _ := json.Marshal(identity)
	s.Digest = hashBytes(b)
	return nil
}

type Evidence struct {
	Chat  map[string]string
	Notes map[string]string
}

type ChatActivitySource interface {
	SnapshotChat(context.Context, skill.Owner, Window, int, int) ([]ChatRef, bool, error)
	LoadChatPinned(context.Context, skill.Owner, []ChatRef) (map[string]string, error)
}
type NoteActivitySource interface {
	SnapshotNotes(context.Context, skill.Owner, Window, int) ([]NoteRef, bool, error)
	LoadNotesPinned(context.Context, skill.Owner, []NoteRef) (map[string]string, error)
}
type MemoryVersionSource interface {
	MemoryMutationVersion(context.Context, skill.Owner) (uint64, error)
}

type ActivityReader struct {
	Chat   ChatActivitySource
	Notes  NoteActivitySource
	Memory MemoryVersionSource
}

func (r ActivityReader) Snapshot(ctx context.Context, owner skill.Owner, window Window, options Options) (SourceSnapshot, error) {
	options, err := options.Normalize()
	if err != nil || !owner.Valid() || !window.Valid() || r.Chat == nil || r.Notes == nil || r.Memory == nil {
		return SourceSnapshot{}, skill.ErrUnavailable
	}
	h, _ := OptionsHash(options)
	snapshot := SourceSnapshot{Owner: owner, Window: window, OptionsHash: h}
	type result struct {
		kind      string
		chat      []ChatRef
		notes     []NoteRef
		truncated bool
		version   uint64
		err       error
	}
	results := make(chan result, 3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		v, t, e := r.Chat.SnapshotChat(ctx, owner, window, options.MaxChatMessages, options.PerSession)
		results <- result{kind: "chat", chat: v, truncated: t, err: e}
	}()
	go func() {
		defer wg.Done()
		v, t, e := r.Notes.SnapshotNotes(ctx, owner, window, options.MaxNotes)
		results <- result{kind: "note", notes: v, truncated: t, err: e}
	}()
	go func() {
		defer wg.Done()
		v, e := r.Memory.MemoryMutationVersion(ctx, owner)
		results <- result{kind: "memory", version: v, err: e}
	}()
	go func() { wg.Wait(); close(results) }()
	for value := range results {
		if value.err != nil {
			return SourceSnapshot{}, value.err
		}
		switch value.kind {
		case "chat":
			snapshot.Chat = value.chat
			if value.truncated {
				snapshot.Warnings = append(snapshot.Warnings, CoverageWarning{"chat", "limit_reached", len(value.chat), len(value.chat) + 1})
			}
		case "note":
			snapshot.Notes = value.notes
			if value.truncated {
				snapshot.Warnings = append(snapshot.Warnings, CoverageWarning{"note", "limit_reached", len(value.notes), len(value.notes) + 1})
			}
		case "memory":
			snapshot.MemoryMutationVersion = value.version
		}
	}
	if err := snapshot.Normalize(); err != nil {
		return SourceSnapshot{}, err
	}
	return snapshot, nil
}

func (r ActivityReader) LoadPinned(ctx context.Context, snapshot SourceSnapshot) (Evidence, error) {
	if err := snapshot.Normalize(); err != nil {
		return Evidence{}, err
	}
	type result struct {
		kind   string
		values map[string]string
		err    error
	}
	results := make(chan result, 2)
	go func() {
		v, e := r.Chat.LoadChatPinned(ctx, snapshot.Owner, snapshot.Chat)
		results <- result{"chat", v, e}
	}()
	go func() {
		v, e := r.Notes.LoadNotesPinned(ctx, snapshot.Owner, snapshot.Notes)
		results <- result{"note", v, e}
	}()
	out := Evidence{}
	for i := 0; i < 2; i++ {
		v := <-results
		if v.err != nil {
			return Evidence{}, v.err
		}
		if v.kind == "chat" {
			out.Chat = v.values
		} else {
			out.Notes = v.values
		}
	}
	return out, nil
}

func hashBytes(value []byte) string   { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func ContentHash(value string) string { return hashBytes([]byte(value)) }
