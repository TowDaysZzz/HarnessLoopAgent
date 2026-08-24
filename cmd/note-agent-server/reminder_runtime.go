package main

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderdelivery"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderllm"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

const reminderCommandDefinition workflow.DefinitionVersion = "reminder-command-v1"

type reminderRuntimeDependencies struct {
	Repository       reminder.Repository
	MemoryRepository memory.Repository
	WorkflowStore    workflow.DurableStore
	EditPayloadStore reminderworkflow.EditPayloadStore
	Runner           agent.ConversationRunner
	DeliveryAdapter  reminderdelivery.Adapter
}

type reminderRuntimeAssembly struct {
	Command    *reminderworkflow.Service
	Query      *reminderworkflow.QueryService
	Dispatcher *reminderdelivery.Dispatcher
	Worker     *reminderdelivery.Worker
}

func assembleReminderRuntime(cfg config.ReminderConfig, deps reminderRuntimeDependencies) (*reminderRuntimeAssembly, error) {
	assembly := &reminderRuntimeAssembly{}
	if !cfg.Enabled {
		return assembly, nil
	}
	if deps.Repository == nil {
		return nil, reminderStartupError("database")
	}
	if cfg.WorkflowPilotEnabled {
		if deps.Runner == nil || deps.MemoryRepository == nil || deps.WorkflowStore == nil || deps.EditPayloadStore == nil {
			return nil, reminderStartupError("workflow dependencies")
		}
		recall, err := memory.NewRecallService(deps.MemoryRepository, nil, memory.RecallConfig{Mode: memory.RecallModeExactOnly, DefaultTarget: 1, MaxTarget: reminder.MaxMemoryRefs, PageSize: 20, MaxScanned: 20, MaxBatches: 1, MaxDuration: 2 * time.Second, MaxContextChars: 4096, PlanMinConfidence: .75, MaxExactCandidates: 20})
		if err != nil {
			return nil, reminderStartupError("memory recall")
		}
		adapter, err := reminderllm.New(deps.Runner, reminderllm.Config{MaxResponseBytes: reminder.MaxCommandPlanBytes, MaxRepairAttempts: 1, MinConfidence: .75, MaxHorizon: cfg.MaxHorizon})
		if err != nil {
			return nil, reminderStartupError("structured model")
		}
		edits := &reminderworkflow.EditPayloadService{Store: deps.EditPayloadStore, TTL: 24 * time.Hour}
		evaluator := &reminderworkflow.Evaluator{Planner: adapter, Recall: recall, Repository: deps.Repository, MaxHorizon: cfg.MaxHorizon}
		nodes := reminderworkflow.NewNodes(evaluator, reminderworkflow.ReviewNode{TTL: 24 * time.Hour, EditLoader: edits}, reminderworkflow.CommitNode{Repository: deps.Repository, MemoryRepository: deps.MemoryRepository})
		durable, err := workflow.NewDurableRuntime(deps.WorkflowStore, nodes, reminderCommandDefinition, reminderworkflow.CommandCodec{MaxBytes: 32 * 1024}, workflow.DurableRuntimeOptions{LeaseDuration: cfg.LeaseDuration, MaxCheckpointBytes: 64 * 1024})
		if err != nil {
			return nil, reminderStartupError("durable workflow")
		}
		command, err := reminderworkflow.NewService(durable, edits, reminderworkflow.ServiceConfig{DefinitionVersion: reminderCommandDefinition, MaxSteps: 40, MaxResumes: 8, RunTTL: 24 * time.Hour})
		if err != nil {
			return nil, reminderStartupError("command service")
		}
		query := &reminderworkflow.QueryService{Planner: adapter, Repository: deps.Repository}
		assembly.Command, assembly.Query = command, query
	}
	if cfg.DispatcherEnabled {
		dispatcher, err := reminderdelivery.NewDispatcher(deps.Repository, reminderdelivery.DispatcherConfig{BatchSize: cfg.BatchSize, MaxBatches: cfg.MaxBatches, LeaseDuration: cfg.LeaseDuration, Interval: cfg.Interval, NewToken: uuid.NewString})
		if err != nil {
			return nil, reminderStartupError("dispatcher")
		}
		assembly.Dispatcher = dispatcher
	}
	if cfg.WorkerEnabled {
		if deps.DeliveryAdapter == nil {
			return nil, reminderStartupError("production delivery adapter")
		}
		worker, err := reminderdelivery.NewWorker(deps.Repository, deps.DeliveryAdapter, reminderdelivery.WorkerConfig{BatchSize: cfg.BatchSize, MaxBatches: cfg.MaxBatches, MaxAttempts: cfg.MaxAttempts, LeaseDuration: cfg.LeaseDuration, Interval: cfg.Interval, BaseBackoff: cfg.RetryBaseBackoff, MaxBackoff: cfg.RetryMaxBackoff, Production: true, NewToken: uuid.NewString})
		if err != nil {
			return nil, reminderStartupError("worker")
		}
		assembly.Worker = worker
	}
	return assembly, nil
}

func (a *reminderRuntimeAssembly) Start(ctx context.Context) {
	if a == nil {
		return
	}
	if a.Dispatcher != nil {
		go func() { _ = a.Dispatcher.Run(ctx) }()
	}
	if a.Worker != nil {
		go func() { _ = a.Worker.Run(ctx) }()
	}
}

func reminderStartupError(component string) error {
	return errors.New("reminder runtime unavailable: " + component)
}
