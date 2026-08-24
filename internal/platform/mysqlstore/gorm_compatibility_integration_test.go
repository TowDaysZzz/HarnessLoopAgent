package mysqlstore_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

func TestGORMSchemaIsBidirectionallyCompatibleWithLegacySQL(t *testing.T) {
	dsn := os.Getenv("MYSQL_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_INTEGRATION_DSN to verify legacy database/sql compatibility")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, mysqlstore.Options{DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := uint64(now.UnixNano()%500000000) + 5000000000
	gormSession := chat.Session{ID: uuid.NewString(), UserID: base, TenantID: base + 1, Title: "written-by-gorm", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSession(ctx, gormSession); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var title string
	if err := legacy.QueryRowContext(ctx, `SELECT title FROM chat_sessions WHERE id=? AND user_id=? AND tenant_id=?`, gormSession.ID, gormSession.UserID, gormSession.TenantID).Scan(&title); err != nil || title != gormSession.Title {
		t.Fatalf("legacy read title=%q err=%v", title, err)
	}
	legacySession := chat.Session{ID: uuid.NewString(), UserID: base + 2, TenantID: base + 3, Title: "written-by-legacy", Status: "active", CreatedAt: now, UpdatedAt: now}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO chat_sessions (id,user_id,tenant_id,title,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, legacySession.ID, legacySession.UserID, legacySession.TenantID, legacySession.Title, legacySession.Status, legacySession.CreatedAt, legacySession.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSession(ctx, chat.Owner{UserID: legacySession.UserID, TenantID: legacySession.TenantID}, legacySession.ID)
	if err != nil || got.Title != legacySession.Title {
		t.Fatalf("gorm read=%+v err=%v", got, err)
	}
}
