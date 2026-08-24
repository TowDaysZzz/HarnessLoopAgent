package mysqlstore

import (
	"strings"
	"testing"
)

func TestSkillInvocationMigrationDeclaresOwnerAndRunIndexes(t *testing.T) {
	body, err := migrations.ReadFile("migrations/0009_daily_review_skill.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"UNIQUE KEY uk_skill_invocation_run (chat_run_id)",
		"KEY idx_skill_invocations_owner_id (tenant_id, user_id, id)",
		"KEY idx_skill_invocations_owner_skill_created (tenant_id, user_id, skill_id, created_at)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestDailyReviewCacheMigrationDeclaresIdentityLeaseAndCleanupIndexes(t *testing.T) {
	body, err := migrations.ReadFile("migrations/0011_daily_review_cache.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"UNIQUE KEY uk_daily_review_identity (tenant_id,user_id,logical_key,source_fingerprint)", "KEY idx_daily_review_owner_id (tenant_id,user_id,id)", "KEY idx_daily_review_cleanup (status,valid_until,lease_until)"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}
