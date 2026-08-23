package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validRecord(now time.Time) Record {
	text, value, hash, _ := NormalizeContent("User prefers tea", StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"drink": "tea"}})
	return Record{ID: "mem-1", Owner: Owner{TenantID: 1, UserID: 2}, Layer: LayerLongTerm, Kind: KindPreference, Scope: Scope{Type: ScopeUser}, Namespace: "profile", SlotKey: "drink", LineageID: "line-1", LineageVersion: 1, RowVersion: 1, CanonicalText: text, StructuredValue: value, ContentHash: hash, Authority: AuthorityUserConfirmed, Confidence: 1, Salience: .8, Source: SourceRef{Type: "workflow", ID: "wf-1"}, Status: StatusActive, CreatedAt: now, UpdatedAt: now}
}

func TestRecordLayerBoundaries(t *testing.T) {
	now := time.Now().UTC()
	base := validRecord(now)
	working := base
	working.Layer = LayerWorking
	if !errors.Is(working.Validate(now), ErrInvalidInput) {
		t.Fatal("working memory must not persist")
	}
	session := base
	session.Layer = LayerSession
	session.Scope = Scope{Type: ScopeSession, ID: "session-1"}
	session.ExpiresAt = nil
	if !errors.Is(session.Validate(now), ErrInvalidInput) {
		t.Fatal("session memory requires expiry")
	}
	expiry := now.Add(time.Hour)
	session.ExpiresAt = &expiry
	if err := session.Validate(now); err != nil {
		t.Fatalf("valid session: %v", err)
	}
	long := base
	long.Scope = Scope{Type: ScopeSession, ID: "session-1"}
	if !errors.Is(long.Validate(now), ErrInvalidInput) {
		t.Fatal("long term memory must use user scope")
	}
}

func TestNormalizeContentStableAndBounded(t *testing.T) {
	value := StructuredValue{Schema: "fact", Version: 1, Data: map[string]any{"b": 2, "a": "x"}}
	text1, _, hash1, err := NormalizeContent("  hello   world ", value)
	if err != nil {
		t.Fatal(err)
	}
	_, _, hash2, err := NormalizeContent("hello world", value)
	if err != nil {
		t.Fatal(err)
	}
	if text1 != "hello world" || hash1 != hash2 {
		t.Fatalf("normalization unstable: %q %q %q", text1, hash1, hash2)
	}
	deep := map[string]any{}
	current := deep
	for i := 0; i < MaxStructuredDepth+1; i++ {
		next := map[string]any{}
		current["x"] = next
		current = next
	}
	if _, _, _, err := NormalizeContent("x", StructuredValue{Schema: "x", Version: 1, Data: deep}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("deep value error=%v", err)
	}
	if _, _, _, err := NormalizeContent(strings.Repeat("x", MaxCanonicalTextBytes+1), value); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("large text error=%v", err)
	}
}

func TestStatusTransitionMatrix(t *testing.T) {
	allowed := map[[2]Status]bool{{StatusCandidate, StatusActive}: true, {StatusCandidate, StatusRejected}: true, {StatusCandidate, StatusExpired}: true, {StatusActive, StatusSuperseded}: true, {StatusActive, StatusRevoked}: true, {StatusActive, StatusExpired}: true}
	all := []Status{StatusCandidate, StatusActive, StatusRejected, StatusSuperseded, StatusRevoked, StatusExpired}
	for _, from := range all {
		for _, to := range all {
			if got := from.CanTransition(to); got != allowed[[2]Status{from, to}] {
				t.Fatalf("%s -> %s = %v", from, to, got)
			}
		}
	}
}

func TestFakeRepositoryOwnerVersionAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := NewFakeRepository()
	now := time.Now().UTC()
	value := validRecord(now)
	m := Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "exec:0", InputHash: "h1", OccurredAt: now}
	first, err := repo.CommitMutation(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.CommitMutation(ctx, m)
	if err != nil || !replay.Replayed || replay.Memory.ID != first.Memory.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	m.InputHash = "h2"
	if _, err := repo.CommitMutation(ctx, m); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error=%v", err)
	}
	values, err := repo.BatchGet(ctx, Owner{TenantID: 1, UserID: 99}, []string{value.ID})
	if err != nil || len(values) != 0 {
		t.Fatalf("cross-owner values=%v err=%v", values, err)
	}
	if _, err := repo.TransitionMemory(ctx, value.Owner, value.ID, 99, StatusRevoked, "user", "withdrawn", "exec:1", "h", now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale version error=%v", err)
	}
}

