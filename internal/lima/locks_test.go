// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedMutexSerializesSameKey(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	ctx := context.Background()

	var concurrent, maxConcurrent int32
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := m.Lock(ctx, InstanceKey("dev"))
			if err != nil {
				t.Errorf("Lock returned error: %v", err)
				return
			}
			defer unlock()

			n := atomic.AddInt32(&concurrent, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if n <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&concurrent, -1)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Errorf("observed %d concurrent holders of the same key, want 1", got)
	}
}

func TestKeyedMutexAllowsDifferentKeys(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	ctx := context.Background()

	// Two different instances must not block each other, or Terraform's
	// parallel apply would be serialised for no reason.
	unlockA, err := m.Lock(ctx, InstanceKey("a"))
	if err != nil {
		t.Fatalf("locking a: %v", err)
	}
	defer unlockA()

	done := make(chan struct{})
	go func() {
		unlockB, err := m.Lock(ctx, InstanceKey("b"))
		if err == nil {
			unlockB()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("locking a different key blocked behind an unrelated key")
	}
}

func TestKeyedMutexRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	unlock, err := m.Lock(context.Background(), InstanceKey("dev"))
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	unlock2, err := m.Lock(ctx, InstanceKey("dev"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Lock took %s to give up, want ~50ms", elapsed)
	}
	// The returned unlock must always be safe to call, even on failure.
	unlock2()
}

func TestKeyedMutexAlreadyCancelledContext(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	unlock, err := m.Lock(ctx, InstanceKey("dev"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Lock error = %v, want Canceled", err)
	}
	unlock()
}

func TestKeyedMutexReleasesOnError(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	ctx := context.Background()

	// The pattern every caller uses: acquire, defer the release, fail inside.
	err := func() error {
		unlock, err := m.Lock(ctx, InstanceKey("dev"))
		if err != nil {
			return err
		}
		defer unlock()
		return errors.New("boom")
	}()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("error = %v, want boom", err)
	}

	// The lock must be free again despite the failure.
	done := make(chan struct{})
	go func() {
		unlock, err := m.Lock(ctx, InstanceKey("dev"))
		if err == nil {
			unlock()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lock was not released after the function returned an error")
	}
}

func TestKeyedMutexReleasesOnPanic(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	ctx := context.Background()

	func() {
		defer func() { _ = recover() }()
		unlock, err := m.Lock(ctx, InstanceKey("dev"))
		if err != nil {
			t.Errorf("Lock: %v", err)
			return
		}
		defer unlock()
		panic("boom")
	}()

	done := make(chan struct{})
	go func() {
		unlock, err := m.Lock(ctx, InstanceKey("dev"))
		if err == nil {
			unlock()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lock was not released after a panic")
	}
}

func TestKeyedMutexUnlockIsIdempotent(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	unlock, err := m.Lock(context.Background(), InstanceKey("dev"))
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	unlock()
	// A second call must not release someone else's lock or panic.
	unlock()

	unlock2, err := m.Lock(context.Background(), InstanceKey("dev"))
	if err != nil {
		t.Fatalf("re-lock: %v", err)
	}
	unlock2()
}

func TestKeyedMutexDoesNotLeakEntries(t *testing.T) {
	t.Parallel()

	m := NewKeyedMutex()
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		unlock, err := m.Lock(ctx, InstanceKey("dev"))
		if err != nil {
			t.Fatalf("Lock: %v", err)
		}
		unlock()
	}

	m.mu.Lock()
	n := len(m.locks)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("lock table holds %d entries after all locks were released, want 0", n)
	}
}

func TestKeyNamespaces(t *testing.T) {
	t.Parallel()

	// Namespacing keeps an instance named "x" from colliding with a disk
	// named "x", and both from colliding with the home-wide key.
	if InstanceKey("x") == DiskKey("x") || InstanceKey("x") == HomeKey() || DiskKey("x") == HomeKey() {
		t.Error("key namespaces collide")
	}
	if InstanceKey("dev") != "instance:dev" {
		t.Errorf("InstanceKey = %q", InstanceKey("dev"))
	}
}

func TestZeroKeyedMutexIsUsable(t *testing.T) {
	t.Parallel()

	var m KeyedMutex
	unlock, err := m.Lock(context.Background(), InstanceKey("dev"))
	if err != nil {
		t.Fatalf("Lock on zero value: %v", err)
	}
	unlock()
}
