package mysqlstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

func TestMemoryExactQueryPlansUseBoundedIndexes(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to verify Memory exact EXPLAIN plans")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, Options{DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ProjectionVersion: "query-plan-v1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	owner := memory.Owner{TenantID: 9701, UserID: 9702}
	now := time.Now().UTC().Truncate(time.Microsecond)
	slot := "query-plan-" + uuid.NewString()
	entityID := "task-" + uuid.NewString()
	text, structured, hash, err := memory.NormalizeContent("query plan target "+slot, memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"slot": slot}})
	if err != nil {
		t.Fatal(err)
	}
	record := memory.Record{ID: uuid.NewString(), Owner: owner, Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", SlotKey: slot, Entity: memory.EntityRef{Type: "task", ID: entityID}, LineageID: uuid.NewString(), LineageVersion: 1, RowVersion: 1, CanonicalText: text, StructuredValue: structured, ContentHash: hash, Authority: memory.AuthorityUserConfirmed, Confidence: 1, Salience: .5, Source: memory.SourceRef{Type: "workflow", ID: "explain"}, Status: memory.StatusActive, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CommitMutation(ctx, memory.Mutation{Owner: owner, NewMemory: &record, Actor: "system", ReasonCode: "explain_seed", IdempotencyKey: "explain-" + uuid.NewString(), InputHash: hash, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}

	assertExplainUsesIndex(t, store, ctx, "idx_memory_exact_slot_active", `SELECT id FROM memory_records WHERE tenant_id=? AND user_id=? AND scope_type=? AND scope_id=? AND namespace=? AND slot_key=? AND status='active' AND (expires_at IS NULL OR expires_at>?) LIMIT 10`, owner.TenantID, owner.UserID, memory.ScopeUser, "", "profile", slot, now)
	assertExplainUsesIndex(t, store, ctx, "idx_memory_exact_entity_active", `SELECT id FROM memory_records WHERE tenant_id=? AND user_id=? AND scope_type=? AND scope_id=? AND entity_type=? AND entity_id=? AND status='active' AND (expires_at IS NULL OR expires_at>?) LIMIT 10`, owner.TenantID, owner.UserID, memory.ScopeUser, "", "task", entityID, now)
}

func assertExplainUsesIndex(t *testing.T, store *Store, ctx context.Context, index, query string, args ...any) {
	t.Helper()
	query = strings.Replace(query, "FROM memory_records", "FROM memory_records FORCE INDEX ("+index+")", 1)
	var plan string
	if err := store.db.WithContext(ctx).Raw("EXPLAIN FORMAT=JSON "+query, args...).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	compact := strings.ReplaceAll(plan, " ", "")
	if !strings.Contains(compact, fmt.Sprintf(`"key":"%s"`, index)) {
		t.Fatalf("expected %s in EXPLAIN plan: %s", index, plan)
	}
}
