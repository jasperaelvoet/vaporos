package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Set is one attempt at a set of extensions: sets/<Name>/ids,
// modprobe.conf and tries.
type Set struct {
	Name    string   // <n>, a decimal number
	IDs     []string // catalog order
	Options []string // "options <module> <param>=<value>" lines, sorted
	Tries   int      // boots left to try it while it is pending (0-9)
}

// ProposeTries is how many boots a new pending set gets.
const ProposeTries = 2

const (
	setIDsFile      = "ids"
	setOptionsFile  = "modprobe.conf"
	setTriesFile    = "tries"
	setLinkPrefix   = "sets/"
	nextSetFile     = ".next" // in sets/: the next set number, never lowered
	maxSetTriesFile = 64
)

var setNameRe = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

func setDir(name string) string { return filepath.Join(config.ExtSetsDir(), name) }

// ReadSet reads sets/<name>. A set that does not exist is an error matching
// fs.ErrNotExist. Lines that are not ids or valid option lines are ignored,
// and a tries file that is not one digit reads as 0.
func ReadSet(name string) (*Set, error) {
	if !setNameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid set name %q", name)
	}
	dir := setDir(name)
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("set %s: %w", name, fs.ErrNotExist)
	}
	lines, err := readLines(filepath.Join(dir, setIDsFile))
	if err != nil {
		return nil, err
	}
	s := &Set{Name: name, IDs: cleanIDs(lines)}
	opts, err := readLines(filepath.Join(dir, setOptionsFile))
	if err != nil {
		return nil, err
	}
	s.Options = normOptions(opts)
	s.Tries, err = readTries(filepath.Join(dir, setTriesFile))
	if err != nil {
		return nil, err
	}
	return s, nil
}

// cleanIDs keeps the valid ids, first occurrence first.
func cleanIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if manifest.ValidExtensionID(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// readTries parses a tries file the way the initramfs does: one digit, and
// anything else (or nothing) is 0.
func readTries(path string) (int, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSetTriesFile))
	if err != nil {
		return 0, err
	}
	return parseTries(string(b)), nil
}

func parseTries(s string) int {
	s = strings.TrimSpace(s)
	if len(s) != 1 || s[0] < '0' || s[0] > '9' {
		return 0
	}
	return int(s[0] - '0')
}

// WriteSet writes a new set: into sets/.tmp-<n>/, every file and the
// directory fsynced, then renamed to sets/<n> and sets/ fsynced, and the
// high-water mark moved past <n> (nextSetName). Option lines that are not
// valid are dropped and tries is clamped to 0-9. Caller holds Lock.
func WriteSet(ids, options []string, tries int) (*Set, error) {
	for _, id := range ids {
		if !manifest.ValidExtensionID(id) {
			return nil, fmt.Errorf("invalid extension id %q", id)
		}
	}
	s := &Set{IDs: cleanIDs(ids), Options: normOptions(options), Tries: min(max(tries, 0), 9)}
	sets := config.ExtSetsDir()
	if err := os.MkdirAll(sets, 0o755); err != nil {
		return nil, err
	}
	if err := removeTemps(sets); err != nil {
		return nil, err
	}
	n, err := nextSetName()
	if err != nil {
		return nil, err
	}
	s.Name = n
	tmp := filepath.Join(sets, tempPrefix+n)
	if err := writeSetDir(tmp, s); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, setDir(n)); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := syncDir(sets); err != nil {
		return nil, err
	}
	// After the rename: until this lands, sets/<n> itself keeps <n> taken.
	v, _ := strconv.Atoi(n)
	if err := config.WriteFileAtomic(nextSetPath(), []byte(strconv.Itoa(v+1)+"\n"), 0o644); err != nil {
		return nil, err
	}
	return s, nil
}

