package extensions

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
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

// appNames is each app's name in the appmanifest of the first library that
// has one for it: Steam's own library, then those libraryfolders.vdf
// lists. vapor owns these files, so they are read through gamerfs, and a
// name is only shown, as text. Apps found nowhere are left out.
func appNames(apps []uint32) map[uint32]string {
	out := map[uint32]string{}
	if len(apps) == 0 {
		return out
	}
	type lib struct{ root, rel string }
	libs := []lib{{config.GamerHome, steamRel}}
	if b, err := gamerfs.ReadFile(config.GamerHome, steamRel+"/steamapps/libraryfolders.vdf", maxSteamMeta); err == nil {
		if paths, err := steam.ParseLibraryFolders(b); err == nil {
			for _, p := range paths {
				if len(libs) < maxLibraries && p != filepath.Join(config.GamerHome, steamRel) {
					libs = append(libs, lib{p, "."})
				}
			}
		}
	}
	for _, app := range apps {
		for _, l := range libs {
			b, err := gamerfs.ReadFile(l.root, path.Join(l.rel, "steamapps", fmt.Sprintf("appmanifest_%d.acf", app)), maxSteamMeta)
			if err != nil {
				continue
			}
			if a, err := steam.ParseManifest(b); err == nil && a.ID == int(app) {
				if name := cleanName(a.Name); name != "" {
					out[app] = name
					break
				}
			}
		}
	}
	return out
}

// cleanName keeps a name Steam wrote short and on one line.
func cleanName(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > maxAppNameLen {
		s = strings.TrimSpace(string(r[:maxAppNameLen]))
	}
	return s
}
