//go:build !linux

package drm

import "context"

// WatchHotplug is Linux-only; elsewhere callers fall back to polling.
func WatchHotplug(ctx context.Context) (<-chan struct{}, error) { return nil, ErrUnsupported }
