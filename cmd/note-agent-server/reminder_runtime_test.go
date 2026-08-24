package main

import (
	"context"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminderworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

type assemblyReminderEditStore struct{}

func (assemblyReminderEditStore) PutReminderEditPayload(context.Context, reminder.Owner, string, string, time.Time, time.Time) error {
	return nil
}
func (assemblyReminderEditStore) ConsumeReminderEditPayload(context.Context, reminder.Owner, string, time.Time) (string, error) {
	return "", reminder.ErrNotFound
}

var _ reminderworkflow.EditPayloadStore = assemblyReminderEditStore{}

func reminderRuntimeTestConfig() config.ReminderConfig {
	return config.ReminderConfig{Enabled: true, BatchSize: 10, MaxBatches: 2, MaxAttempts: 3, LeaseDuration: time.Second, Interval: time.Second, MaxHorizon: 365 * 24 * time.Hour, RetryBaseBackoff: time.Second, RetryMaxBackoff: time.Minute, Timezone: reminder.DefaultTimezone}
}

func TestAssembleReminderRuntimeFeatureMatrixDefaultsOff(t *testing.T) {
	off, err := assembleReminderRuntime(config.ReminderConfig{}, reminderRuntimeDependencies{})
	if err != nil || off.Command != nil || off.Query != nil || off.Dispatcher != nil || off.Worker != nil {
		t.Fatalf("off=%+v err=%v", off, err)
	}
	cfg := reminderRuntimeTestConfig()
	repo := reminder.NewFakeRepository()
	enabled, err := assembleReminderRuntime(cfg, reminderRuntimeDependencies{Repository: repo})
	if err != nil || enabled.Command != nil || enabled.Dispatcher != nil {
		t.Fatalf("enabled=%+v err=%v", enabled, err)
	}
	cfg.DispatcherEnabled = true
	dispatcher, err := assembleReminderRuntime(cfg, reminderRuntimeDependencies{Repository: repo})
	if err != nil || dispatcher.Dispatcher == nil || dispatcher.Worker != nil {
		t.Fatalf("dispatcher=%+v err=%v", dispatcher, err)
	}
}

func TestAssembleReminderWorkflowAndMissingProductionAdapter(t *testing.T) {
	cfg := reminderRuntimeTestConfig()
	cfg.WorkflowPilotEnabled = true
	reminderRepo := reminder.NewFakeRepository()
	memoryRepo := memory.NewFakeRepository()
	deps := reminderRuntimeDependencies{Repository: reminderRepo, MemoryRepository: memoryRepo, WorkflowStore: workflow.NewMemoryDurableStore(), EditPayloadStore: assemblyReminderEditStore{}, Runner: assemblyRunner{}}
	assembly, err := assembleReminderRuntime(cfg, deps)
	if err != nil || assembly.Command == nil || assembly.Query == nil {
		t.Fatalf("assembly=%+v err=%v", assembly, err)
	}
	cfg.DispatcherEnabled, cfg.WorkerEnabled = true, true
	cfg.ProductionDeliveryAdapter = "configured"
	if _, err := assembleReminderRuntime(cfg, deps); err == nil || err.Error() != "reminder runtime unavailable: production delivery adapter" {
		t.Fatalf("worker err=%v", err)
	}
}
