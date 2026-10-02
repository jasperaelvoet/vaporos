//go:build !unix

package buildcheck

import "io/fs"

// owner is unknown off Unix, where images are never built.
func owner(fs.FileInfo) string { return "" }
