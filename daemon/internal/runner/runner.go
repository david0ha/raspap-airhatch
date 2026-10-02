// Package runner executes bounded, cancellable commands without a shell.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Runner is the command boundary shared by system adapters and their tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// InputRunner also accepts stdin without exposing secrets in argv.
type InputRunner interface {
	Runner
	RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error)
}

// ErrOutputLimit indicates a command exceeded its combined output budget.
var ErrOutputLimit = errors.New("command output limit exceeded")

// Exec runs commands directly with argv and a per-command deadline.
// Construct it with NewExec; its zero value uses the same safe defaults.
type Exec struct {
	timeout   time.Duration
	maxOutput int
}

// NewExec configures execution limits. Nonpositive values select defaults.
func NewExec(timeout time.Duration, maxOutput int) *Exec {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if maxOutput <= 0 {
		maxOutput = 1024 * 1024
	}
	return &Exec{timeout: timeout, maxOutput: maxOutput}
}

// Run returns combined stdout/stderr, limited in size even for failed commands.
// Errors never include command arguments, which may contain credentials.
func (r *Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.RunInput(ctx, nil, name, args...)
}

// RunInput executes a command with input supplied exclusively on stdin.
func (r *Exec) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	limits := NewExec(r.timeout, r.maxOutput)
	ctx, cancel := context.WithTimeout(ctx, limits.timeout)
	defer cancel()
	output := &boundedBuffer{limit: limits.maxOutput}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	if output.exceeded {
		err = errors.Join(err, ErrOutputLimit)
	}
	if err != nil {
		return output.buffer.Bytes(), fmt.Errorf("run %s: %w", name, err)
	}
	return output.buffer.Bytes(), nil
}

type boundedBuffer struct {
	sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	length := len(p)
	remaining := b.limit - b.buffer.Len()
	if length > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	// Consume the full write so the process cannot block on a full output pipe.
	return length, nil
}
