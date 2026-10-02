//go:build !linux

package buildcheck

// forbiddenXattrs is Linux only: images are built on Linux, and other
// systems name and store extended attributes differently.
func forbiddenXattrs(string) ([]string, error) { return nil, nil }
