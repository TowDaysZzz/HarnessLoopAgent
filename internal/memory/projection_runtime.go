package memory

import (
	"context"
	"time"
)

type ProjectionBacklog interface {
	PendingProjectionCount(context.Context) (int64, error)
}
type ProjectionBatchRunner interface {
	RunBatch(context.Context, time.Time) (ProjectionBatchResult, error)
}

type ProjectionRuntime struct {
	enabled bool
	runner  ProjectionBatchRunner
	backlog ProjectionBacklog
}

func NewProjectionRuntime(enabled bool, runner ProjectionBatchRunner, backlog ProjectionBacklog) (*ProjectionRuntime, error) {
	if backlog == nil || (enabled && runner == nil) {
		return nil, ErrInvalidInput
	}
	return &ProjectionRuntime{enabled: enabled, runner: runner, backlog: backlog}, nil
}

func (r *ProjectionRuntime) Enabled() bool { return r != nil && r.enabled }
func (r *ProjectionRuntime) Ready() error {
	if r == nil {
		return ErrInvalidInput
	}
	if r.enabled && r.runner == nil {
		return ErrInvalidInput
	}
	return nil
}
func (r *ProjectionRuntime) Backlog(ctx context.Context) (int64, error) {
	if r == nil || r.backlog == nil {
		return 0, ErrInvalidInput
	}
	return r.backlog.PendingProjectionCount(ctx)
}
func (r *ProjectionRuntime) RunBatch(ctx context.Context, now time.Time) (ProjectionBatchResult, error) {
	if r == nil {
		return ProjectionBatchResult{}, ErrInvalidInput
	}
	if !r.enabled {
		return ProjectionBatchResult{}, nil
	}
	return r.runner.RunBatch(ctx, now)
}
