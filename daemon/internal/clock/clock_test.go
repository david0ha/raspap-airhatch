package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

type advancing struct{ now time.Time }

func (c *advancing) Now() time.Time { return c.now }
func (c *advancing) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func TestPollUntilRetriesAndBoundsTime(t *testing.T) {
	for _, readyAfter := range []int{2, 100} {
		clock := &advancing{now: time.Unix(100, 0)}
		start := clock.Now()
		calls := 0
		err := PollUntil(context.Background(), clock, 250*time.Millisecond, 100*time.Millisecond, func(context.Context) (bool, error) {
			calls++
			if calls < readyAfter {
				return false, errors.New("transient")
			}
			return true, nil
		})
		if readyAfter == 2 {
			if err != nil || clock.Now().Sub(start) != 100*time.Millisecond {
				t.Fatalf("retry: %v", err)
			}
		} else if !errors.Is(err, context.DeadlineExceeded) || clock.Now().Sub(start) != 250*time.Millisecond {
			t.Fatalf("deadline: %v elapsed=%v", err, clock.Now().Sub(start))
		}
	}
}

func TestPollUntilRejectsLateAndCancelledSuccess(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		clock := &advancing{now: time.Unix(100, 0)}
		ctx, cancel := context.WithCancel(context.Background())
		err := PollUntil(ctx, clock, time.Second, time.Millisecond, func(context.Context) (bool, error) {
			if cancelled {
				cancel()
			} else {
				clock.now = clock.now.Add(time.Second)
			}
			return true, nil
		})
		cancel()
		if err == nil {
			t.Fatal("accepted success after deadline/cancellation")
		}
	}
}

func TestFakeFiresDueTimersOnly(t *testing.T) {
	start := time.Unix(1000, 0)
	f := NewFake(start)
	early, late := f.After(time.Second), f.After(time.Minute)
	if f.Waiters() != 2 {
		t.Fatalf("waiters = %d", f.Waiters())
	}
	f.Advance(2 * time.Second)
	select {
	case at := <-early:
		if !at.Equal(start.Add(2 * time.Second)) {
			t.Fatalf("fired at %v", at)
		}
	default:
		t.Fatal("due timer did not fire")
	}
	select {
	case <-late:
		t.Fatal("future timer fired")
	default:
	}
	if f.Waiters() != 1 || !f.Now().Equal(start.Add(2*time.Second)) {
		t.Fatalf("waiters=%d now=%v", f.Waiters(), f.Now())
	}
	if len(f.After(0)) != 1 {
		t.Fatal("zero duration must fire at once")
	}
}
