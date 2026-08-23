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
}

type Metrics struct {
	mu                                                        sync.RWMutex
	recallCalls, recallScanned, recallValid, obsoleteFiltered uint64
	conflicts                                                 map[PolicyAction]uint64
	hitl, idempotencyReplays, stateConflicts                  uint64
	projectionIndexed, projectionFailed                       uint64
	projectionLatency                                         time.Duration
	generation                                                string
}

type MetricsSnapshot struct {
	RecallCalls, RecallScanned, RecallValid, ObsoleteFiltered uint64
	Conflicts                                                 map[PolicyAction]uint64
	HITL, IdempotencyReplays, StateConflicts                  uint64
	ProjectionIndexed, ProjectionFailed                       uint64
	ProjectionLatencyMS                                       int64
	Generation                                                string
}

func NewMetrics() *Metrics { return &Metrics{conflicts: map[PolicyAction]uint64{}} }
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
	return MetricsSnapshot{RecallCalls: m.recallCalls, RecallScanned: m.recallScanned, RecallValid: m.recallValid, ObsoleteFiltered: m.obsoleteFiltered, Conflicts: conflicts, HITL: m.hitl, IdempotencyReplays: m.idempotencyReplays, StateConflicts: m.stateConflicts, ProjectionIndexed: m.projectionIndexed, ProjectionFailed: m.projectionFailed, ProjectionLatencyMS: m.projectionLatency.Milliseconds(), Generation: m.generation}
}

// SafeLogFields intentionally admits identifiers and bounded reason codes only.
func SafeLogFields(event, memoryID, workflowID, reasonCode string) map[string]any {
	return map[string]any{"event": boundedReason(event), "memory_id": boundedReason(memoryID), "workflow_id": boundedReason(workflowID), "reason_code": boundedReason(reasonCode)}
}
