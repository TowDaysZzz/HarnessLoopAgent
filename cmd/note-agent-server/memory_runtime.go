package main

import (
	"errors"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryllm"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

const memoryCaptureDefinition workflow.DefinitionVersion = "memory-capture-v1"

type memoryRuntimeDependencies struct {
	Repository        memory.Repository
	WorkflowStore     workflow.DurableStore
	EditPayloadStore  memoryworkflow.EditPayloadStore
	ProjectionBacklog memory.ProjectionBacklog
	Runner            agent.ConversationRunner
	RAG               ragclient.MemoryClient
}

type memoryRuntimeAssembly struct {
	Recall     *memory.RecallService
	Adapter    *memoryllm.Adapter
	Capture    *memoryworkflow.CaptureService
	Projection *memory.ProjectionRuntime
	Metrics    *memory.Metrics
}

func assembleMemoryRuntime(cfg config.MemoryConfig, deps memoryRuntimeDependencies) (*memoryRuntimeAssembly, error) {
	metrics := memory.NewMetrics()
	if !cfg.Enabled {
		metrics.ObserveFeatureDisabled("memory")
		return &memoryRuntimeAssembly{Metrics: metrics}, nil
	}
	if deps.Repository == nil || deps.ProjectionBacklog == nil {
		return nil, memoryStartupError("database")
	}
	mode := memory.RecallMode(cfg.RecallMode)
	if mode == memory.RecallModeExactPlusSemantic && deps.RAG == nil {
		return nil, memoryStartupError("rag")
	}
	recall, err := memory.NewRecallService(deps.Repository, deps.RAG, memory.RecallConfig{
		Mode:               mode,
		DefaultTarget:      cfg.RecallTarget,
		MaxTarget:          cfg.RecallPageSize,
		PageSize:           cfg.RecallPageSize,
		MaxScanned:         cfg.MaxScanned,
		MaxBatches:         cfg.MaxBatches,
		MaxDuration:        cfg.RAGTimeout,
		MaxContextChars:    cfg.MaxContextChars,
		PlanMinConfidence:  cfg.StructuredPlanMinConfidence,
		MaxExactCandidates: cfg.MaxExactCandidates,
	})
	if err != nil {
		return nil, memoryStartupError("recall")
	}
	recall.SetTelemetry(metrics)
	assembly := &memoryRuntimeAssembly{Recall: recall, Metrics: metrics}

	if cfg.ProjectionEnabled {
		if deps.RAG == nil {
			return nil, memoryStartupError("rag")
		}
		projector, projectorErr := memory.NewProjector(deps.Repository, deps.RAG, memory.ProjectorConfig{
			BatchSize: cfg.ProjectionBatchSize, BaseBackoff: cfg.ProjectionBaseBackoff,
			MaxBackoff: cfg.ProjectionMaxBackoff, MaxAttempts: cfg.ProjectionMaxAttempts,
			ModelVersion: cfg.ProjectionVersion,
		})
		if projectorErr != nil {
			return nil, memoryStartupError("projection")
		}
		projector.SetTelemetry(metrics)
		assembly.Projection, err = memory.NewProjectionRuntime(true, projector, deps.ProjectionBacklog)
	} else {
		metrics.ObserveFeatureDisabled("projection")
		assembly.Projection, err = memory.NewProjectionRuntime(false, nil, deps.ProjectionBacklog)
	}
	if err != nil {
		return nil, memoryStartupError("projection")
	}
	if !cfg.WorkflowPilotEnabled {
		metrics.ObserveFeatureDisabled("workflow_pilot")
		return assembly, nil
	}
	if deps.Runner == nil {
		return nil, memoryStartupError("structured model")
	}
	if deps.WorkflowStore == nil || deps.EditPayloadStore == nil {
		return nil, memoryStartupError("database")
	}
	adapter, err := memoryllm.New(deps.Runner, memoryllm.Config{
		MaxResponseBytes: cfg.MaxLLMResponseBytes, MaxRepairAttempts: cfg.MaxLLMRepairAttempts,
		PlanMinConfidence: cfg.StructuredPlanMinConfidence, MaxCandidates: cfg.MaxExactCandidates,
	})
	if err != nil {
		return nil, memoryStartupError("structured model")
	}
	edits := &memoryworkflow.EditPayloadService{Store: deps.EditPayloadStore, Extractor: adapter, TTL: cfg.DefaultSessionTTL}
	nodes := memoryworkflow.Nodes{
		Extract:              memoryworkflow.ExtractNode{Extractor: adapter},
		ExactCandidateLookup: memoryworkflow.ExactCandidateLookupNode{Repository: deps.Repository, MaxCandidates: cfg.MaxExactCandidates},
		Conflict:             memoryworkflow.ConflictNode{Resolver: adapter, Repository: deps.Repository},
		Review:               memoryworkflow.ReviewNode{Repository: deps.Repository, Resolver: adapter, EditLoader: edits, TTL: cfg.DefaultSessionTTL, MaxCandidates: cfg.MaxExactCandidates},
		Commit:               memoryworkflow.CommitNode{Repository: deps.Repository},
	}
	durable, err := workflow.NewDurableRuntime(deps.WorkflowStore, nodes.List(), memoryCaptureDefinition, memoryworkflow.CaptureCodec{MaxBytes: 32 * 1024}, workflow.DurableRuntimeOptions{LeaseDuration: time.Minute, MaxCheckpointBytes: 64 * 1024})
	if err != nil {
		return nil, memoryStartupError("workflow")
	}
	maxDraftChars := cfg.MaxCandidateTextChars
	if maxDraftChars > 4096 {
		maxDraftChars = 4096
	}
	if maxDraftChars < 64 {
		return nil, memoryStartupError("workflow")
	}
	capture, err := memoryworkflow.NewCaptureService(durable, edits, memoryworkflow.CaptureServiceConfig{
		DefinitionVersion: memoryCaptureDefinition, MaxSteps: 16, MaxResumes: 5,
		MaxDraftChars: maxDraftChars, RunTTL: cfg.DefaultSessionTTL, Telemetry: metrics,
	})
	if err != nil {
		return nil, memoryStartupError("workflow")
	}
	assembly.Adapter = adapter
	assembly.Capture = capture
	return assembly, nil
}

func memoryStartupError(component string) error {
	return errors.New("memory runtime unavailable: " + component)
}
