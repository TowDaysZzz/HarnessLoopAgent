package memory

import (
	"strings"
	"testing"
	"time"
)

func TestMemoryMetricsContainCountsWithoutContent(t *testing.T) {
	m := NewMetrics()
	m.ObserveRecall(RecallResult{Items: []RecallItem{{Memory: Record{CanonicalText: "private body"}}}, Scanned: 20, ObsoleteFiltered: 7})
	m.ObserveConflict(PolicyDecision{Action: ActionReview})
	m.ObserveHITL()
	m.ObserveIdempotencyReplay()
	m.ObserveStateConflict()
	m.ObserveProjection(ProjectionBatchResult{Indexed: 2, Failed: 1}, 25*time.Millisecond)
	m.ObserveGeneration("gen-v2")
	snapshot := m.Snapshot()
	if snapshot.RecallCalls != 1 || snapshot.RecallScanned != 20 || snapshot.RecallValid != 1 || snapshot.ObsoleteFiltered != 7 || snapshot.Conflicts[ActionReview] != 1 || snapshot.ProjectionIndexed != 2 || snapshot.Generation != "gen-v2" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	fields := SafeLogFields("recall", "mem-1", "wf-1", "Bearer secret-value")
	if len(fields) != 4 || fields["reason_code"] != "redacted" {
		t.Fatalf("fields=%+v", fields)
	}
	for key, value := range fields {
		if strings.Contains(strings.ToLower(key), "content") || strings.Contains(strings.ToLower(value.(string)), "secret-value") {
			t.Fatalf("unsafe field %s=%v", key, value)
		}
	}
}
