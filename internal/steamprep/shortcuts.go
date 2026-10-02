package steamprep

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// VaporOSTag is the collection every VaporOS shortcut is in.
const VaporOSTag = "VaporOS"

// shortcutPlan is one account's shortcuts.vdf, decided before step 1 so
// the compatibility tools know the shortcuts' app ids, written in step 3.
type shortcutPlan struct {
	acct    uint32
	name    string
	f       *file
	out     []byte                    // the new file, nil when unchanged
	next    map[string]*ShortcutState // the account's records after it
	removed []uint32                  // app ids of shortcuts VaporOS took out
	written bool
}

// shortcutInput is one account's shortcuts and what decides them.
type shortcutInput struct {
	list    []steam.Shortcut
	existed bool // the account had a shortcuts.vdf
	want    []Shortcut
	names   func(owner string) bool // steam.json names the extension anywhere
	st      map[string]*ShortcutState
	unwrap  bool
	// icon is the icon to give a shortcut that has none, "" for none.
	icon func(w Shortcut, appid uint32) string
}

// shortcutResult is the account's shortcuts as they should be, the
// records of VaporOS's ones, the app ids of those it removed, and those
// it kept although steam.json does not list them.
type shortcutResult struct {
	list    []steam.Shortcut
	next    map[string]*ShortcutState
	removed []uint32
	kept    []uint32
}

// decideShortcuts decides an account's shortcuts. A VaporOS shortcut is
// the one whose launch options carry its `--shortcut <owner>/<key>` (or,
// once --unwrap took that off, whose app id the record holds), whatever
// Steam or the user did to the rest. One steam.json does not list stays,
// untouched and recorded, while steam.json names its extension at all: a
// shortcut missing for one run (an extension not mounted on this boot,
// one whose install has no target for it yet) is neither taken out nor
// added again. A record the user deleted always stays.
func decideShortcuts(in shortcutInput) shortcutResult {
	list := in.list
	res := shortcutResult{next: map[string]*ShortcutState{}}
	byRef := map[string]int{}
	claimed := map[int]bool{}
	for i, s := range list {
		if owner, key, ok := steam.ShortcutRef(s.LaunchOptions); ok {
			if _, dup := byRef[owner+"/"+key]; !dup {
				byRef[owner+"/"+key] = i
				claimed[i] = true
			}
		}
	}
	refs := slices.Sorted(maps.Keys(in.st))
	for _, ref := range refs {
		ss := in.st[ref]
		if _, ok := byRef[ref]; ok || ss.Deleted {
			continue
		}
		for i, s := range list {
			if !claimed[i] && s.AppID == ss.AppID && !strings.Contains(s.LaunchOptions, steam.Dispatcher) {
				byRef[ref], claimed[i] = i, true
				break
			}
		}
	}

	if in.unwrap {
		for _, ref := range refs {
			if _, ok := byRef[ref]; !ok {
				c := *in.st[ref]
				res.next[ref] = &c
			}
		}
		for ref, i := range byRef {
			s := &list[i]
			s.LaunchOptions = strings.Replace(s.LaunchOptions, steam.Dispatcher+" --shortcut "+ref+" ", "", 1)
			res.next[ref] = newShortcutState(s.AppID)
		}
		res.list = list
		return res
	}

	wanted := map[string]bool{}
	for _, w := range in.want {
		wanted[w.Ref()] = true
	}
	owned := func(ref string) bool {
		owner, _, _ := strings.Cut(ref, "/")
		return in.names(owner)
	}
	drop := map[int]bool{}
	for ref, i := range byRef {
		switch {
		case wanted[ref]:
		case owned(ref):
			res.next[ref] = newShortcutState(list[i].AppID)
			res.kept = append(res.kept, list[i].AppID)
		default:
			drop[i] = true
			res.removed = append(res.removed, list[i].AppID)
		}
	}
	for _, ref := range refs {
		if _, found := byRef[ref]; found || wanted[ref] {
			continue
		}
		if ss := in.st[ref]; ss.Deleted || owned(ref) {
			c := *ss
			res.next[ref] = &c
		}
	}
	slices.Sort(res.removed)
	slices.Sort(res.kept)

	var added []steam.Shortcut
	for _, w := range in.want {
		ref := w.Ref()
		if i, ok := byRef[ref]; ok {
			s := &list[i]
			s.AppName = w.Name
			s.Exe, s.StartDir = `"`+w.Exe+`"`, `"`+w.StartDir+`"`
			s.LaunchOptions = steam.ShortcutLaunchOptions(w.Owner, w.Key)
			if !slices.Contains(s.Tags, VaporOSTag) {
				s.Tags = append(s.Tags, VaporOSTag)
			}
			if s.AppID == 0 {
				s.AppID = steam.ShortcutAppID(w.Owner, w.Key)
			}
			if s.Icon == "" && in.icon != nil {
				s.Icon = in.icon(w, s.AppID)
			}
			res.next[ref] = newShortcutState(s.AppID)
			continue
		}
		if ss := in.st[ref]; ss != nil && in.existed {
			// VaporOS added it and it is gone: the user removed it.
			c := *ss
			c.Deleted = true
			res.next[ref] = &c
			continue
		}
		s := steam.NewShortcut(steam.ShortcutAppID(w.Owner, w.Key), w.Name, w.Exe, w.StartDir,
			steam.ShortcutLaunchOptions(w.Owner, w.Key))
		s.Tags = []string{VaporOSTag}
		if in.icon != nil {
			s.Icon = in.icon(w, s.AppID)
		}
		added = append(added, s)
		res.next[ref] = newShortcutState(s.AppID)
	}
	res.list = make([]steam.Shortcut, 0, len(list)+len(added))
	for i, s := range list {
		if !drop[i] {
			res.list = append(res.list, s)
		}
	}
	res.list = append(res.list, added...)
	return res
}

