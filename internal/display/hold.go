package display

import (
	"context"
	"sync"
)

// HoldUnits keeps the display policy, sessions and Steam restarts from
// starting or stopping units until release: vosd holds it while it takes
// Steam down before a restart of its own, since a gamescope started
// meanwhile would apply steam.json again (docs/CONTRACTS.md "Extensions",
// Steam). It waits for a switch under way until ctx ends, and then returns
// ctx's error. Calling release again does nothing.
func (m *Manager) HoldUnits(ctx context.Context) (release func(), err error) {
	if err := m.op.LockCtx(ctx); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(m.op.Unlock) }, nil
}
