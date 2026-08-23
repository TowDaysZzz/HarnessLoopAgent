package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryMetricsContainCountsWithoutContent(t *testing.T) {
	m := NewMetrics()
	m.ObserveRecall(RecallResult{Mode: RecallModeExactOnly, HadSelector: true, Items: []RecallItem{{Memory: Record{CanonicalText: "private body"}, MatchSource: MatchSlot}}, Scanned: 20, ObsoleteFiltered: 7, UnknownFiltered: 2, Truncated: true})
	m.ObserveRecall(RecallResult{Mode: RecallModeExactOnly, Clarification: &RecallClarification{Needed: true, Reason: "private-structured-value"}})
	m.ObserveConflict(PolicyDecision{Action: ActionReview})
	m.ObserveHITL()
	m.ObserveIdempotencyReplay()
	m.ObserveStateConflict()
	m.ObserveProjection(ProjectionBatchResult{Indexed: 2, Failed: 1}, 25*time.Millisecond)
	m.ObserveGeneration("gen-v2")
	m.ObserveFeatureDisabled("projection")
	m.ObserveFeatureDisabled("Bearer secret-feature")
	m.ObserveCapture("started", "")
	m.ObserveCapture("failed", "Bearer secret-error")
	snapshot := m.Snapshot()
	if snapshot.RecallCalls != 2 || snapshot.RecallModes[RecallModeExactOnly] != 2 || snapshot.SelectorHits[MatchSlot] != 1 || snapshot.NoSelector != 1 || snapshot.Clarifications != 1 || snapshot.RecallTruncated != 1 || snapshot.UnknownFiltered != 2 || snapshot.RecallScanned != 20 || snapshot.RecallValid != 1 || snapshot.ObsoleteFiltered != 7 || snapshot.Conflicts[ActionReview] != 1 || snapshot.ProjectionIndexed != 2 || snapshot.Generation != "gen-v2" || snapshot.FeatureDisabled["projection"] != 1 || snapshot.FeatureDisabled["other"] != 1 || snapshot.CaptureLifecycle["started"] != 1 || snapshot.CaptureErrors["internal"] != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	encoded := strings.ToLower(fmt.Sprint(snapshot))
	if strings.Contains(encoded, "private body") || strings.Contains(encoded, "private-structured-value") || strings.Contains(encoded, "secret-feature") || strings.Contains(encoded, "secret-error") {
		t.Fatalf("metrics leaked sensitive values: %s", encoded)
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
