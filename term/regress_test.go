package term

import (
	"testing"
	"time"
)

// TestRemoveDoesNotDeadlock proves Device.remove does not re-enter its own
// mutex. Against the original implementation (d.mu.Lock() followed by
// d.Get(), which takes d.mu.RLock() on the same non-reentrant RWMutex)
// this test hangs and fails on the timeout.
func TestRemoveDoesNotDeadlock(t *testing.T) {
	d := New(nil)
	rid, err := d.Alloc()
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		d.remove(rid)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("remove() deadlocked: re-entrant lock on d.mu")
	}

	if _, err := d.Get(rid); err == nil {
		t.Fatal("resource still present after remove()")
	}
}

// TestRemoveUnknownIDIsNoop guards the early-return path that also has to
// release the lock before returning.
func TestRemoveUnknownIDIsNoop(t *testing.T) {
	d := New(nil)
	done := make(chan struct{})
	go func() {
		d.remove("does-not-exist")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("remove() of unknown id deadlocked")
	}

	// Mutex must still be usable afterwards.
	if _, err := d.Alloc(); err != nil {
		t.Fatalf("device unusable after no-op remove: %v", err)
	}
}
