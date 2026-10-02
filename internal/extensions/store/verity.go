package store

import "errors"

// errNotSealed means a file has no fs-verity (or not the sha256 kind the
// store uses).
var errNotSealed = errors.New("file is not sealed with fs-verity")

// The kernel calls, as variables so tests can fake them. Each takes a file
// opened read-only.
var (
	// enableVerity seals the file: FS_IOC_ENABLE_VERITY with sha256,
	// 4096-byte blocks and no salt. The kernel refuses every write to it
	// afterwards and checks every block it reads.
	enableVerity = sysEnableVerity
	// measureVerity returns the file's fs-verity digest as 64 hex digits
	// (FS_IOC_MEASURE_VERITY), or errNotSealed.
	measureVerity = sysMeasureVerity
	// verityAttr reports whether statx shows STATX_ATTR_VERITY.
	verityAttr = sysVerityAttr
)
