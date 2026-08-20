package workflow

import (
	"context"
	"testing"
)

type testStep struct{ name string }

func (s testStep) Name() string { return s.name }
func (s testStep) Run(_ context.Context, input map[string]any) error {
	input[s.name] = true
	return nil
}

func TestRunnerBoundsSteps(t *testing.T) {
	input := map[string]any{}
	runner := Runner{Steps: []Step{testStep{"a"}, testStep{"b"}}, MaxSteps: 2}
	if err := runner.Run(context.Background(), input); err != nil || input["a"] != true || input["b"] != true {
		t.Fatalf("Run() = %v, input=%#v", err, input)
	}
}
