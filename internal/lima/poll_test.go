package lima

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock records requested sleeps without ever waiting, so polling tests
// run instantly and deterministically.
type fakeClock struct {
	slept []time.Duration
	// after is the number of sleeps before the context is treated as expired.
	expireAfter int
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	c.slept = append(c.slept, d)
	if c.expireAfter > 0 && len(c.slept) >= c.expireAfter {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func TestPollSucceedsImmediately(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{}
	calls := 0
	err := Poll(context.Background(), PollOptions{Sleep: clock.sleep}, "start", "running",
		func(context.Context) (PollResult, error) {
			calls++
			return PollResult{Done: true, Observed: "running"}, nil
		})
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if calls != 1 {
		t.Errorf("check was called %d times, want 1", calls)
	}
	// An already-satisfied condition must not cost any wall-clock time.
	if len(clock.slept) != 0 {
		t.Errorf("Poll slept %v before the first check", clock.slept)
	}
}

func TestPollSucceedsAfterSeveralAttempts(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{}
	calls := 0
	err := Poll(context.Background(), PollOptions{Sleep: clock.sleep}, "start", "running",
		func(context.Context) (PollResult, error) {
			calls++
			if calls < 4 {
				return PollResult{Observed: "starting"}, nil
			}
			return PollResult{Done: true, Observed: "running"}, nil
		})
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if calls != 4 {
		t.Errorf("check was called %d times, want 4", calls)
	}
	if len(clock.slept) != 3 {
		t.Errorf("slept %d times, want 3", len(clock.slept))
	}
}

func TestPollBacksOff(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{}
	calls := 0
	_ = Poll(context.Background(), PollOptions{
		Interval:    time.Second,
		MaxInterval: 4 * time.Second,
		Factor:      2,
		Sleep:       clock.sleep,
	}, "start", "running", func(context.Context) (PollResult, error) {
		calls++
		return PollResult{Done: calls >= 6, Observed: "starting"}, nil
	})

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(clock.slept) != len(want) {
		t.Fatalf("slept %v, want %v", clock.slept, want)
	}
	for i := range want {
		if clock.slept[i] != want[i] {
			// Backoff must grow and then stay capped, so a slow VM boot is
			// not polled hundreds of times.
			t.Errorf("sleep %d = %s, want %s", i, clock.slept[i], want[i])
		}
	}
}

func TestPollTimeoutReportsLastObservedState(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{expireAfter: 3}
	err := Poll(context.Background(), PollOptions{Sleep: clock.sleep}, "start of instance \"dev\"", "running",
		func(context.Context) (PollResult, error) {
			return PollResult{Observed: "starting"}, nil
		})

	if err == nil {
		t.Fatal("Poll succeeded, want a timeout")
	}
	if !IsTimeout(err) {
		t.Errorf("IsTimeout(%v) = false, want true", err)
	}

	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not a TimeoutError", err)
	}
	if te.LastSeen != "starting" {
		t.Errorf("LastSeen = %q, want starting", te.LastSeen)
	}
	if te.Want != "running" {
		t.Errorf("Want = %q, want running", te.Want)
	}
	// The message must say what it was waiting for and what it actually saw.
	msg := err.Error()
	for _, want := range []string{"dev", "running", "starting", "timed out"} {
		if !contains(msg, want) {
			t.Errorf("timeout message %q is missing %q", msg, want)
		}
	}
}

func TestPollTimeoutWhenTheCheckItselfIsCancelled(t *testing.T) {
	t.Parallel()

	// The deadline commonly expires *inside* a Lima command rather than
	// during a sleep, because a command can outlive the remaining timeout.
	// The user must still get a timeout naming the last observed state, not a
	// bare "context deadline exceeded".
	calls := 0
	err := Poll(context.Background(), PollOptions{Sleep: (&fakeClock{}).sleep}, "start of instance \"dev\"", "running",
		func(context.Context) (PollResult, error) {
			calls++
			if calls == 1 {
				return PollResult{Observed: "starting"}, nil
			}
			// Simulates the adapter returning ctx.Err() mid-command.
			return PollResult{}, context.DeadlineExceeded
		})

	if err == nil {
		t.Fatal("Poll succeeded, want a timeout")
	}
	if !IsTimeout(err) {
		t.Fatalf("error = %v, want it reported as a TimeoutError", err)
	}

	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not a TimeoutError", err)
	}
	if te.LastSeen != "starting" {
		t.Errorf("LastSeen = %q, want starting", te.LastSeen)
	}
	// The original cause must remain inspectable.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the underlying context error was lost")
	}
}

func TestPollWrappedCancellationIsATimeout(t *testing.T) {
	t.Parallel()

	// A CommandError wrapping a cancellation must be recognised too.
	wrapped := &CommandError{Binary: "limactl", Cause: context.Canceled}
	err := Poll(context.Background(), PollOptions{Sleep: (&fakeClock{}).sleep}, "stop", "stopped",
		func(context.Context) (PollResult, error) {
			return PollResult{}, wrapped
		})

	if !IsTimeout(err) {
		t.Errorf("error = %v, want it reported as a TimeoutError", err)
	}
}

func TestPollPropagatesCheckErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lima exploded")
	err := Poll(context.Background(), PollOptions{Sleep: (&fakeClock{}).sleep}, "start", "running",
		func(context.Context) (PollResult, error) {
			return PollResult{}, sentinel
		})
	if !errors.Is(err, sentinel) {
		t.Errorf("Poll error = %v, want the check's error", err)
	}
	if IsTimeout(err) {
		t.Error("a check failure must not be reported as a timeout")
	}
}

func TestPollRespectsRealContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Poll(ctx, PollOptions{Interval: 5 * time.Millisecond, MaxInterval: 10 * time.Millisecond},
		"start", "running", func(context.Context) (PollResult, error) {
			return PollResult{Observed: "starting"}, nil
		})

	if err == nil {
		t.Fatal("Poll succeeded, want a timeout")
	}
	if !IsTimeout(err) {
		t.Errorf("error = %v, want a TimeoutError", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Poll took %s to notice cancellation", elapsed)
	}
}

func TestPollOptionDefaults(t *testing.T) {
	t.Parallel()

	got := PollOptions{}.withDefaults()
	if got.Interval <= 0 || got.MaxInterval <= 0 || got.Factor < 1 || got.Sleep == nil {
		t.Errorf("zero PollOptions produced unusable defaults: %+v", got)
	}
	if got.MaxInterval < got.Interval {
		t.Errorf("MaxInterval %s is below Interval %s", got.MaxInterval, got.Interval)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
