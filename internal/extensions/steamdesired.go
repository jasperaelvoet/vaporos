package extensions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// SteamDesired is /var/lib/vos/ext/steam.json: what VaporOS wants in Steam
// for the extensions this boot mounted (docs/CONTRACTS.md "Extensions",
// Steam). vosd writes it; `vos steam prepare` applies it and `vos ext
// launch` reads its hooks, both as the gaming user.
type SteamDesired struct {
	Set               string          `json:"set"`
	Dispatcher        bool            `json:"dispatcher"`
	DefaultCompatTool string          `json:"default_compat_tool"`
	Apps              []SteamApp      `json:"apps"`
	Shortcuts         []SteamShortcut `json:"shortcuts"`
	Release           []SteamRelease  `json:"release"`
	// Owners are the extensions whose Steam entries stay even while this
	// boot does not list them (wanted ∪ core ∪ mounted), sorted; nil
	// when wanted cannot be read, and then prepare removes none.
	Owners []string `json:"owners"`
}

// SteamApp is what extensions set for one Steam app.
type SteamApp struct {
	App        uint32       `json:"app"`
	CompatTool string       `json:"compat_tool"`
	Hooks      []string     `json:"hooks"`
	Beta       *BetaRequest `json:"beta"`
}

// SteamShortcut is a non-Steam game an extension adds.
type SteamShortcut struct {
	Owner      string   `json:"owner"`
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Exe        string   `json:"exe"`
	StartDir   string   `json:"start_dir"`
	Args       []string `json:"args,omitempty"`
	CompatTool string   `json:"compat_tool"`
	Art        string   `json:"art"`
}

// SteamRelease is an app a removed extension forced a compatibility tool
// on: prepare hands its mapping over to the user.
type SteamRelease struct {
	App uint32 `json:"app"`
}

// ShortcutAppID is the app id VaporOS gives a shortcut in shortcuts.vdf:
// crc32 (IEEE) of "<owner>/<key>" with the top bit set, as Steam's own
// shortcut ids have it. Steam may change it; prepare records the one it
// kept.
func ShortcutAppID(owner, key string) uint32 {
	return crc32.ChecksumIEEE([]byte(owner+"/"+key)) | 0x80000000
}

// ShortcutGameID is the 64-bit id Steam starts a shortcut by
// (steam://rungameid/<id>).
func ShortcutGameID(appid uint32) uint64 { return uint64(appid)<<32 | 0x02000000 }

// isShortcutGameID reports whether id is a shortcut's game id.
func isShortcutGameID(id uint64) bool { return id>>32 != 0 && uint32(id) == 0x02000000 }

// steamEntries is what Steam gets from this boot's extensions: the ids
// mounted and still wanted (an extension removed but mounted until the
// restart is dropped at once), in catalog order, with their shipped
// descriptors and the helpers' parts.
type steamEntries struct {
	report  *store.BootReport
	catalog *catalog.Catalog
	desired map[string]bool // wanted ∪ core with their requirements; nil when unknown
	wanted  []string        // wanted as it is, ids the booted catalog lacks included
	descs   map[string]*descriptor.Descriptor
	ids     []string
	parts   map[string]SteamParts
	// owned is steam-owned.json's ids with those wanted or mounted now:
	// the extensions that may have set something in Steam. ownedChanged
	// says the file lacks some of them.
	owned        map[string]bool
	ownedChanged bool
}

// steamOwned is /var/lib/vos/ext/steam-owned.json: every extension that
// was wanted or mounted on this box at some point. Only those may have set
// something in Steam, so only their apps are ever released.
type steamOwned struct {
	IDs []string `json:"ids"`
}

// maxSteamOwned bounds the list: catalogs hold far fewer extensions.
const maxSteamOwned = 256

func loadSteamOwned() map[string]bool {
	var f steamOwned
	if err := config.ReadJSON(config.ExtSteamOwnedPath(), &f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("extensions: %v", err)
	}
	out := map[string]bool{}
	for _, id := range f.IDs {
		if manifest.ValidExtensionID(id) && len(out) < maxSteamOwned {
			out[id] = true
		}
	}
	return out
}

