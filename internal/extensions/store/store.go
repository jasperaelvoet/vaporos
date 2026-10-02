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
// WriteEnabled, ClearPending, Promote, FailPending, AfterHealthy,
// AfterHealthyWant, GC, Collect). Put, Has and CleanTemp do not need it: an
// image only ever gets its final name sealed, and GC leaves fresh images and
// temp files alone. The slot files belong to the update lock (WriteSlot,
// RemoveSlot).
package store

import (
	"bufio"
	"errors"
	"fmt"
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

// maxListFile bounds the line files (wanted, proven, failed, a set's
// files): a larger one is an error, never read in part.
const maxListFile = 1 << 20

// maxLine bounds one line of them. No valid entry comes near it (the longest
// is an option line of about 400 bytes), so a longer line is skipped as junk
// rather than failing every read of the file.
const maxLine = 4 << 10

// ErrListTooBig means a line file is over maxListFile bytes.
var ErrListTooBig = errors.New("store file is too big")

// tempPrefix starts every temp file and directory the store writes, so a
// leftover one is never read as an image or a set.
const tempPrefix = ".tmp-"

// readLines returns the trimmed, non-empty, non-comment lines of path. A
// missing file has none; one over maxListFile is ErrListTooBig.
func readLines(path string) ([]string, error) {
	var out []string
	err := scanLines(path, maxListFile, func(line string) { out = append(out, line) })
	return out, err
}

// scanLines calls fn with each trimmed, non-empty, non-comment line of path
// shorter than maxLine. A limit > 0 refuses a larger file before reading
// any of it. Only a regular file is read: anything else might never end.
func scanLines(path string, limit int64, fn func(line string)) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	tooBig := func(size int64) error {
		return fmt.Errorf("%s: %w (%d bytes, at most %d)", path, ErrListTooBig, size, limit)
	}
	var in io.Reader = f
	// The store replaces these files by rename; the reader still catches
	// one that grows in place while it is read.
	lr := &io.LimitedReader{R: f, N: limit + 1}
	if limit > 0 {
		if fi.Size() > limit {
			return tooBig(fi.Size())
		}
		in = lr
	}
	r := bufio.NewReaderSize(in, maxLine)
	for {
		line, long, err := r.ReadLine()
		if long {
			for long && err == nil {
				_, long, err = r.ReadLine()
			}
		} else if err == nil {
			if l := strings.TrimSpace(string(line)); l != "" && !strings.HasPrefix(l, "#") {
				fn(l)
			}
		}
		if errors.Is(err, io.EOF) && limit > 0 && lr.N == 0 {
			return tooBig(limit + 1)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
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
