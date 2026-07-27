package lima

import (
	"context"
	"fmt"
	"sync"
)

// KeyedMutex serialises operations that mutate the same Lima object while
// letting operations on different objects proceed concurrently.
//
// Terraform may apply several lima_instance resources in parallel; two
// operations on the same instance would race inside Lima, but two operations
// on different instances are safe. A global lock would needlessly serialise
// every VM creation.
//
// Keys are namespaced strings, e.g. InstanceKey("dev").
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[string]*entry
}

type entry struct {
	ch   chan struct{}
	refs int
}

// NewKeyedMutex returns a ready-to-use KeyedMutex. The zero value is also
// usable.
func NewKeyedMutex() *KeyedMutex { return &KeyedMutex{} }

// InstanceKey returns the lock key for an instance.
func InstanceKey(name string) string { return "instance:" + name }

// DiskKey returns the lock key for a Lima disk.
func DiskKey(name string) string { return "disk:" + name }

// NetworkKey returns the lock key for a Lima network.
func NetworkKey(name string) string { return "network:" + name }

// Lock acquires the lock for key, blocking until it is free or ctx is done.
//
// It returns an unlock function which is safe to call exactly once. On context
// cancellation it returns the context error and a no-op unlock, so callers can
// always `defer unlock()` without checking.
func (m *KeyedMutex) Lock(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return func() {}, err
	}

	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*entry)
	}
	e, ok := m.locks[key]
	if !ok {
		// Buffered by one and pre-filled: a receive is an acquire.
		e = &entry{ch: make(chan struct{}, 1)}
		e.ch <- struct{}{}
		m.locks[key] = e
	}
	e.refs++
	m.mu.Unlock()

	release := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		e.refs--
		if e.refs == 0 {
			delete(m.locks, key)
		}
	}

	select {
	case <-e.ch:
		var once sync.Once
		return func() {
			once.Do(func() {
				// Return the token before dropping the reference, so a
				// waiter blocked on the channel is always woken.
				e.ch <- struct{}{}
				release()
			})
		}, nil
	case <-ctx.Done():
		release()
		return func() {}, ctx.Err()
	}
}

// WithLock runs fn while holding the lock for key. The lock is released even
// if fn panics.
func (m *KeyedMutex) WithLock(ctx context.Context, key string, fn func() error) error {
	unlock, err := m.Lock(ctx, key)
	if err != nil {
		return fmt.Errorf("acquiring lock %q: %w", key, err)
	}
	defer unlock()
	return fn()
}