func writeSetDir(dir string, s *Set) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
	}{
		{setIDsFile, joinLines(s.IDs)},
		{setOptionsFile, joinLines(s.Options)},
		{setTriesFile, []byte(strconv.Itoa(s.Tries) + "\n")},
	}
	for _, f := range files {
		if err := writeSynced(filepath.Join(dir, f.name), f.data); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// nextSetName never hands out a number twice: it is at least the high-water
// mark in sets/.next and one more than the highest set number in sets/ or
// named by a link or the boot report. Nothing that remembers a set by number
// (a dangling link, the auto-restart breaker) then mistakes a new set for a
// collected one.
func nextSetName() (string, error) {
	ents, err := os.ReadDir(config.ExtSetsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	names := make([]string, 0, len(ents)+3)
	for _, e := range ents {
		names = append(names, e.Name())
	}
	for _, link := range []string{config.ExtEnabledLink(), config.ExtPendingLink()} {
		n, err := linkName(link)
		if err != nil {
			return "", err
		}
		names = append(names, n)
	}
	if rep, err := LoadBootReport(); err == nil {
		names = append(names, rep.Set)
	}
	top := 0
	for _, n := range names {
		if setNameRe.MatchString(n) {
			v, _ := strconv.Atoi(n)
			top = max(top, v)
		}
	}
	next, err := readNextSet()
	if err != nil {
		return "", err
	}
	n := strconv.Itoa(max(top+1, next))
	if !setNameRe.MatchString(n) {
		return "", fmt.Errorf("no set number left after %d", top)
	}
	return n, nil
}

// readNextSet reads the high-water mark; a missing one, or one that is not
// a set number, is 0.
func readNextSet() (int, error) {
	f, err := os.Open(nextSetPath())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSetTriesFile))
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if !setNameRe.MatchString(s) {
		return 0, nil
	}
	v, _ := strconv.Atoi(s)
	return v, nil
}

func nextSetPath() string { return filepath.Join(config.ExtSetsDir(), nextSetFile) }

// removeTemps removes the leftover temp entries in dir. Only for
// directories whose writers hold Lock, so every temp there is stale.
func removeTemps(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkName returns the set a link names, or "" when the link is missing or
// is not a "sets/<n>" symlink.
func linkName(path string) (string, error) {
	t, err := os.Readlink(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EINVAL) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	n, ok := strings.CutPrefix(t, setLinkPrefix)
	if !ok || !setNameRe.MatchString(n) {
		return "", nil
	}
	return n, nil
}

// setLink points link at sets/<name>: a temp symlink renamed over it, then
// the directory fsynced.
func setLink(link, name string) error {
	dir := filepath.Dir(link)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, tempPrefix+filepath.Base(link))
	os.Remove(tmp)
	if err := os.Symlink(setLinkPrefix+name, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func linkedSet(link string) (*Set, error) {
	n, err := linkName(link)
	if n == "" || err != nil {
		return nil, err
	}
	s, err := ReadSet(n)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return s, err
}

// Enabled returns the last good set, or nil when there is none.
func Enabled() (*Set, error) { return linkedSet(config.ExtEnabledLink()) }

// Pending returns the set on trial, or nil when there is none.
func Pending() (*Set, error) { return linkedSet(config.ExtPendingLink()) }

// Propose writes a new set with ProposeTries tries and makes it pending.
// Renaming the pending link into place is the commit. Caller holds Lock.
func Propose(ids, options []string) (*Set, error) {
	s, err := WriteSet(ids, options, ProposeTries)
	if err != nil {
		return nil, err
	}
	if err := setLink(config.ExtPendingLink(), s.Name); err != nil {
		return nil, err
	}
	return s, nil
}

// WriteEnabled writes a new set and makes it enabled directly, with no
// trial (the installer, and a repair). Caller holds Lock.
func WriteEnabled(ids, options []string) (*Set, error) {
	s, err := WriteSet(ids, options, 0)
	if err != nil {
		return nil, err
	}
	if err := setLink(config.ExtEnabledLink(), s.Name); err != nil {
		return nil, err
	}
	return s, nil
}

// ClearPending removes the pending link. Caller holds Lock.
func ClearPending() error { return removeLink(config.ExtPendingLink()) }

// ClearEnabled removes the enabled link: a repair starts over from core,
// through a trial. Caller holds Lock.
func ClearEnabled() error { return removeLink(config.ExtEnabledLink()) }

func removeLink(link string) error {
	err := os.Remove(link)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(link))
}

// Promote makes the set this boot tried the enabled one, if pending still
// names it: enabled is pointed at it, then pending is removed. It reports
// whether it did. Caller holds Lock.
func Promote(bootedSet string) (bool, error) {
	n, err := linkName(config.ExtPendingLink())
	if err != nil || n == "" || n != bootedSet {
		return false, err
	}
	if _, err := ReadSet(n); err != nil {
		return false, err
	}
	if err := setLink(config.ExtEnabledLink(), n); err != nil {
		return false, err
	}
	return true, ClearPending()
}

// FailPending records the pending set's fingerprint (its ids at cat's
// digests, and its options) in failed and removes pending. cat is the
// booted catalog; on an OS trial the plan clears pending instead, as the
// failed trials ran at another image's digests. Caller holds Lock.
func FailPending(cat *catalog.Catalog) error {
	p, err := Pending()
	if err != nil {
		return err
	}
	if p != nil {
		if err := AddFailed(Fingerprint(Pairs(cat, p.IDs), p.Options)); err != nil {
			return err
		}
	}
	return ClearPending()
}
