package extensions

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// steamDoc is what d changes in Steam, for its card; nil when it changes
// none of these (Steam's default tool is the core extension's own story).
func steamDoc(d *descriptor.Descriptor, names map[uint32]string) *SteamDoc {
	st := d.Steam
	if st == nil || (st.CompatTool == "" && len(st.ForceCompatTool) == 0 && len(st.Hooks) == 0 && len(st.Shortcuts) == 0) {
		return nil
	}
	out := &SteamDoc{CompatTool: st.CompatTool, Forces: []SteamAppDoc{}, Hooks: []SteamAppDoc{}, Shortcuts: []ShortcutDoc{}}
	for _, a := range st.ForceCompatTool {
		out.Forces = append(out.Forces, SteamAppDoc{App: a, Name: names[a]})
	}
	var hooked []uint32
	for _, h := range st.Hooks {
		for _, a := range h.Apps {
			if !slices.Contains(hooked, a) {
				hooked = append(hooked, a)
				out.Hooks = append(out.Hooks, SteamAppDoc{App: a, Name: names[a]})
			}
		}
	}
	for _, sc := range st.Shortcuts {
		out.Shortcuts = append(out.Shortcuts, ShortcutDoc{Name: sc.Name})
	}
	return out
}

// hooksOffLine is the card's line of an extension with app launch hooks
// while steam.json's dispatcher is false: no app's launch options carry
// `vos ext launch`, so its hooks do not run until every VaporOS the box
// can boot has it, which the next update brings.
func hooksOffLine(name string) StatusLine {
	return StatusLine{Text: "Starting games with " + name + " works after the next VaporOS update. Until then, they start without it.",
		Tone: "warning"}
}

// hooksApps reports whether id hooks an app's launch: its descriptor's
// steam.hooks names one, or steam.json lists it in an app's hooks.
func hooksApps(id string, d *descriptor.Descriptor, sd *SteamDesired) bool {
	if d != nil && d.Steam != nil && slices.ContainsFunc(d.Steam.Hooks, func(h descriptor.Hook) bool { return len(h.Apps) > 0 }) {
		return true
	}
	return sd != nil && slices.ContainsFunc(sd.Apps, func(a SteamApp) bool { return slices.Contains(a.Hooks, id) })
}

// steamApps lists the apps the cards' descriptors force a tool on or hook.
func (s *Service) steamApps(xs []ExtensionStatus) []uint32 {
	var apps []uint32
	for _, x := range xs {
		d := s.desc(x.ID)
		if d == nil || d.Steam == nil {
			continue
		}
		apps = append(apps, d.Steam.ForceCompatTool...)
		for _, h := range d.Steam.Hooks {
			apps = append(apps, h.Apps...)
		}
	}
	slices.Sort(apps)
	return slices.Compact(apps)
}

// Bounds on what appNames reads of vapor's Steam: a real appmanifest or
// libraryfolders.vdf is a few KiB.
const (
	maxSteamMeta  = 1 << 20
	maxLibraries  = 16
	maxAppNameLen = 128
)

// steamRel is where vosd reads Steam, in vapor's home (as `vos steam
// prepare` requires).
const steamRel = ".local/share/Steam"

// gameDrivesDir is where the game drives' folders (GameDrives/<name>) are
// mounted; a variable for tests.
var gameDrivesDir = GameDrives

// steamLib is a library appNames reads: a directory rel beneath root, a
// gamerfs root vosd trusts.
type steamLib struct{ root, rel string }

// manifestName is what appNames last read of one appmanifest.
type manifestName struct {
	mtime time.Time
	size  int64
	name  string // "" when it named another app, or none
}

// steamNames caches appNames' reads, by library and file.
type steamNames struct {
	mu   sync.Mutex
	read map[steamLib]manifestName
}

// steamLibs are where appNames looks, in order: Steam's own library in
// vapor's home, then each game drive config.json adopted (at its top, in
// its SteamLibrary, and wherever on it libraryfolders.vdf lists one). The
// roots are vapor's home and the drives' folders, never a path vapor
// wrote: libraryfolders.vdf only picks a directory on a drive.
func (s *Service) steamLibs() []steamLib {
	libs := []steamLib{{config.GamerHome, steamRel}}
	var listed []string
	if b, err := gamerfs.ReadFile(config.GamerHome, steamRel+"/steamapps/libraryfolders.vdf", maxSteamMeta); err == nil {
		listed, _ = steam.ParseLibraryFolders(b)
	}
	for _, l := range s.cfg.Snapshot().Storage.Libraries {
		drive := l.Mountpoint
		name, ok := strings.CutPrefix(drive, GameDrives+"/")
		if !ok || !drivePath(drive) {
			continue
		}
		rels := []string{".", "SteamLibrary"}
		for _, p := range listed {
			p = path.Clean(p)
			if strings.HasPrefix(p, "/mnt/") {
				p = "/var" + p // /mnt is a link to /var/mnt
			}
			if rel, ok := strings.CutPrefix(p, drive+"/"); ok && filepath.IsLocal(rel) {
				rels = append(rels, rel)
			}
		}
		for _, rel := range rels {
			lib := steamLib{filepath.Join(gameDrivesDir, name), rel}
			if len(libs) < maxLibraries && !slices.Contains(libs, lib) {
				libs = append(libs, lib)
			}
		}
	}
	return libs
}

// appNames is each app's name in the appmanifest of the first library that
// has one for it (steamLibs). vapor owns these files, so they are read
// through gamerfs, and a name is only shown, as text. A manifest is read
// again only once its mtime or size changed. Apps found nowhere are left
// out.
func (s *Service) appNames(apps []uint32) map[uint32]string {
	out := map[uint32]string{}
	if len(apps) == 0 {
		return out
	}
	libs := s.steamLibs()
	c := &s.cc.names
	c.mu.Lock()
	defer c.mu.Unlock()
	looked := map[steamLib]manifestName{}
	for _, app := range apps {
		for _, l := range libs {
			f := steamLib{l.root, path.Join(l.rel, "steamapps", fmt.Sprintf("appmanifest_%d.acf", app))}
			m, ok := readManifestName(f, app, c.read[f])
			if !ok {
				continue
			}
			looked[f] = m
			if m.name != "" {
				out[app] = m.name
				break
			}
		}
	}
	c.read = looked // what was not looked at this time goes
	return out
}

// readManifestName reads app's name from the appmanifest f, unless last
// (what the previous read found) has its mtime and size. It reports false
// when there is no such file.
func readManifestName(f steamLib, app uint32, last manifestName) (manifestName, bool) {
	fh, err := gamerfs.Open(f.root, f.rel)
	if err != nil {
		return manifestName{}, false
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return manifestName{}, false
	}
	if !last.mtime.IsZero() && last.mtime.Equal(fi.ModTime()) && last.size == fi.Size() {
		return last, true
	}
	m := manifestName{mtime: fi.ModTime(), size: fi.Size()}
	if b, err := io.ReadAll(io.LimitReader(fh, maxSteamMeta+1)); err == nil && len(b) <= maxSteamMeta {
		if a, err := steam.ParseManifest(b); err == nil && a.ID == int(app) {
			m.name = cleanName(a.Name)
		}
	}
	return m, true
}

// cleanName keeps a name Steam wrote short and on one line.
func cleanName(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > maxAppNameLen {
		s = strings.TrimSpace(string(r[:maxAppNameLen]))
	}
	return s
}
