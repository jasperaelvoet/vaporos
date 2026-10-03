package display

import (
	"context"
	"errors"
	"testing"
	"time"
)

// While vosd holds the units, the policy starts no gamescope; once it
// lets go, the next pass does. A switch under way is waited for only
// until the hold's context ends.
func TestHoldUnits(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	h.setActive(GamescopeUnit, true, false) // vosd stopped it before a restart

	release, err := m.HoldUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx, true)
	if h.isActive(GamescopeUnit, true) {
		t.Fatal("the policy started gamescope while the units were held")
	}
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := m.HoldUnits(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second hold got %v", err)
	}

	release()
	release() // a second release must not free a lock someone else took
	if !m.op.TryLock() {
		t.Fatal("the units stayed held after release")
	}
	if m.op.TryLock() {
		t.Fatal("a second release unlocked twice")
	}
	m.op.Unlock()
	m.reconcile(ctx, true)
	if !h.isActive(GamescopeUnit, true) {
		t.Fatal("the policy did not start gamescope after the hold")
	}
}
