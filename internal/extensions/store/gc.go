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

// GC removes the images whose sha256 keep does not name (a temp file only
// once nothing has written to it for an hour), the sets that neither
// enabled, pending nor this boot's report names, and leftover temp links.
// It returns what it removed, relative to the store. Caller holds Lock.
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
	dir := config.ExtImagesDir()
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
		if sha, ok := strings.CutSuffix(name, ".raw"); ok && keep[sha] {
			continue
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
	keep := map[string]bool{}
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