// planShortcuts reads every account's shortcuts.vdf and decides it.
func (p *prep) planShortcuts() {
	planned := map[string]bool{}
	for _, acct := range p.accounts {
		path := steam.ShortcutsPath(p.root, acct)
		name := relName(p.root, path)
		if !dirExists(filepath.Dir(path)) {
			continue // the account has not finished signing in
		}
		f, err := readFile(path, 4<<20)
		var list []steam.Shortcut
		if err == nil {
			list, err = steam.ParseShortcuts(f.data)
		}
		if err != nil {
			p.fail(name, err)
			p.shortcutsUnread = true
			continue
		}
		grid := gridDir(p.root, acct)
		res := decideShortcuts(shortcutInput{
			list: list, existed: !f.missing, want: p.want.Shortcuts, names: p.want.names,
			st: p.st.Shortcuts[acctKey(acct)], unwrap: p.o.Unwrap,
			icon: func(w Shortcut, appid uint32) string {
				if w.Art == "" || !regularFile(filepath.Join(w.Art, iconArt)) {
					return ""
				}
				return filepath.Join(grid, gridName(appid, iconSuffix))
			},
		})
		plan := &shortcutPlan{acct: acct, name: name, f: f, next: res.next, removed: res.removed}
		if data, err := steam.MarshalShortcuts(res.list); err != nil {
			p.fail(name, err)
			p.shortcutsUnread = true
			continue
		} else if !bytes.Equal(data, f.data) && !(f.missing && len(res.list) == 0) {
			plan.out = data
		}
		for _, id := range res.kept {
			p.keptShortcuts[id] = true
		}
		planned[acctKey(acct)] = true
		p.plans = append(p.plans, plan)
	}
	for ak, records := range p.st.Shortcuts {
		if len(records) > 0 && !planned[ak] {
			p.shortcutsUnread = true // an account this run cannot see
		}
	}
}

// shortcutTools returns the compatibility tool each VaporOS shortcut runs
// with, by the app ids the plans give them.
func (p *prep) shortcutTools() map[uint32]string {
	tools := map[uint32]string{}
	byRef := map[string]Shortcut{}
	for _, s := range p.want.Shortcuts {
		byRef[s.Ref()] = s
	}
	for _, plan := range p.plans {
		for ref, ss := range plan.next {
			if w, ok := byRef[ref]; ok && !ss.Deleted && w.CompatTool != "" && ss.AppID&0x80000000 != 0 {
				tools[ss.AppID] = w.CompatTool
			}
		}
	}
	return tools
}

// writeShortcuts is step 3.
func (p *prep) writeShortcuts() {
	for _, plan := range p.plans {
		if plan.out != nil {
			if !p.step("writing " + plan.name) {
				return
			}
			if err := plan.f.write(plan.out); err != nil {
				p.fail(plan.name, err)
				continue
			}
			p.o.Log.Printf("prepare: %s: VaporOS shortcuts updated", plan.name)
		}
		plan.written = true
		p.st.Shortcuts[acctKey(plan.acct)] = plan.next
		p.commit()
	}
}

// Art Steam shows for a shortcut: the extension's file, and the suffix
// of its name in userdata/<account>/config/grid/<appid><suffix>.png.
var artFiles = []struct{ src, suffix string }{
	{"capsule.png", "p"}, {"capsule-wide.png", ""}, {"hero.png", "_hero"}, {"logo.png", "_logo"}, {iconArt, iconSuffix},
}

const (
	iconArt    = "icon.png"
	iconSuffix = "_icon"
	maxArt     = 16 << 20
)

func gridDir(root string, acct uint32) string {
	return filepath.Join(root, "userdata", acctKey(acct), "config", "grid")
}

func gridName(appid uint32, suffix string) string {
	return strconv.FormatUint(uint64(appid), 10) + suffix + ".png"
}

// regularFile reports whether path is a regular file (not a symlink).
func regularFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// art is step 4: each VaporOS shortcut's art, where the account has none
// for it yet (the user may have picked their own), and none for the
// shortcuts VaporOS removed.
func (p *prep) art() {
	if p.o.Unwrap {
		return
	}
	byRef := map[string]Shortcut{}
	for _, s := range p.want.Shortcuts {
		byRef[s.Ref()] = s
	}
	for _, plan := range p.plans {
		if !plan.written {
			continue
		}
		grid := gridDir(p.root, plan.acct)
		for _, id := range plan.removed {
			for _, a := range artFiles {
				err := os.Remove(filepath.Join(grid, gridName(id, a.suffix)))
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					p.fail(relName(p.root, grid), err)
				}
			}
		}
		for _, ref := range slices.Sorted(maps.Keys(plan.next)) {
			w, ss := byRef[ref], plan.next[ref]
			if ss.Deleted || w.Art == "" {
				continue
			}
			if !p.step("art") {
				return
			}
			for _, a := range artFiles {
				dst := filepath.Join(grid, gridName(ss.AppID, a.suffix))
				if _, err := os.Lstat(dst); err == nil {
					continue
				}
				data, err := readRegular(filepath.Join(w.Art, a.src), maxArt)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err == nil {
					err = config.WriteFileAtomic(dst, data, 0o644)
				}
				if err != nil {
					p.fail(relName(p.root, dst), err)
				}
			}
		}
	}
}
