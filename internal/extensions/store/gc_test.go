package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

func TestGC(t *testing.T) {
	setup(t)
	keep, drop := newImage(t, "proton", 3_000), newImage(t, "coolercontrol", 2_000)
	put(t, keep)
	put(t, drop)
	images := config.ExtImagesDir()
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
	eq(t, "images", strs(entries(t, images)), strs([]string{tempPrefix + "truckersmp-fresh", keep.entry.SHA256 + ".raw"}))
	eq(t, "sets", strs(entries(t, config.ExtSetsDir())), strs([]string{"1", "2", "4"}))
	noTemps(t, config.ExtDir())
	eq(t, "kept image still sealed", has(t, keep.entry), true)
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
	eq(t, "sets", strs(entries(t, config.ExtSetsDir())), strs([]string{"1"}))
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