func loadSteamEntries() (*steamEntries, error) {
	rep, err := store.LoadBootReport()
	if err != nil {
		return nil, fmt.Errorf("boot report: %w", err)
	}
	cat, err := catalog.Load(config.ExtCatalogPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	e := &steamEntries{report: rep, catalog: cat, descs: map[string]*descriptor.Descriptor{}, parts: map[string]SteamParts{},
		owned: loadSteamOwned()}
	own := func(id string) {
		if !e.owned[id] && manifest.ValidExtensionID(id) && len(e.owned) < maxSteamOwned {
			e.owned[id], e.ownedChanged = true, true
		}
	}
	if wanted, err := store.Wanted(); err == nil {
		e.wanted = wanted
		e.desired = map[string]bool{}
		for _, id := range cat.Closure(append(wanted, cat.Core()...)) {
			e.desired[id] = true
			own(id)
		}
	} else {
		log.Printf("extensions: steam.json keeps every mounted extension: %v", err)
	}
	for _, m := range rep.Mounted {
		own(m.ID)
	}
	for _, id := range cat.IDs() {
		d, err := Shipped(id)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Printf("extensions: %s: %v", id, err)
			}
			continue
		}
		e.descs[id] = d
		if rep.IsMounted(id) && (e.desired == nil || e.desired[id]) {
			e.ids = append(e.ids, id)
			e.parts[id] = HelperFor(id).Steam(steamExt(id, d))
		}
	}
	return e, nil
}

// steamExt is what a helper works with outside a request: the shipped
// descriptor, the settings (descriptor defaults, overridden by
// settings/<id>.json's keys the descriptor has) and the data areas.
func steamExt(id string, d *descriptor.Descriptor) *Ext {
	x := &Ext{ID: id, Desc: d, Settings: map[string]any{},
		DataDir: filepath.Join(config.ExtDataDir(), id),
		HomeDir: filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, id)}
	for _, s := range d.Settings {
		if s.Default != nil {
			x.Settings[s.Key] = s.Default
		}
	}
	var saved map[string]any
	err := config.ReadJSON(filepath.Join(config.ExtSettingsDir(), id+".json"), &saved)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("extensions: %s settings: %v", id, err)
	}
	for k, v := range saved {
		if _, ok := d.Setting(k); ok {
			x.Settings[k] = v
		}
	}
	return x
}

// toolInstalled reports whether Steam has the compatibility tool: a tool
// Steam cannot find would leave a mapping that starts nothing.
func toolInstalled(tool string) bool {
	if tool == "" || strings.ContainsAny(tool, "/\x00") || tool == "." || tool == ".." {
		return false
	}
	fi, err := os.Stat(filepath.Join(config.CompatToolsDir, tool, "compatibilitytool.vdf"))
	return err == nil && fi.Mode().IsRegular()
}

// desired builds steam.json's content; dispatcher is dispatcherReady's.
func (e *steamEntries) desiredSteam(dispatcher bool) SteamDesired {
	d := SteamDesired{Set: e.report.Set, Dispatcher: dispatcher,
		Apps: []SteamApp{}, Shortcuts: []SteamShortcut{}, Release: []SteamRelease{}}
	apps := map[uint32]*SteamApp{}
	app := func(id uint32) *SteamApp {
		if apps[id] == nil {
			apps[id] = &SteamApp{App: id, Hooks: []string{}}
		}
		return apps[id]
	}
	forced := map[uint32]bool{}
	for _, id := range e.ids {
		desc, parts := e.descs[id], e.parts[id]
		if s := desc.Steam; s != nil {
			if desc.Core && d.DefaultCompatTool == "" && toolInstalled(s.DefaultCompatTool) {
				d.DefaultCompatTool = s.DefaultCompatTool
			}
			tool := ""
			if toolInstalled(s.CompatTool) {
				tool = s.CompatTool
			}
			for _, a := range s.ForceCompatTool {
				app(a).CompatTool = tool
				forced[a] = true
			}
			for _, h := range s.Hooks {
				for _, a := range h.Apps {
					if x := app(a); !slices.Contains(x.Hooks, id) {
						x.Hooks = append(x.Hooks, id)
					}
				}
			}
			for _, sc := range s.Shortcuts {
				t, ok := parts.Shortcuts[sc.Key]
				if !ok {
					continue // the install has not placed it yet
				}
				if !validPath(t.Exe) || !validPath(t.StartDir) {
					log.Printf("extensions: %s/%s: the shortcut's target %q in %q is not a clean absolute path", id, sc.Key, t.Exe, t.StartDir)
					continue
				}
				if !validArgs(t.Args) {
					log.Printf("extensions: %s/%s: the shortcut's arguments %q are not plain words", id, sc.Key, t.Args)
					continue
				}
				out := SteamShortcut{Owner: id, Key: sc.Key, Name: sc.Name, Exe: t.Exe, StartDir: t.StartDir, Args: slices.Clone(t.Args)}
				if sc.CompatTool {
					out.CompatTool = tool
				}
				if sc.Art != "" {
					art := filepath.Join(config.ExtMountedLibDir, id, sc.Art)
					if fi, err := os.Stat(art); err == nil && fi.IsDir() {
						out.Art = art
					}
				}
				d.Shortcuts = append(d.Shortcuts, out)
			}
		}
		for a, beta := range parts.Beta {
			if a == 0 || (beta.Branch != "" && !steamNameRe.MatchString(beta.Branch)) || !steamNameRe.MatchString(beta.Request) {
				log.Printf("extensions: %s: branch %q (request %q) for app %d is not well formed", id, beta.Branch, beta.Request, a)
				continue
			}
			b := beta
			app(a).Beta = &b
		}
	}
	for _, x := range apps {
		d.Apps = append(d.Apps, *x)
	}
	slices.SortFunc(d.Apps, func(a, b SteamApp) int { return cmp.Compare(a.App, b.App) })

	if e.desired != nil {
		release := map[uint32]bool{}
		for _, id := range e.catalog.IDs() {
			if desc := e.descs[id]; !e.desired[id] && e.owned[id] && desc != nil && desc.Steam != nil {
				for _, a := range desc.Steam.ForceCompatTool {
					if !forced[a] {
						release[a] = true
					}
				}
			}
		}
		for a := range release {
			d.Release = append(d.Release, SteamRelease{App: a})
		}
		slices.SortFunc(d.Release, func(a, b SteamRelease) int { return cmp.Compare(a.App, b.App) })
		d.Owners = e.owners()
	}
	return d
}

