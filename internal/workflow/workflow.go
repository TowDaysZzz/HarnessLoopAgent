package workflow

import (
	"context"
	"errors"
	"fmt"
)

type Step interface {
	Name() string
	Run(context.Context, map[string]any) error
}

type Runner struct {
	Steps          []Step
	MaxSteps       int
	EnableParallel bool
}

func (r Runner) Run(ctx context.Context, input map[string]any) error {
	if len(r.Steps) == 0 {
		return errors.New("workflow requires at least one step")
	}
	max := r.MaxSteps
	if max < 1 {
		max = len(r.Steps)
	}
	if len(r.Steps) > max {
		return fmt.Errorf("workflow step budget exceeded: %d > %d", len(r.Steps), max)
	}
	for _, step := range r.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if step == nil {
			return errors.New("workflow contains nil step")
		}
		if err := step.Run(ctx, input); err != nil {
			return fmt.Errorf("workflow step %s: %w", step.Name(), err)
		}
	}
	return nil
}
