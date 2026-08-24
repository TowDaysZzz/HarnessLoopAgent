package dailyreview

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

func TestCacheIdentityIncludesAllFreshnessDimensions(t *testing.T) {
	w, _ := ResolveWindow("2026-08-24", "Asia/Shanghai")
	base := CacheIdentity{Owner: skill.Owner{TenantID: 1, UserID: 2}, Window: w, OptionsHash: ContentHash("o"), SkillID: "daily_review", SkillVersion: "v1", SchemaVersion: "v1", PromptPolicyVersion: "v1"}
	first, _ := base.LogicalKey()
	variants := []CacheIdentity{base, base, base, base, base, base}
	variants[0].Owner.UserID++
	variants[1].Window.LocalDate = "2026-08-23"
	variants[2].OptionsHash = ContentHash("x")
	variants[3].SkillVersion = "v2"
	variants[4].SchemaVersion = "v2"
	variants[5].PromptPolicyVersion = "v2"
	for _, v := range variants {
		got, _ := v.LogicalKey()
		if got == first {
			t.Fatalf("identity dimension omitted: %#v", v)
		}
	}
}

func TestMemoryCacheSingleFlightLeaseAndTokenFencing(t *testing.T) {
	repo := NewMemoryCache()
	owner := skill.Owner{TenantID: 1, UserID: 2}
	now := time.Now().UTC()
	const callers = 20
	var wg sync.WaitGroup
	wg.Add(callers)
	var mu sync.Mutex
	generators := 0
	var winning CacheRecord
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			claim, err := repo.Claim(context.Background(), owner, "logical", "source", now, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if claim.Generator {
				mu.Lock()
				generators++
				winning = claim.Record
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if generators != 1 {
		t.Fatalf("generators=%d", generators)
	}
	reclaimed, err := repo.Claim(context.Background(), owner, "logical", "source", now.Add(2*time.Minute), time.Minute)
	if err != nil || !reclaimed.Generator {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	result := CachedResult{Structured: []byte(`{"version":"v1"}`), Rendered: "回顾", EvidenceHash: ContentHash("e"), ContentHash: ContentHash("回顾")}
	if _, err := repo.CommitReady(context.Background(), owner, winning.ID, winning.ClaimToken, result, now.Add(time.Hour), now.Add(2*time.Minute)); err != ErrClaimLost {
		t.Fatalf("old token err=%v", err)
	}
	if _, err := repo.CommitReady(context.Background(), owner, reclaimed.Record.ID, reclaimed.Record.ClaimToken, result, now.Add(time.Hour), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Lookup(context.Background(), skill.Owner{TenantID: 1, UserID: 3}, "logical", "source", now); err != ErrCacheNotFound {
		t.Fatalf("cross owner err=%v", err)
	}
}

func TestValidUntilUsesEarliestBoundary(t *testing.T) {
	now := time.Now().UTC()
	memory := now.Add(5 * time.Minute)
	policy := now.Add(10 * time.Minute)
	if got := ComputeValidUntil(now, time.Hour, &memory, &policy); !got.Equal(memory) {
		t.Fatalf("valid until=%v", got)
	}
}

func TestCacheCleanupRemovesOnlyExpiredTerminalRecords(t *testing.T) {
	repo := NewMemoryCache()
	owner := skill.Owner{TenantID: 1, UserID: 2}
	now := time.Now().UTC()
	makeReady := func(source string, valid time.Time) {
		claim, _ := repo.Claim(context.Background(), owner, "logical", source, now.Add(-time.Hour), time.Minute)
		result := CachedResult{Structured: []byte(`{"v":1}`), Rendered: source, EvidenceHash: ContentHash("e"), ContentHash: ContentHash(source)}
		if _, err := repo.CommitReady(context.Background(), owner, claim.Record.ID, claim.Record.ClaimToken, result, valid, now.Add(-time.Hour+time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	makeReady("expired", now.Add(-time.Minute))
	makeReady("fresh", now.Add(time.Hour))
	_, _ = repo.Claim(context.Background(), owner, "logical", "generating", now, time.Hour)
	count, err := repo.CleanupExpired(context.Background(), now, 10)
	if err != nil || count != 1 {
		t.Fatalf("cleanup=%d err=%v", count, err)
	}
	if _, err := repo.Lookup(context.Background(), owner, "logical", "fresh", now); err != nil {
		t.Fatal(err)
	}
}

func TestCacheKeysAndObservationEnvelopeContainNoSourceBodiesOrCredentials(t *testing.T) {
	window, _ := ResolveWindow("2026-08-24", "Asia/Shanghai")
	identity := CacheIdentity{Owner: skill.Owner{TenantID: 1, UserID: 2}, Window: window, OptionsHash: ContentHash("secret chat body"), SkillID: "daily_review", SkillVersion: "v1", SchemaVersion: "v1", PromptPolicyVersion: "v1"}
	key, err := identity.LogicalKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret chat body", "token", "password", "cookie"} {
		if strings.Contains(strings.ToLower(key), forbidden) {
			t.Fatalf("cache key leaked %q: %s", forbidden, key)
		}
	}
}