// owners are the extensions whose shortcuts prepare keeps though this
// boot does not list them: a boot without extensions, a trial that fell
// back, an image whose catalog lacks one or an install with no target
// yet must not take the user's shortcut (with its art and collections)
// away. wanted as it is, so an extension the booted catalog lacks counts.
func (e *steamEntries) owners() []string {
	ids := []string{} // [] says none; null would say unknown
	ids = append(ids, e.wanted...)
	for id := range e.desired {
		ids = append(ids, id)
	}
	for _, m := range e.report.Mounted {
		if manifest.ValidExtensionID(m.ID) {
			ids = append(ids, m.ID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// steamNameRe is a branch or a branch request's id, as prepare accepts
// them.
var steamNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validPath accepts a clean absolute path without control characters:
// prepare writes it into shortcuts.vdf, whose strings end at a NUL.
func validPath(p string) bool {
	return len(p) < 4096 && filepath.IsAbs(p) && filepath.Clean(p) == p &&
		!strings.ContainsFunc(p, unicode.IsControl)
}

// shortcutArgRe is one word of a shortcut's arguments: Steam splits launch
// options at spaces and reads quotes, so only words without either go in.
var shortcutArgRe = regexp.MustCompile(`^[A-Za-z0-9._/:=+-]{1,128}$`)

// validArgs accepts up to 16 plain words.
func validArgs(args []string) bool {
	if len(args) > 16 {
		return false
	}
	for _, a := range args {
		if !shortcutArgRe.MatchString(a) {
			return false
		}
	}
	return true
}

// slotEntry is the boot entry of slot (a variable for tests).
var slotEntry = func(slot string) (*boot.Entry, error) {
	if err := boot.EnsureESP(config.ESP); err != nil {
		return nil, err
	}
	return boot.EntryForSlot(config.ESP, slot)
}

// dispatcherReady reports whether Steam launch options may point at `vos
// ext launch`: whatever boots must have it. The booted catalog says so
// for this image. For the other slot: with no boot entry there is nothing
// to roll back to; otherwise only an image built with extensions has the
// dispatcher, and the updater records each image it writes in
// slots/<slot>.json, so its slot file must be for the entry's version and
// list extensions. current is what steam.json says now, which one ESP
// read that fails does not change. Under steamMu.
func (s *Service) dispatcherReady(cat *catalog.Catalog, current bool) bool {
	if cat == nil || cat.Dispatcher < catalog.Dispatcher {
		return false
	}
	slot := config.BootedSlot()
	if slot != "a" && slot != "b" {
		return false
	}
	return s.otherSlotReady(config.OtherSlot(slot), current)
}

// slotCheck is the last reading of the other slot's boot entry.
type slotCheck struct {
	slot  string
	key   string    // what the updater writes around every change of it
	at    time.Time // when the ESP was read
	ready bool
	known bool
	errs  int // ESP reads that failed in a row
}

// slotRecheck is how long a reading of the ESP stands while neither the
// other slot's file nor update-state.json changes. A variable for tests.
var slotRecheck = 30 * time.Minute

// otherSlotReady is dispatcherReady's other slot. The minute's check must
// not read the ESP each time: every stage writes slots/<slot>.json and
// update-state.json around its change of the entries (also from a
// shell), so the entry is read again only when either file changed, after
// slotRecheck, or when the slots may have changed (forgetSlots). A read
// that fails keeps current; only a second one in a row makes it false.
func (s *Service) otherSlotReady(other string, current bool) bool {
	c := &s.slots
	key := statKey(filepath.Join(config.ExtSlotsDir(), other+".json")) + statKey(config.UpdateStatePath())
	if c.known && c.slot == other && c.key == key && now().Sub(c.at) < slotRecheck {
		return c.ready
	}
	e, err := slotEntry(other)
	if err != nil {
		c.errs++
		log.Printf("extensions: slot %s's boot entry (%d in a row): %v", other, c.errs, err)
		return c.errs < 2 && current
	}
	ready := e == nil
	if e != nil {
		sl, err := store.ReadSlot(other)
		ready = err == nil && sl != nil && sl.Version == e.Version && len(sl.Extensions) > 0
	}
	*c = slotCheck{slot: other, key: key, at: now(), ready: ready, known: true}
	return ready
}

// forgetSlots makes the next check read the ESP again. Under steamMu.
func (s *Service) forgetSlots() { s.slots.known = false }

// statKey is a file's size and mtime, "-" when it cannot be read.
func statKey(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "- "
	}
	return fmt.Sprintf("%d:%d ", fi.Size(), fi.ModTime().UnixNano())
}

// SetSteamRestarter sets what asks for a Steam restart when steam.json or
// the accounts prepare has seen change under a running Steam
// (display.Manager.RestartSteam).
func (s *Service) SetSteamRestarter(f func(reason string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steamRestart = f
}

func (s *Service) restartSteam(reason string) {
	s.mu.Lock()
	f := s.steamRestart
	s.mu.Unlock()
	if f != nil {
		f(reason)
	}
}

// SyncSteam writes /var/lib/vos/ext/steam.json (0644, atomically) when its
// content changed, and then asks for a Steam restart unless only owners
// changed: Run calls it at start and after every reconcile, WatchSteam
// when the slots change and once a minute, the control center after an
// extension is added or removed or its settings change. It reports
// whether it wrote the file.
func (s *Service) SyncSteam() (bool, error) {
	if config.IsLive() {
		return false, nil
	}
	s.steamMu.Lock()
	defer s.steamMu.Unlock()
	e, err := loadSteamEntries()
	if err != nil {
		return false, err
	}
	if e.ownedChanged {
		// First: steam.json releases apps only for ids this file keeps.
		ids := slices.Sorted(maps.Keys(e.owned))
		if err := config.WriteJSONAtomic(config.ExtSteamOwnedPath(), steamOwned{IDs: ids}, 0o644); err != nil {
			return false, err
		}
	}
	old, oldErr := os.ReadFile(config.ExtSteamPath())
	var was *SteamDesired
	if oldErr == nil {
		was = &SteamDesired{}
		if json.Unmarshal(old, was) != nil {
			was = nil
		}
	}
	d := e.desiredSteam(s.dispatcherReady(e.catalog, was != nil && was.Dispatcher))
	b, err := marshalSteamDesired(d)
	if err != nil {
		return false, err
	}
	if oldErr == nil && bytes.Equal(old, b) {
		return false, nil
	}
	if err := config.WriteFileAtomic(config.ExtSteamPath(), b, 0o644); err != nil {
		return false, err
	}
	log.Printf("extensions: wrote %s", config.ExtSteamPath())
	if was == nil || !sameButOwners(*was, d) {
		s.restartSteam("what the extensions set in Steam changed")
	}
	if was != nil && was.Dispatcher && !d.Dispatcher {
		u := unwrap{gamescopeState, prepareAsGamer, settleEvery, settleFor}
		go u.run()
	}
	return true, nil
}

// sameButOwners reports whether a and b differ in owners at most. Owners
// only decide which shortcuts prepare takes away, which the next Steam
// start does anyway, so adding an extension that sets nothing in Steam
// (CoolerControl) does not restart it. One removed with shortcuts also
// drops its entries, which still does.
func sameButOwners(a, b SteamDesired) bool {
	a.Owners, b.Owners = nil, nil
	x, errA := marshalSteamDesired(a)
	y, errB := marshalSteamDesired(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// marshalSteamDesired is steam.json's bytes as vosd writes them, which
// `vos steam prepare` parses (internal/steamprep; both sides test the same
// golden file, internal/steamprep/testdata/steam.json).
func marshalSteamDesired(d SteamDesired) ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (s *Service) syncSteam() {
	if _, err := s.SyncSteam(); err != nil {
		log.Printf("extensions: steam.json: %v", err)
	}
}

// readSteamDesired reads steam.json (vosd's own, 0644, so `vos ext launch`
// reads it directly).
func readSteamDesired() (*SteamDesired, error) {
	d := &SteamDesired{}
	if err := config.ReadJSON(config.ExtSteamPath(), d); err != nil {
		return nil, err
	}
	return d, nil
}