func TestRelationProposalCodec(t *testing.T) {
	allowed := map[string]struct{}{"mem-1": {}}
	raw, _ := json.Marshal([]RelationProposal{{MemoryID: "mem-1", Relation: ProposalCorrection, Confidence: .9, ReasonCode: "user_correction"}})
	if _, err := DecodeRelationProposals(raw, allowed, 2); err != nil {
		t.Fatal(err)
	}
	for _, relation := range []ProposedRelation{ProposalDuplicate, ProposalRefinement, ProposalCorrection, ProposalContradiction, ProposalTemporalChange, ProposalIndependent} {
		raw, _ := json.Marshal([]RelationProposal{{MemoryID: "mem-1", Relation: relation, Confidence: 1, ReasonCode: "bounded"}})
		if _, err := DecodeRelationProposals(raw, allowed, 1); err != nil {
			t.Fatalf("relation %s: %v", relation, err)
		}
	}
	for _, bad := range [][]byte{[]byte(`[{"memory_id":"unknown","relation":"duplicate","confidence":1,"reason_code":"x"}]`), []byte(`[{"memory_id":"mem-1","relation":"delete","confidence":1,"reason_code":"x"}]`), []byte(`[{"memory_id":"mem-1","relation":"duplicate","confidence":2,"reason_code":"x"}]`), []byte(`[{"memory_id":"mem-1","relation":"duplicate","confidence":1,"reason_code":"x","store_command":"delete"}]`)} {
		if _, err := DecodeRelationProposals(bad, allowed, 2); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad codec error=%v", err)
		}
	}
}

func TestPolicyDecisionTable(t *testing.T) {
	now := time.Now().UTC()
	old := validRecord(now)
	fresh := old
	fresh.ID = "mem-2"
	fresh.ContentHash = "new"
	fresh.Entity = EntityRef{Type: "task", ID: "1"}
	old.Entity = fresh.Entity
	tests := []struct {
		name   string
		in     PolicyInput
		action PolicyAction
	}{
		{"duplicate", PolicyInput{NewMemory: old, Existing: old}, ActionNoop},
		{"correction", PolicyInput{NewMemory: fresh, Existing: old, Intent: IntentUserCorrection, Proposal: RelationProposal{Relation: ProposalCorrection, Confidence: 1, ReasonCode: "corrected"}}, ActionSupersede},
		{"model conflict", PolicyInput{NewMemory: fresh, Existing: old, Intent: IntentModelInference, Proposal: RelationProposal{Relation: ProposalContradiction, Confidence: 1}}, ActionReview},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecidePolicy(tt.in); got.Action != tt.action {
				t.Fatalf("decision=%+v", got)
			}
		})
	}
	other := fresh
	other.Entity.ID = "2"
	if got := DecidePolicy(PolicyInput{NewMemory: other, Existing: old, Proposal: RelationProposal{Relation: ProposalDuplicate, Confidence: 1}}); got.Action != ActionIndependent {
		t.Fatalf("different entity=%+v", got)
	}
}

func TestSensitiveContentRejected(t *testing.T) {
	for _, value := range []StructuredValue{{Schema: "x", Version: 1, Data: map[string]any{"access_token": "abc"}}, {Schema: "x", Version: 1, Data: map[string]any{"nested": []any{"Bearer abcdefghijklmnop"}}}} {
		if err := ValidateContent("safe", value, SourceRef{Type: "workflow", ID: "wf"}); !errors.Is(err, ErrSensitiveContent) {
			t.Fatalf("error=%v", err)
		}
	}
	if err := ValidateContent("safe", StructuredValue{Schema: "x", Version: 1, Data: map[string]any{"x": "y"}}, SourceRef{Type: "unknown", ID: "1"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("source whitelist error=%v", err)
	}
	if _, _, err := NormalizeAuditFields("Bearer secret-token", "created"); !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("audit error=%v", err)
	}
}
