package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Pair is one image by identity: an extension id and its fs-verity digest.
type Pair struct {
	ID       string
	FSVerity string
}

func (p Pair) valid() bool { return manifest.ValidExtensionID(p.ID) && isHex64(p.FSVerity) }

func (p Pair) line() string { return p.ID + " " + p.FSVerity }

// Pairs returns the catalog's pair for each of ids it lists.
func Pairs(c *catalog.Catalog, ids []string) []Pair {
	var out []Pair
	for _, id := range ids {
		if e, ok := c.Get(id); ok {
			out = append(out, Pair{ID: id, FSVerity: e.FSVerity})
		}
	}
	return out
}

// Wanted returns the ids the user added, sorted. Lines that are not ids are
// ignored.
func Wanted() ([]string, error) {
	lines, err := readLines(config.ExtWantedPath())
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range lines {
		if manifest.ValidExtensionID(l) {
			ids = append(ids, l)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// WriteWanted replaces wanted with ids (sorted, without duplicates). Caller
// holds Lock.
func WriteWanted(ids []string) error {
	out := slices.Clone(ids)
	for _, id := range out {
		if !manifest.ValidExtensionID(id) {
			return fmt.Errorf("invalid extension id %q", id)
		}
	}
	slices.Sort(out)
	return writeLines(config.ExtWantedPath(), slices.Compact(out))
}

// Proven returns the images that passed a boot.
func Proven() (map[Pair]bool, error) {
	lines, err := readLines(config.ExtProvenPath())
	if err != nil {
		return nil, err
	}
	out := map[Pair]bool{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		if p := (Pair{ID: f[0], FSVerity: f[1]}); p.valid() {
			out[p] = true
		}
	}
	return out, nil
}

// AddProven adds pairs to proven, writing only when something is new.
// Caller holds Lock.
func AddProven(pairs []Pair) error {
	have, err := Proven()
	if err != nil {
		return err
	}
	changed := false
	for _, p := range pairs {
		if p.valid() && !have[p] {
			have[p] = true
			changed = true
		}
	}
	if !changed {
		return nil
	}
	lines := make([]string, 0, len(have))
	for p := range have {
		lines = append(lines, p.line())
	}
	slices.Sort(lines)
	return writeLines(config.ExtProvenPath(), lines)
}

// Failed returns the fingerprints of the sets whose trial failed.
func Failed() (map[string]bool, error) {
	lines, err := readLines(config.ExtFailedPath())
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, l := range lines {
		if isHex64(l) {
			out[l] = true
		}
	}
	return out, nil
}

// AddFailed records a failed set's fingerprint. Caller holds Lock.
func AddFailed(fp string) error {
	return editFailed(fp, true)
}

// RemoveFailed forgets a fingerprint ("Try again"). Caller holds Lock.
func RemoveFailed(fp string) error {
	return editFailed(fp, false)
}

func editFailed(fp string, add bool) error {
	if !isHex64(fp) {
		return fmt.Errorf("invalid set fingerprint %q", fp)
	}
	have, err := Failed()
	if err != nil {
		return err
	}
	if have[fp] == add {
		return nil
	}
	if add {
		have[fp] = true
	} else {
		delete(have, fp)
	}
	return writeLines(config.ExtFailedPath(), slices.Sorted(maps.Keys(have)))
}

// Fingerprint identifies a set by content: the hex sha256 of its sorted,
// unique "<id> <fsverity>" lines followed by its sorted, unique module
// option lines, each line ending in "\n". Pairs and options that are not
// valid are left out, so the two kinds of lines never collide.
func Fingerprint(pairs []Pair, options []string) string {
	var lines []string
	for _, p := range pairs {
		if p.valid() {
			lines = append(lines, p.line())
		}
	}
	slices.Sort(lines)
	h := sha256.New()
	for _, l := range slices.Compact(lines) {
		h.Write([]byte(l + "\n"))
	}
	for _, o := range normOptions(options) {
		h.Write([]byte(o + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

var (
	moduleNameRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	moduleValueRe = regexp.MustCompile(`^[0-9A-Za-z_x.-]{1,256}$`)
)

// normOptions returns the valid "options <module> <param>=<value>" lines,
// with single spaces, sorted and unique. The initramfs takes only lines of
// this shape, so others are dropped everywhere.
func normOptions(options []string) []string {
	var out []string
	for _, o := range options {
		f := strings.Fields(o)
		if len(f) != 3 || f[0] != "options" || !moduleNameRe.MatchString(f[1]) {
			continue
		}
		param, value, ok := strings.Cut(f[2], "=")
		if !ok || !moduleNameRe.MatchString(param) || !moduleValueRe.MatchString(value) {
			continue
		}
		out = append(out, strings.Join(f, " "))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
