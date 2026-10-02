package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockContention(t *testing.T) {
	setup(t)
	unlock, err := Lock(t.Context())
	check(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock = %v, want DeadlineExceeded", err)
	}

	got := make(chan error, 1)
	go func() {
		u, err := Lock(t.Context())
		if err == nil {
			u()
		}
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	unlock()
	unlock() // idempotent
	select {
	case err := <-got:
		check(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("waiting Lock never got the lock")
	}
}
