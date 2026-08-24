package mysqlstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/dailyreview"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/google/uuid"
)

func TestDailyReviewCacheMySQLSingleFlightAndTokenFencing(t *testing.T) {
	store, ctx := openGORMRepositoryStore(t)
	suffix := uint64(time.Now().UnixNano()%500000000) + 4500000000
	owner := skill.Owner{TenantID: suffix, UserID: suffix + 1}
	logical, source := dailyreview.ContentHash(uuid.NewString()), dailyreview.ContentHash(uuid.NewString())
	now := time.Now().UTC().Truncate(time.Microsecond)
	const callers = 12
	var wg sync.WaitGroup
	wg.Add(callers)
	var mu sync.Mutex
	generators := 0
	var winner dailyreview.CacheRecord
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			claim, err := store.Claim(context.Background(), owner, logical, source, now, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			if claim.Generator {
				mu.Lock()
				generators++
				winner = claim.Record
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if generators != 1 {
		t.Fatalf("generators=%d", generators)
	}
	result := dailyreview.CachedResult{Structured: []byte(`{"version":"v1"}`), Rendered: "cached review", EvidenceHash: dailyreview.ContentHash("e"), ContentHash: dailyreview.ContentHash("cached review")}
	if _, err := store.CommitReady(ctx, owner, winner.ID, "wrong-token", result, now.Add(time.Hour), now); err != dailyreview.ErrClaimLost {
		t.Fatalf("wrong token err=%v", err)
	}
	ready, err := store.CommitReady(ctx, owner, winner.ID, winner.ClaimToken, result, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Lookup(ctx, owner, logical, source, now); err != nil || got.Result.ContentHash != ready.Result.ContentHash {
		t.Fatalf("lookup=%#v err=%v", got, err)
	}
	if _, err := store.Lookup(ctx, skill.Owner{TenantID: owner.TenantID, UserID: owner.UserID + 1}, logical, source, now); err != dailyreview.ErrCacheNotFound {
		t.Fatalf("cross owner err=%v", err)
	}
}
