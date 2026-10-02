// Package store keeps the extension store on the data partition
// (/var/lib/vos/ext, docs/CONTRACTS.md "Extensions"): the sealed images, the
// sets and the enabled/pending links, wanted, proven and failed, the slot
// catalogs, the boot report, and the pure reconcile plan.
//
// It is a leaf: internal/update imports it, so it imports nothing from vos
// beyond config, manifest and the catalog.
//
// Locking: every function that writes wanted, a set, the enabled or pending
// link, proven or failed, or runs GC expects the caller to hold Lock
// (WriteWanted, AddProven, AddFailed, RemoveFailed, WriteSet, Propose,
// WriteEnabled, ClearPending, Promote, FailPending, AfterHealthy, GC). Put,
// Has and CleanTemp do not need it: an image only ever gets its final name
// sealed, and GC leaves fresh temp files alone. The slot files belong to the
// update lock (WriteSlot, RemoveSlot).
package store

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

var (
	// ErrMismatch means an image's bytes are not the ones the catalog lists:
	// wrong size, sha256 or fs-verity digest.
	ErrMismatch = errors.New("extension image does not match the catalog")
	// ErrUnsupported means the kernel or filesystem cannot seal files with
	// fs-verity (always the case off Linux).
	ErrUnsupported = errors.New("fs-verity is not supported here")
)

// maxListFile bounds the line files (wanted, proven, failed, a set's files).
const maxListFile = 1 << 20

// tempPrefix starts every temp file and directory the store writes, so a
// leftover one is never read as an image or a set.
const tempPrefix = ".tmp-"

// readLines returns the trimmed, non-empty, non-comment lines of path. A
// missing file has none.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(io.LimitReader(f, maxListFile))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

func joinLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// writeLines replaces path atomically (temp file, fsync, rename, directory
// fsync).
func writeLines(path string, lines []string) error {
	return config.WriteFileAtomic(path, joinLines(lines), 0o644)
}

// syncDir makes the renames and removals in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeSynced creates path with data and fsyncs it.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
