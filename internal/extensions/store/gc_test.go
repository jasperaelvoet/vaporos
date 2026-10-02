package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

func TestGC(t *testing.T) {
	setup(t)
	keep, drop, fresh := newImage(t, "proton", 3_000), newImage(t, "coolercontrol", 2_000), newImage(t, "truckersmp", 1_000)
	put(t, keep)
	put(t, drop)
	put(t, fresh)
	images := config.ExtImagesDir()
	age(t, ImagePath(drop.entry.SHA256), 2*time.Hour)
	writeFile(t, filepath.Join(images, "junk"), "x")
	writeFile(t, filepath.Join(images, tempPrefix+"truckersmp-fresh"), "x")
	writeFile(t, filepath.Join(images, tempPrefix+"truckersmp-old"), "x")
	age(t, filepath.Join(images, tempPrefix+"truckersmp-old"), 2*time.Hour)

	for range 5 {
		writeSet(t, []string{"proton"}, nil, 2)
	}
	writeFile(t, filepath.Join(config.ExtSetsDir(), tempPrefix+"9", "ids"), "proton\n")
	check(t, setLink(config.ExtEnabledLink(), "1"))
	check(t, setLink(config.ExtPendingLink(), "4"))
	writeReport(t, `{"mode":"enabled","set":"2"}`)
	check(t, os.Symlink("sets/5", filepath.Join(config.ExtDir(), tempPrefix+"pending")))

	removed, err := GC(map[string]bool{keep.entry.SHA256: true})
	check(t, err)
	want := []string{
		filepath.Join("images", drop.entry.SHA256+".raw"),
		filepath.Join("images", tempPrefix+"truckersmp-old"),
		filepath.Join("images", "junk"),
		filepath.Join("sets", tempPrefix+"9"),
		filepath.Join("sets", "3"),
		filepath.Join("sets", "5"),
	}
	slices.Sort(removed)
	slices.Sort(want)
	eq(t, "removed", strs(removed), strs(want))
	wantImages := []string{tempPrefix + "truckersmp-fresh", keep.entry.SHA256 + ".raw", fresh.entry.SHA256 + ".raw"}
	slices.Sort(wantImages)
	eq(t, "images", strs(entries(t, images)), strs(wantImages))
	eq(t, "sets", strs(entries(t, config.ExtSetsDir())), strs([]string{nextSetFile, "1", "2", "4"}))
	noTemps(t, config.ExtDir())
	eq(t, "kept image still sealed", has(t, keep.entry), true)
	eq(t, "fresh image still sealed", has(t, fresh.entry), true)

	// An hour on, nothing keeps the fresh image.
	age(t, ImagePath(fresh.entry.SHA256), 2*time.Hour)
	removed, err = GC(map[string]bool{keep.entry.SHA256: true})
	check(t, err)
	eq(t, "removed later", strs(removed), strs([]string{filepath.Join("images", fresh.entry.SHA256+".raw")}))
}

func TestCollectPrunesProven(t *testing.T) {
	setup(t)
	other := &catalog.Catalog{Entries: []catalog.Entry{
		{ID: "proton", SHA256: hex64('a'), Size: 1, FSVerity: hex64('b'), Core: true},
	}}
	rep := &BootReport{Mode: ModeEnabled, Mounted: []Mounted{{ID: "old", SHA256: hex64('7'), FSVerity: hex64('8')}}}
	keepLines := []string{
		"coolercontrol " + hex64('4'), // the booted catalog's
		"old " + hex64('8'),           // mounted this boot
		"proton " + hex64('2'),
		"proton " + hex64('b'), // the other slot's
	}
	// Over maxListFile: Proven refuses it, and Collect shrinks it.
	var b strings.Builder
	for i := 0; b.Len() <= maxListFile; i++ {
		fmt.Fprintf(&b, "proton %064x\n", 1<<20+i)
	}
	b.WriteString(strings.Join(keepLines, "\n") + "\nproton " + hex64('2') + "\n" + strings.Repeat("x", 3*maxLine) + "\n")
	writeFile(t, config.ExtProvenPath(), b.String())
	if _, err := Proven(); !errors.Is(err, ErrListTooBig) {
		t.Fatalf("Proven on a %d-byte file = %v, want ErrListTooBig", b.Len(), err)
	}

	_, err := Collect(nil, []*catalog.Catalog{testCatalog, other, nil}, rep)
	check(t, err)
	data, err := os.ReadFile(config.ExtProvenPath())
	check(t, err)
	eq(t, "proven", string(data), strings.Join(keepLines, "\n")+"\n")
	p, err := Proven()
	check(t, err)
	eq(t, "pairs", len(p), len(keepLines))

	// Nothing to drop: the file is not rewritten.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	check(t, os.Chtimes(config.ExtProvenPath(), old, old))
	_, err = Collect(nil, []*catalog.Catalog{testCatalog, other}, rep)
	check(t, err)
	fi, err := os.Stat(config.ExtProvenPath())
	check(t, err)
	eq(t, "mtime", fi.ModTime().Equal(old), true)
}

func TestGCEmptyStore(t *testing.T) {
	setup(t)
	removed, err := GC(nil)
	check(t, err)
	eq(t, "removed", len(removed), 0)
}

func TestGCKeepsSetsWithoutAReadableReport(t *testing.T) {
	setup(t)
	writeSet(t, nil, nil, 0)
	writeReport(t, "{broken")
	if _, err := GC(nil); err == nil {
		t.Error("GC went on without knowing the booted set")
	}
	eq(t, "sets", strs(entries(t, config.ExtSetsDir())), strs([]string{nextSetFile, "1"}))
}

func TestKeepImages(t *testing.T) {
	other := &catalog.Catalog{Entries: []catalog.Entry{
		{ID: "proton", SHA256: hex64('a'), Size: 1, FSVerity: hex64('b'), Core: true},
		{ID: "truckersmp", SHA256: hex64('c'), Size: 1, FSVerity: hex64('d'), Requires: []string{"proton"}},
		{ID: "coolercontrol", SHA256: hex64('e'), Size: 1, FSVerity: hex64('f')},
	}}
	rep := &BootReport{Mounted: []Mounted{{ID: "old", SHA256: hex64('7'), FSVerity: hex64('8')}, {ID: "bad", SHA256: "x"}}}
	keep := KeepImages([]string{"truckersmp"}, []*catalog.Catalog{testCatalog, other, nil}, rep)
	want := []string{hex64('1'), hex64('5'), hex64('7'), hex64('a'), hex64('c')}
	var got []string
	for sha := range keep {
		got = append(got, sha)
	}
	slices.Sort(got)
	eq(t, "keep", strs(got), strs(want))
}
