package reminder

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

func TestBuildMemoryRecallPlanAcceptsOnlyExactSelectorsAndTrustedRefs(t *testing.T) {
	hash := strings.Repeat("a", 64)
	plan, err := BuildMemoryRecallPlan([]MemorySelector{
		{Type: memory.SelectorEntity, Entity: memory.EntityRef{Type: "task", ID: "weekly-report"}},
		{Type: memory.SelectorSlot, Namespace: "preferences", SlotKey: "weekly_report_format"},
		{Type: memory.SelectorContentHash, ContentHash: hash},
	}, []MemoryRef{{ID: "memory-fixed", LineageVersion: 2, ContentHash: hash}})
	if err != nil || len(plan.Plan.Selectors) != 3 || len(plan.Pinned) != 1 || plan.Plan.Selectors[0].Scope.Type != memory.ScopeUser {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for name, selectors := range map[string][]MemorySelector{
		"local scope":     {{Type: memory.SelectorLocalScope}},
		"slot incomplete": {{Type: memory.SelectorSlot, Namespace: "preferences"}},
		"bad hash":        {{Type: memory.SelectorContentHash, ContentHash: "not-a-hash"}},
	} {
		if _, err := BuildMemoryRecallPlan(selectors, nil); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s err=%v", name, err)
		}
	}
}

type fixedRecall struct{ result memory.RecallResult }

func (f fixedRecall) Recall(context.Context, memory.RecallRequest, time.Time) (memory.RecallResult, error) {
	return f.result, nil
}

func TestResolveMemoryAssociationFailsClosedForNoHitAmbiguousAndSemantic(t *testing.T) {
	owner := Owner{TenantID: 1, UserID: 2}
	plan, _ := BuildMemoryRecallPlan([]MemorySelector{{Type: memory.SelectorSlot, Namespace: "preferences", SlotKey: "drink"}}, nil)
	for name, result := range map[string]memory.RecallResult{
		"none":      {Mode: memory.RecallModeExactOnly},
		"ambiguous": {Mode: memory.RecallModeExactOnly, Items: []memory.RecallItem{{}, {}}},
	} {
		got, err := ResolveMemoryAssociation(context.Background(), fixedRecall{result}, owner, plan, time.Now())
		if err != nil || got.Clarification == nil || !got.Clarification.Needed {
			t.Fatalf("%s got=%+v err=%v", name, got, err)
		}
	}
	if _, err := ResolveMemoryAssociation(context.Background(), fixedRecall{memory.RecallResult{Mode: memory.RecallModeExactPlusSemantic}}, owner, plan, time.Now()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("semantic err=%v", err)
	}
}

func TestValidatePinnedMemoryRefsRejectsOwnerAndVersionStatusChanges(t *testing.T) {
	now := time.Now().UTC()
	owner := Owner{TenantID: 1, UserID: 2}
	repo := memory.NewFakeRepository()
	value := reminderMemoryRecord(t, owner, "memory-1", now, "偏好简洁周报")
	if _, err := repo.CommitMutation(context.Background(), memory.Mutation{Owner: value.Owner, NewMemory: &value, IdempotencyKey: "seed", InputHash: value.ContentHash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	ref := MemoryRef{ID: value.ID, LineageVersion: value.LineageVersion, ContentHash: value.ContentHash}
	if err := ValidatePinnedMemoryRefs(context.Background(), repo, owner, []MemoryRef{ref}, now); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePinnedMemoryRefs(context.Background(), repo, Owner{TenantID: 1, UserID: 9}, []MemoryRef{ref}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross owner err=%v", err)
	}
	badVersion := ref
	badVersion.LineageVersion++
	if err := ValidatePinnedMemoryRefs(context.Background(), repo, owner, []MemoryRef{badVersion}, now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("version err=%v", err)
	}
	if _, err := repo.TransitionMemory(context.Background(), value.Owner, value.ID, value.RowVersion, memory.StatusRevoked, "user", "revoked", "revoke", value.ContentHash, now); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePinnedMemoryRefs(context.Background(), repo, owner, []MemoryRef{ref}, now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("revoked err=%v", err)
	}
	for _, status := range []memory.Status{memory.StatusExpired, memory.StatusSuperseded} {
		statusRepo := memory.NewFakeRepository()
		statusValue := reminderMemoryRecord(t, owner, "memory-"+string(status), now, "独立状态测试")
		if _, err := statusRepo.CommitMutation(context.Background(), memory.Mutation{Owner: statusValue.Owner, NewMemory: &statusValue, IdempotencyKey: "seed-" + string(status), InputHash: statusValue.ContentHash, OccurredAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := statusRepo.TransitionMemory(context.Background(), statusValue.Owner, statusValue.ID, statusValue.RowVersion, status, "system", "status_test", "transition-"+string(status), statusValue.ContentHash, now); err != nil {
			t.Fatal(err)
		}
		statusRef := MemoryRef{ID: statusValue.ID, LineageVersion: statusValue.LineageVersion, ContentHash: statusValue.ContentHash}
		if err := ValidatePinnedMemoryRefs(context.Background(), statusRepo, owner, []MemoryRef{statusRef}, now); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("%s err=%v", status, err)
		}
	}
}

func TestMemorySummaryIsBoundedUntrustedAndRedactsCredentials(t *testing.T) {
	value := memory.Record{ID: "memory-1", LineageVersion: 1, ContentHash: strings.Repeat("a", 64), CanonicalText: strings.Repeat("内容", 200)}
	summary := BuildMemorySummary(value, 24)
	if !strings.Contains(summary.UntrustedText, "UNTRUSTED_MEMORY_SUMMARY") || len([]rune(summary.UntrustedText)) > 90 || !strings.Contains(summary.UntrustedText, "…") {
		t.Fatalf("summary=%q", summary.UntrustedText)
	}
	value.CanonicalText = "Authorization: Bearer super-secret-token"
	summary = BuildMemorySummary(value, 100)
	if strings.Contains(summary.UntrustedText, "super-secret") || !strings.Contains(summary.UntrustedText, "redacted") {
		t.Fatalf("credential leaked: %q", summary.UntrustedText)
	}
}

func reminderMemoryRecord(t *testing.T, owner Owner, id string, now time.Time, text string) memory.Record {
	t.Helper()
	normalized, structured, hash, err := memory.NormalizeContent(text, memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"value": text}})
	if err != nil {
		t.Fatal(err)
	}
	return memory.Record{ID: id, Owner: memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID}, Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "preferences", SlotKey: "weekly_report_format", LineageID: id + "-lineage", LineageVersion: 1, RowVersion: 1, CanonicalText: normalized, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserConfirmed, Confidence: 1, Salience: .8, Source: memory.SourceRef{Type: "workflow", ID: "association"}, Status: memory.StatusActive, CreatedAt: now, UpdatedAt: now}
}
