package memory

import (
	"sync"
	"time"
)

type Telemetry interface {
	ObserveRecall(RecallResult)
	ObserveConflict(PolicyDecision)
	ObserveHITL()
	ObserveIdempotencyReplay()
	ObserveStateConflict()
	ObserveProjection(ProjectionBatchResult, time.Duration)
	ObserveGeneration(string)
	ObserveFeatureDisabled(string)
	ObserveCapture(string, string)
}

type Metrics struct {
	mu                                                           sync.RWMutex
	recallCalls, recallScanned, recallValid, obsoleteFiltered    uint64
	conflicts                                                    map[PolicyAction]uint64
	hitl, idempotencyReplays, stateConflicts                     uint64
	projectionIndexed, projectionFailed                          uint64
	projectionLatency                                            time.Duration
	generation                                                   string
	recallModes                                                  map[RecallMode]uint64
	selectorHits                                                 map[MatchSource]uint64
	featureDisabled, captureLifecycle, captureErrors             map[string]uint64
	noSelector, clarifications, recallTruncated, unknownFiltered uint64
}

type MetricsSnapshot struct {
	RecallCalls, RecallScanned, RecallValid, ObsoleteFiltered    uint64
	Conflicts                                                    map[PolicyAction]uint64
	HITL, IdempotencyReplays, StateConflicts                     uint64
	ProjectionIndexed, ProjectionFailed                          uint64
	ProjectionLatencyMS                                          int64
	Generation                                                   string
	RecallModes                                                  map[RecallMode]uint64
	SelectorHits                                                 map[MatchSource]uint64
	FeatureDisabled, CaptureLifecycle, CaptureErrors             map[string]uint64
	NoSelector, Clarifications, RecallTruncated, UnknownFiltered uint64
}

func NewMetrics() *Metrics {
	return &Metrics{conflicts: map[PolicyAction]uint64{}, recallModes: map[RecallMode]uint64{}, selectorHits: map[MatchSource]uint64{}, featureDisabled: map[string]uint64{}, captureLifecycle: map[string]uint64{}, captureErrors: map[string]uint64{}}
}
func (m *Metrics) ObserveRecall(r RecallResult) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallCalls++
	m.recallScanned += uint64(r.Scanned)
	m.recallValid += uint64(len(r.Items))
	m.obsoleteFiltered += uint64(r.ObsoleteFiltered)
	m.unknownFiltered += uint64(r.UnknownFiltered)
	m.recallModes[r.Mode]++
	if !r.HadSelector {
		m.noSelector++
	}
	if r.Clarification != nil {
		m.clarifications++
	}
	if r.Truncated {
		m.recallTruncated++
	}
	for _, item := range r.Items {
		m.selectorHits[item.MatchSource]++
	}
}
func (m *Metrics) ObserveConflict(d PolicyDecision) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conflicts[d.Action]++
}
func (m *Metrics) ObserveHITL() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.hitl++
	m.mu.Unlock()
}
func (m *Metrics) ObserveIdempotencyReplay() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.idempotencyReplays++
	m.mu.Unlock()
}
func (m *Metrics) ObserveStateConflict() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stateConflicts++
	m.mu.Unlock()
}
func (m *Metrics) ObserveProjection(r ProjectionBatchResult, latency time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectionIndexed += uint64(r.Indexed)
	m.projectionFailed += uint64(r.Failed + r.PermanentFailed)
	m.projectionLatency += latency
}
func (m *Metrics) ObserveGeneration(g string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.generation = boundedReason(g)
	m.mu.Unlock()
}
func (m *Metrics) ObserveFeatureDisabled(feature string) {
	if m == nil {
		return
	}
	feature = safeFeature(feature)
	m.mu.Lock()
	m.featureDisabled[feature]++
	m.mu.Unlock()
}
func (m *Metrics) ObserveCapture(event, errorCode string) {
	if m == nil {
		return
	}
	event = safeCaptureEvent(event)
	errorCode = safeCaptureError(errorCode)
	m.mu.Lock()
	m.captureLifecycle[event]++
	if errorCode != "" {
		m.captureErrors[errorCode]++
	}
	m.mu.Unlock()
}
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	conflicts := map[PolicyAction]uint64{}
	for k, v := range m.conflicts {
		conflicts[k] = v
	}
	return MetricsSnapshot{RecallCalls: m.recallCalls, RecallScanned: m.recallScanned, RecallValid: m.recallValid, ObsoleteFiltered: m.obsoleteFiltered, UnknownFiltered: m.unknownFiltered, Conflicts: conflicts, HITL: m.hitl, IdempotencyReplays: m.idempotencyReplays, StateConflicts: m.stateConflicts, ProjectionIndexed: m.projectionIndexed, ProjectionFailed: m.projectionFailed, ProjectionLatencyMS: m.projectionLatency.Milliseconds(), Generation: m.generation, RecallModes: cloneMetricMap(m.recallModes), SelectorHits: cloneMetricMap(m.selectorHits), FeatureDisabled: cloneMetricMap(m.featureDisabled), CaptureLifecycle: cloneMetricMap(m.captureLifecycle), CaptureErrors: cloneMetricMap(m.captureErrors), NoSelector: m.noSelector, Clarifications: m.clarifications, RecallTruncated: m.recallTruncated}
}

func cloneMetricMap[K comparable](source map[K]uint64) map[K]uint64 {
	result := make(map[K]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func safeFeature(value string) string {
	switch value {
	case "memory", "semantic_recall", "projection", "workflow_pilot":
		return value
	default:
		return "other"
	}
}
func safeCaptureEvent(value string) string {
	switch value {
	case "started", "suspended", "approved", "rejected", "edited", "completed", "failed":
		return value
	default:
		return "other"
	}
}
func safeCaptureError(value string) string {
	switch value {
	case "", "not_found", "invalid_request", "invalid_resume", "wait_expired", "state_conflict", "claim_conflict", "idempotency_conflict", "internal":
		return value
	default:
		return "internal"
	}
}

// SafeLogFields intentionally admits identifiers and bounded reason codes only.
func SafeLogFields(event, memoryID, workflowID, reasonCode string) map[string]any {
	return map[string]any{"event": boundedReason(event), "memory_id": boundedReason(memoryID), "workflow_id": boundedReason(workflowID), "reason_code": boundedReason(reasonCode)}
}
