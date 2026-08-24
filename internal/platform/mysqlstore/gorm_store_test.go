package mysqlstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestGORMPersistenceModelsCoverManagedTables(t *testing.T) {
	models := []interface{ TableName() string }{
		chatSessionRow{}, agentRunRow{}, chatMessageRow{}, agentRunEventRow{}, authSessionRow{}, noteRow{}, noteOutboxRow{}, knowledgeBaseRow{}, noteDraftRow{}, workflowRunRow{}, workflowWaitRow{}, workflowNodeEventRow{}, memoryRecordRow{}, memoryEventRow{}, memoryRelationRow{}, memoryProjectionRow{}, memoryEditPayloadRow{},
	}
	want := []string{"chat_sessions", "agent_runs", "chat_messages", "agent_run_events", "agent_user_sessions", "notes", "note_outbox_events", "agent_user_knowledge_bases", "note_drafts", "workflow_runs", "workflow_waits", "workflow_node_events", "memory_records", "memory_events", "memory_relations", "memory_projection_outbox", "memory_edit_payloads"}
	if len(models) != len(want) {
		t.Fatalf("models=%d tables=%d", len(models), len(want))
	}
	for i := range models {
		if models[i].TableName() != want[i] {
			t.Fatalf("model %d table=%q want=%q", i, models[i].TableName(), want[i])
		}
	}
}

func TestMemoryGORMRowRoundTripPreservesJSONNullUTCAndZeroValues(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 123000, time.UTC)
	value := memory.Record{ID: "memory-id", Owner: memory.Owner{TenantID: 1, UserID: 2}, Layer: memory.LayerLongTerm, Kind: memory.KindPreference, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: "profile", LineageID: "lineage-id", LineageVersion: 1, RowVersion: 1, CanonicalText: "tea", StructuredValue: memory.StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"score": float64(0)}}, ContentHash: strings.Repeat("a", 64), Authority: memory.AuthorityUserStated, Confidence: 0, Salience: 0, Source: memory.SourceRef{Type: "test", ID: "row"}, Status: memory.StatusCandidate, CreatedAt: now, UpdatedAt: now}
	row, err := memoryToRow(value, now)
	if err != nil {
		t.Fatal(err)
	}
	if row.SupersedesID != nil || row.SupersededBy != nil || row.ExpiresAt != nil {
		t.Fatalf("nullable columns=%+v", row)
	}
	got, err := memoryFromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 0 || got.Salience != 0 || got.SupersedesID != "" || got.ExpiresAt != nil || !got.CreatedAt.Equal(now) {
		t.Fatalf("round trip=%+v", got)
	}
	if got.StructuredValue.Data["score"] != float64(0) {
		t.Fatalf("structured=%+v", got.StructuredValue)
	}
}

func TestGORMErrorMappingPreservesDomainContracts(t *testing.T) {
	for _, err := range []error{gorm.ErrDuplicatedKey, &mysqlDriver.MySQLError{Number: 1062, Message: "duplicate"}} {
		if !errors.Is(mapMemoryWriteError(err), memory.ErrStateConflict) {
			t.Fatalf("duplicate %T was not mapped", err)
		}
		if !duplicateKey(err) {
			t.Fatalf("duplicateKey rejected %T", err)
		}
	}
	sentinel := errors.New("database unavailable")
	if !errors.Is(mapMemoryWriteError(sentinel), sentinel) {
		t.Fatal("generic error was replaced")
	}
}

func TestStoreRejectsEmptyDSN(t *testing.T) {
	if _, err := Open(context.Background(), Options{}); err == nil {
		t.Fatal("expected empty DSN error")
	}
}

func TestStoreReportsConnectionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store, err := Open(ctx, Options{DSN: "root:invalid@tcp(127.0.0.1:1)/missing?timeout=100ms"})
	if err == nil || store != nil || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("expected wrapped mysql connection failure, store=%v err=%v", store, err)
	}
}

func TestRuntimeMySQLStoreUsesOnlyGORMDatabaseOperations(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{"QueryContext(", "QueryRowContext(", "ExecContext(", "BeginTx(", ".Save(", "AutoMigrate("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains forbidden database operation %s", entry.Name(), forbidden)
			}
		}
	}
}

func TestMySQLGORMConnectionPoolAndConcurrentMigration(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to verify GORM connection pool and concurrent migration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := Open(ctx, Options{DSN: dsn, MaxOpenConns: 7, MaxIdleConns: 3, ConnMaxLifetime: time.Minute, ProjectionVersion: "gorm-test-v1"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := store.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB.Stats().MaxOpenConnections != 7 {
		t.Fatalf("max open=%d", sqlDB.Stats().MaxOpenConnections)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- store.Migrate(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(ctx); err == nil {
		t.Fatal("expected closed connection pool to reject ping")
	}
}
