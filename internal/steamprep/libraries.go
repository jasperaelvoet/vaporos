package steamprep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// pendingLibrariesPath is vosd's queue of adopted libraries Steam does
// not list yet (internal/storage).
func pendingLibrariesPath() string { return filepath.Join(config.StateDir, "steam-libraries.json") }

// addQueuedLibraries adds the libraries vosd queued to Steam's library lists.
// vosd adds them itself only while Steam is stopped, and on a box whose
// Steam starts at boot and never stops it never sees that; prepare runs
// right before every Steam start, so it is the one place sure to. vosd
// finds them listed on its next round and takes them off its queue. A
// library whose disk is not there keeps waiting; nothing here goes into
// the record's error, which is about VaporOS's own entries.
func (p *prep) addQueuedLibraries() {
	raw, err := readFile(pendingLibrariesPath(), 1<<20)
	if err != nil {
		p.o.Log.Printf("prepare: libraries: %v", err)
		return
	}
	if raw.missing {
		return
	}
	var q struct {
		Pending []string `json:"pending"`
	}
	if err := json.Unmarshal(raw.data, &q); err != nil {
		p.o.Log.Printf("prepare: libraries: %s: %v", raw.path, err)
		return
	}
	var dirs []string
	for _, dir := range q.Pending {
		if !filepath.IsAbs(dir) {
			continue
		}
		// An unmounted disk leaves its empty mountpoint behind.
		if fi, err := os.Stat(filepath.Join(dir, "steamapps")); err != nil || !fi.IsDir() {
			continue
		}
		dirs = append(dirs, filepath.Clean(dir))
	}
	if len(dirs) == 0 {
		return
	}
	lists := []string{
		filepath.Join(p.root, "steamapps", "libraryfolders.vdf"),
		filepath.Join(p.root, "config", "libraryfolders.vdf"),
	}
	files := make([]*file, 0, len(lists))
	var listed []string
	for i, path := range lists {
		f, err := readFile(path, steam.VDFMax)
		if err != nil {
			p.o.Log.Printf("prepare: libraries: %v", err)
			return
		}
		if f.missing {
			if i == 0 {
				return // Steam never ran: its first start finds no list to add to
			}
			continue
		}
		paths, err := steam.ParseLibraryFolders(f.data)
		if err != nil {
			p.o.Log.Printf("prepare: libraries: %s: %v", relName(p.root, path), err)
			return
		}
		listed = append(listed, paths...)
		files = append(files, f)
	}
	for _, dir := range dirs {
		if slices.ContainsFunc(listed, func(l string) bool { return within(l, dir) }) {
			continue // in Steam already, perhaps added there by hand
		}
		label, contentID := "", "0"
		if m, err := readFile(filepath.Join(dir, "libraryfolder.vdf"), steam.VDFMax); err == nil && !m.missing {
			label, contentID = steam.ParseLibraryFolder(m.data)
		}
		for _, f := range files {
			out, added, err := steam.AddLibraryFolder(f.data, dir, label, contentID)
			if err != nil {
				p.o.Log.Printf("prepare: libraries: %s: %v", relName(p.root, f.path), err)
				continue
			}
			if !added {
				continue
			}
			if err := f.write(out); err != nil {
				p.o.Log.Printf("prepare: libraries: %v", err)
				continue
			}
			p.wrote(f.path)
		}
		listed = append(listed, dir)
		p.o.Log.Printf("prepare: added %s to Steam's libraries", dir)
	}
}

// within reports whether p is dir or lies below it, with /mnt read as
// /var/mnt (a symlink on VaporOS) as vosd does.
func within(p, dir string) bool {
	p, dir = canonMnt(p), canonMnt(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func canonMnt(p string) string {
	p = filepath.Clean(p)
	if p == "/mnt" || strings.HasPrefix(p, "/mnt/") {
		return "/var" + p
	}
	return p
}
