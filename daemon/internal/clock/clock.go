// Package clock shares time sources and bounded polling across daemon operations.
package clock

import (
	"context"
	"sync"
	"time"
)

// Clock supplies policy time and interruptible timers.
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

// Real uses the system clock.
type Real struct{}

func (Real) Now() time.Time                         { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// PollUntil retries probe until it succeeds before the deadline. Probe errors are
// transient; cancellation or timeout is returned after the last unsuccessful probe.
func PollUntil(parent context.Context, clock Clock, timeout, interval time.Duration, probe func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	deadline := clock.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !clock.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		ready, err := probe(ctx)
		if err == nil && ready && ctx.Err() == nil && clock.Now().Before(deadline) {
			return nil
		}
		remaining := deadline.Sub(clock.Now())
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-clock.After(min(interval, remaining)):
		}
	}
}

// Fake is a manually advanced clock for tests of time-driven code.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

// NewFake starts a fake clock at start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After fires once the fake time reaches now+d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := f.now.Add(d)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, waiter{at: at, ch: ch})
	return ch
}

// Advance moves the fake time forward and fires every timer that is due.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	pending := f.waiters[:0]
	for _, w := range f.waiters {
		if w.at.After(f.now) {
			pending = append(pending, w)
			continue
		}
		w.ch <- f.now
	}
	f.waiters = pending
}

// Waiters reports how many timers are pending, so tests can wait until the
// code under test is blocked on the clock.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}
