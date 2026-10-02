package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

// KeepImages returns the sha256 of the images GC keeps: those wanted ∪ core
// (with requirements) resolve to in each catalog (the booted one and both
// slot files'), and those this boot mounted.
func KeepImages(wanted []string, cats []*catalog.Catalog, rep *BootReport) map[string]bool {
	keep := map[string]bool{}
	for _, c := range cats {
		for _, id := range c.Closure(append(slices.Clone(wanted), c.Core()...)) {
			if e, ok := c.Get(id); ok {
				keep[e.SHA256] = true
			}
		}
	}
	if rep != nil {
		for _, m := range rep.Mounted {
			if isHex64(m.SHA256) {
				keep[m.SHA256] = true
			}
		}
	}
	return keep
}

// Collect is the store's garbage collection, and the one its callers use:
// GC with the images KeepImages names, then proven pruned to the pairs that
// a catalog of cats lists or this boot mounted. cats are the booted catalog
// and both slot files' (a nil one lists nothing), read under the same Lock
// (a stage writes a slot file under it). Caller holds Lock.
func Collect(wanted []string, cats []*catalog.Catalog, rep *BootReport) ([]string, error) {
	removed, err := GC(KeepImages(wanted, cats, rep))
	if err != nil {
		return removed, err
	}
	return removed, pruneProven(cats, rep)
}

// GC removes the images whose sha256 keep does not name (an image or a temp
// file only once nothing has written to it for an hour; anything else in
// images/ at once), the sets that neither enabled, pending nor this boot's
// report names, and leftover temp links. It returns what it removed,
// relative to the store. It is only Collect's first half: callers use
// Collect, as GC alone never prunes proven. Caller holds Lock.
func GC(keep map[string]bool) ([]string, error) {
	removed, err := gcImages(keep, time.Now())
	if err != nil {
		return removed, err
	}
	sets, err := gcSets()
	removed = append(removed, sets...)
	if err != nil {
		return removed, err
	}
	if err := removeTemps(config.ExtDir()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return removed, err
	}
	return removed, nil
}

func gcImages(keep map[string]bool, now time.Time) ([]string, error) {
	dir, err := imagesDir()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, tempPrefix) {
			continue
		}
		if sha, ok := strings.CutSuffix(name, ".raw"); ok && isHex64(sha) {
			if keep[sha] {
				continue
			}
			fi, err := e.Info()
			if err != nil || now.Sub(fi.ModTime()) < freshGrace {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return removed, err
		}
		removed = append(removed, filepath.Join("images", name))
	}
	temps, err := cleanImageTemps(now)
	removed = append(removed, temps...)
	if err != nil {
		return removed, err
	}
	if len(removed) > 0 {
		return removed, syncDir(dir)
	}
	return removed, nil
}

func gcSets() ([]string, error) {
	keep := map[string]bool{nextSetFile: true}
	for _, link := range []string{config.ExtEnabledLink(), config.ExtPendingLink()} {
		n, err := linkName(link)
		if err != nil {
			return nil, err
		}
		keep[n] = true
	}
	// Without knowing the booted set, keep every set.
	rep, err := LoadBootReport()
	if err != nil {
		return nil, err
	}
	keep[rep.Set] = true

	dir := config.ExtSetsDir()
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range ents {
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return removed, err
		}
		removed = append(removed, filepath.Join("sets", e.Name()))
	}
	if len(removed) > 0 {
		return removed, syncDir(dir)
	}
	return removed, nil
}

// pruneProven keeps the proven pairs a catalog of cats lists or this boot
// mounted, so the file stays far below maxListFile however many versions
// pass. It reads the file without that limit, so it also shrinks one that
// grew past it, and rewrites one with a line too long to read.
func pruneProven(cats []*catalog.Catalog, rep *BootReport) error {
	listed := map[Pair]bool{}
	for _, c := range cats {
		if c == nil {
			continue
		}
		for _, e := range c.Entries {
			listed[Pair{ID: e.ID, FSVerity: e.FSVerity}] = true
		}
	}
	for _, p := range rep.MountedPairs() {
		listed[p] = true
	}
	kept := map[Pair]bool{}
	dropped := false
	long, err := scanLines(config.ExtProvenPath(), 0, func(line string) {
		f := strings.Fields(line)
		if len(f) == 2 {
			if p := (Pair{ID: f[0], FSVerity: f[1]}); listed[p] && !kept[p] {
				kept[p] = true
				return
			}
		}
		dropped = true
	})
	if err != nil || (!dropped && long == 0) {
		return err
	}
	lines := make([]string, 0, len(kept))
	for p := range kept {
		lines = append(lines, p.line())
	}
	slices.Sort(lines)
	return writeLines(config.ExtProvenPath(), lines)
}
