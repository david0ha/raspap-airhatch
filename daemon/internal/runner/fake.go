package runner

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// Call records an argv invocation without interpreting it.
type Call struct {
	Name  string
	Args  []string
	Input []byte
}

// Step is one expected command and its scripted result.
type Step struct {
	Input  []byte
	Name   string
	Args   []string
	Output []byte
	Err    error
}

// Fake is a concurrency-safe command script for adapter tests.
// Unexpected calls return errors and leave the next expected step intact.
type Fake struct {
	mu    sync.Mutex
	steps []Step
	calls []Call
}

// NewFake copies a command script so callers cannot mutate it during execution.
func NewFake(steps ...Step) *Fake {
	f := &Fake{steps: make([]Step, len(steps))}
	for i, step := range steps {
		step.Input = slices.Clone(step.Input)
		step.Args = slices.Clone(step.Args)
		step.Output = slices.Clone(step.Output)
		f.steps[i] = step
	}
	return f
}

// Run records an invocation and consumes its matching scripted result.
func (f *Fake) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunInput(ctx, nil, name, args...)
}

// RunInput checks argv and stdin against the next scripted command.
func (f *Fake) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: name, Args: slices.Clone(args), Input: slices.Clone(input)})
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("run %s: %w", name, err)
	}
	if len(f.steps) == 0 {
		return nil, fmt.Errorf("unexpected command %s: script exhausted", name)
	}
	step := f.steps[0]
	if step.Name != name || !slices.Equal(step.Args, args) || !slices.Equal(step.Input, input) {
		return nil, fmt.Errorf("unexpected command %s: argv does not match next scripted command %s", name, step.Name)
	}
	f.steps = f.steps[1:]
	return slices.Clone(step.Output), step.Err
}

// Calls returns a detached copy of all attempted commands.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]Call, len(f.calls))
	for i, call := range f.calls {
		calls[i] = Call{Name: call.Name, Args: slices.Clone(call.Args), Input: slices.Clone(call.Input)}
	}
	return calls
}

// Remaining returns the number of unconsumed expectations.
func (f *Fake) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steps)
}
