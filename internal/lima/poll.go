// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PollOptions configures Poll. The zero value uses sensible defaults.
type PollOptions struct {
	// Interval is the first wait between attempts. Default 2s.
	Interval time.Duration
	// MaxInterval caps the backoff. Default 15s.
	MaxInterval time.Duration
	// Factor is the backoff multiplier. Default 1.5.
	Factor float64
	// Sleep overrides time.Sleep-style waiting, for tests. It must respect
	// context cancellation.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (o PollOptions) withDefaults() PollOptions {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.MaxInterval <= 0 {
		o.MaxInterval = 15 * time.Second
	}
	if o.Factor < 1 {
		o.Factor = 1.5
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	return o
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isCancellation reports whether err is a context deadline or cancellation,
// including one wrapped by a command failure.
func isCancellation(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// TimeoutError reports that polling ran out of time, including the last state
// observed so the diagnostic can say what the instance was actually doing.
type TimeoutError struct {
	Operation string
	Want      string
	LastSeen  string
	Elapsed   time.Duration
	Cause     error
}

func (e *TimeoutError) Error() string {
	last := e.LastSeen
	if last == "" {
		last = "unknown"
	}
	return fmt.Sprintf("timed out after %s waiting for %s (wanted %s, last observed %s)",
		e.Elapsed.Round(time.Second), e.Operation, e.Want, last)
}

func (e *TimeoutError) Unwrap() error { return e.Cause }

// PollResult is what a poll attempt reports back.
type PollResult struct {
	// Done ends polling successfully.
	Done bool
	// Observed describes the current state, used in timeout diagnostics.
	Observed string
}

// Poll repeatedly calls check until it reports Done, ctx expires, or check
// returns an error.
//
// check is called immediately, before any sleep, so an already-satisfied
// condition costs no wall-clock time. Backoff grows geometrically from
// Interval to MaxInterval, which keeps a slow VM boot from being polled
// hundreds of times while still reacting quickly to a fast one.
func Poll(ctx context.Context, opts PollOptions, operation, want string, check func(context.Context) (PollResult, error)) error {
	o := opts.withDefaults()
	start := time.Now()
	interval := o.Interval
	lastSeen := ""

	timedOut := func(cause error) error {
		return &TimeoutError{
			Operation: operation,
			Want:      want,
			LastSeen:  lastSeen,
			Elapsed:   time.Since(start),
			Cause:     cause,
		}
	}

	for {
		res, err := check(ctx)
		if err != nil {
			// The deadline can expire inside the check itself — a Lima
			// command can easily outlive a short remaining timeout. Report
			// that as a timeout with the last observed state, not as a bare
			// "context deadline exceeded", which tells the user nothing about
			// what the instance was doing.
			if isCancellation(err) {
				return timedOut(err)
			}
			return err
		}
		if res.Observed != "" {
			lastSeen = res.Observed
		}
		if res.Done {
			return nil
		}

		if err := o.Sleep(ctx, interval); err != nil {
			if isCancellation(err) {
				return timedOut(err)
			}
			return err
		}

		next := time.Duration(float64(interval) * o.Factor)
		if next > o.MaxInterval {
			next = o.MaxInterval
		}
		interval = next
	}
}
