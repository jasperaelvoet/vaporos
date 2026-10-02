//go:build !linux

package extensions

// idleIO is Linux's ioprio_set; elsewhere (the dev Mac) I/O keeps its
// priority.
func idleIO() error { return nil }
