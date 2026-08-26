package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type assemblyRunner struct{}

func (assemblyRunner) StreamConversation(context.Context, agent.ConversationRequest) <-chan agent.Event {
	events := make(chan agent.Event)
	close(events)
	return events
}

type assemblyEditStore struct{}

func (assemblyEditStore) PutMemoryEditPayload(context.Context, memory.Owner, string, memoryworkflow.Draft, time.Time, time.Time) error {
	return nil
}
func (assemblyEditStore) ConsumeMemoryEditPayload(context.Context, memory.Owner, string, time.Time) (memoryworkflow.Draft, error) {
	return memoryworkflow.Draft{}, memory.ErrNotFound
}

func memoryRuntimeTestConfig() config.MemoryConfig {
	return config.MemoryConfig{
		Enabled: true, RecallMode: "exact-only", StructuredPlanMinConfidence: .75,
		MaxRecallSelectors: 8, MaxExactCandidates: 40, MaxCandidateTextChars: 16000,
		MaxLLMResponseBytes: 16384, MaxLLMRepairAttempts: 1, DefaultSessionTTL: 24 * time.Hour,
		RecallTarget: 10, RecallPageSize: 20, MaxScanned: 200, MaxBatches: 10,
		MaxContextChars: 12000, ConflictThreshold: .8, ProjectionBatchSize: 50,
		ProjectionBaseBackoff: time.Second, ProjectionMaxBackoff: 5 * time.Minute,
		ProjectionMaxAttempts: 8, RAGTimeout: 10 * time.Second, ProjectionVersion: "v1",
	}
}

func TestAssembleMemoryRuntimeFeatureMatrix(t *testing.T) {
	off, err := assembleMemoryRuntime(config.MemoryConfig{}, memoryRuntimeDependencies{})
	if err != nil || off.Recall != nil || off.Capture != nil || off.Projection != nil || off.Metrics == nil || off.Metrics.Snapshot().FeatureDisabled["memory"] != 1 {
		t.Fatalf("disabled assembly=%+v err=%v", off, err)
	}

	repository := memory.NewFakeRepositoryWithProjectionVersion("v1")
	deps := memoryRuntimeDependencies{Repository: repository, ProjectionBacklog: repository}
	exactOnly, err := assembleMemoryRuntime(memoryRuntimeTestConfig(), deps)
	if err != nil || exactOnly.Recall == nil || exactOnly.Capture != nil || exactOnly.Adapter != nil || exactOnly.Projection == nil || exactOnly.Projection.Enabled() {
		t.Fatalf("exact-only assembly=%+v err=%v", exactOnly, err)
	}
	if _, err := exactOnly.Projection.RunBatch(context.Background(), time.Now()); err != nil {
		t.Fatalf("disabled projection RunBatch=%v", err)
	}

	pilotCfg := memoryRuntimeTestConfig()
	pilotCfg.WorkflowPilotEnabled = true
	deps.WorkflowStore = workflow.NewMemoryDurableStore()
	deps.EditPayloadStore = assemblyEditStore{}
	deps.Runner = assemblyRunner{}
	pilot, err := assembleMemoryRuntime(pilotCfg, deps)
	if err != nil || pilot.Recall == nil || pilot.Adapter == nil || pilot.Capture == nil || pilot.Projection.Enabled() {
		t.Fatalf("pilot assembly=%+v err=%v", pilot, err)
	}
}

func TestAssembleMemoryRuntimeReturnsSanitizedDependencyErrors(t *testing.T) {
	cfg := memoryRuntimeTestConfig()
	cfg.WorkflowPilotEnabled = true
	repository := memory.NewFakeRepositoryWithProjectionVersion("v1")
	_, err := assembleMemoryRuntime(cfg, memoryRuntimeDependencies{Repository: repository, ProjectionBacklog: repository, WorkflowStore: workflow.NewMemoryDurableStore(), EditPayloadStore: assemblyEditStore{}})
	if err == nil || !strings.Contains(err.Error(), "structured model") || strings.Contains(err.Error(), "api_key") {
		t.Fatalf("model error=%v", err)
	}
	_, err = assembleMemoryRuntime(cfg, memoryRuntimeDependencies{})
	if err == nil || err.Error() != "memory runtime unavailable: database" {
		t.Fatalf("database error=%v", err)
	}
}
