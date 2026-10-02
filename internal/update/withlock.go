package update

import "context"

// WithLock runs fn holding the update lock, which also covers the extension
// slot files (docs/CONTRACTS.md "Extensions"). It waits for the lock until
// ctx ends. Hold it briefly: a stage that finds it held for long gives up
// with ErrBusy.
func WithLock(ctx context.Context, fn func() error) error {
	lock, err := takeUpdateLock(ctx, 0)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return fn()
}
