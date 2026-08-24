package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeRepositoryFindExactIsBoundedAndOwnerScoped(t *testing.T) {
	now := time.Now().UTC()
	repo := NewFakeRepository()
	first := validRecord(now)
	first.ID, first.LineageID, first.SlotKey = "mem-a", "line-a", "drink"
	first.Entity = EntityRef{Type: "task", ID: "task-1"}
	storeRecallRecord(t, repo, first, "exact-a")
	second := validRecord(now)
	second.ID, second.LineageID, second.SlotKey = "mem-b", "line-b", "timezone"
	second.Kind = KindFact
	second.Entity = EntityRef{Type: "reminder", ID: "reminder-1"}
	storeRecallRecord(t, repo, second, "exact-b")
	obsolete := validRecord(now)
	obsolete.ID, obsolete.LineageID, obsolete.SlotKey = "mem-c", "line-c", "obsolete"
	storeRecallRecord(t, repo, obsolete, "exact-c")
	if _, err := repo.TransitionMemory(context.Background(), obsolete.Owner, obsolete.ID, 1, StatusRevoked, "user", "revoke", "exact-revoke", obsolete.ContentHash, now); err != nil {
		t.Fatal(err)
	}

	activeAt := now.Add(time.Second)
	values, err := repo.FindExact(context.Background(), ExactQuery{Owner: first.Owner, Scope: first.Scope, ActiveAt: &activeAt, Layers: []Layer{LayerLongTerm}, Kinds: []Kind{KindPreference, KindFact}, Slots: []SlotSelector{{Namespace: "profile", SlotKey: "drink"}, {Namespace: "profile", SlotKey: "timezone"}, {Namespace: "profile", SlotKey: "obsolete"}}, Entities: []EntityRef{{Type: "task", ID: "task-1"}}, ContentHashes: []string{second.ContentHash}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].ID != "mem-a" || values[1].ID != "mem-b" {
		t.Fatalf("stable active results=%+v", values)
	}

	empty, err := repo.FindExact(context.Background(), ExactQuery{Owner: first.Owner, Scope: first.Scope, Limit: 10})
	if err != nil || len(empty) != 0 {
		t.Fatalf("selector-free query=%+v err=%v", empty, err)
	}
	crossOwner, err := repo.FindExact(context.Background(), ExactQuery{Owner: Owner{TenantID: first.Owner.TenantID, UserID: first.Owner.UserID + 1}, Scope: first.Scope, Slots: []SlotSelector{{Namespace: "profile", SlotKey: "drink"}}, Limit: 10})
	if err != nil || len(crossOwner) != 0 {
		t.Fatalf("cross owner=%+v err=%v", crossOwner, err)
	}
}

func TestExactQueryRejectsUnboundedOrMalformedInput(t *testing.T) {
	owner := Owner{TenantID: 1, UserID: 2}
	tests := []ExactQuery{
		{Scope: Scope{Type: ScopeUser}, Slots: []SlotSelector{{Namespace: "profile", SlotKey: "drink"}}, Limit: 10},
		{Owner: owner, Scope: Scope{Type: ScopeUser}, Slots: []SlotSelector{{Namespace: "profile", SlotKey: "drink"}}, Limit: 0},
		{Owner: owner, Scope: Scope{Type: ScopeSession}, Slots: []SlotSelector{{Namespace: "profile", SlotKey: "drink"}}, Limit: 10},
		{Owner: owner, Scope: Scope{Type: ScopeUser}, Namespace: "profile", Limit: 10},
		{Owner: owner, Scope: Scope{Type: ScopeUser}, ContentHashes: []string{"not-a-hash"}, Limit: 10},
	}
	for i, query := range tests {
		if err := query.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d err=%v", i, err)
		}
	}
}

func TestMutationVersionAdvancesOncePerSuccessfulMutationAndIsOwnerScoped(t *testing.T) {
	repo := NewFakeRepository()
	now := time.Now().UTC()
	value := exactRecallRecord(t, now, "versioned", "slot", "", "", "value")
	result, err := repo.CommitMutation(context.Background(), Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "version-create", InputHash: value.ContentHash, OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := repo.MutationVersion(context.Background(), value.Owner)
	if first != 1 {
		t.Fatalf("first version=%d", first)
	}
	if replay, err := repo.CommitMutation(context.Background(), Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "version-create", InputHash: value.ContentHash, OccurredAt: now}); err != nil || !replay.Replayed {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if got, _ := repo.MutationVersion(context.Background(), value.Owner); got != first {
		t.Fatalf("replay advanced version=%d", got)
	}
	if _, err := repo.TransitionMemory(context.Background(), value.Owner, result.Memory.ID, result.Memory.RowVersion, StatusRevoked, "user", "revoke", "version-revoke", value.ContentHash, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.MutationVersion(context.Background(), value.Owner); got != 2 {
		t.Fatalf("transition version=%d", got)
	}
	if got, _ := repo.MutationVersion(context.Background(), Owner{TenantID: value.Owner.TenantID, UserID: value.Owner.UserID + 1}); got != 0 {
		t.Fatalf("cross owner version=%d", got)
	}
}
