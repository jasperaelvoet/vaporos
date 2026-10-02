package steamprep

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
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
	removed []*ShortcutState          // the shortcuts VaporOS took out
	written bool
}

// shortcutInput is one account's shortcuts and what decides them.
type shortcutInput struct {
	list    []steam.Shortcut
	existed bool // the account had a shortcuts.vdf
	want    []Shortcut
	keep    func(owner string) bool // the extension's shortcuts stay unlisted
	st      map[string]*ShortcutState
	unwrap  bool
	// icon is the icon to give a shortcut that has none, "" for none.
	icon func(w Shortcut, appid uint32) string
}

// shortcutResult is the account's shortcuts as they should be, the
// records of VaporOS's ones, those of the ones it removed, and the app
// ids of those it kept although steam.json does not list them.
type shortcutResult struct {
	list    []steam.Shortcut
	next    map[string]*ShortcutState
	removed []*ShortcutState
	kept    []uint32
}

// launchOptions is the shortcut's LaunchOptions: the dispatcher with its
// id, then its args, which Steam passes to Exe after %command%.
func (w Shortcut) launchOptions() string {
	o := steam.ShortcutLaunchOptions(w.Owner, w.Key)
	if len(w.Args) > 0 {
		o += " " + strings.Join(w.Args, " ")
	}
	return o
}

// decideShortcuts decides an account's shortcuts. A VaporOS shortcut is
// the one whose launch options carry its `--shortcut <owner>/<key>` (or,
// once --unwrap took that off, whose app id the record holds), whatever
// Steam or the user did to the rest. One steam.json does not list stays,
// untouched and recorded, while its extension is kept (in.keep): a
// shortcut missing for one run (a boot without extensions, a trial that
// fell back, one whose install has no target for it yet) is neither
// taken out nor added again. A record the user deleted always stays. A
// record keeps the art VaporOS wrote for its shortcut.
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
				res.next[ref] = in.st[ref].clone()
			}
		}
		for ref, i := range byRef {
			s := &list[i]
			s.LaunchOptions = strings.Replace(s.LaunchOptions, steam.Dispatcher+" --shortcut "+ref+" ", "", 1)
			res.next[ref] = carried(in.st[ref], s.AppID)
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
		return in.keep != nil && in.keep(owner)
	}
	drop := map[int]bool{}
	for _, ref := range slices.Sorted(maps.Keys(byRef)) {
		i := byRef[ref]
		switch {
		case wanted[ref]:
		case owned(ref):
			res.next[ref] = carried(in.st[ref], list[i].AppID)
			res.kept = append(res.kept, list[i].AppID)
		default:
			drop[i] = true
			res.removed = append(res.removed, carried(in.st[ref], list[i].AppID))
		}
	}
	for _, ref := range refs {
		if _, found := byRef[ref]; found || wanted[ref] {
			continue
		}
		if ss := in.st[ref]; ss.Deleted || owned(ref) {
			res.next[ref] = ss.clone()
		}
	}
	slices.Sort(res.kept)

	var added []steam.Shortcut
	for _, w := range in.want {
		ref := w.Ref()
		if i, ok := byRef[ref]; ok {
			s := &list[i]
			s.AppName = w.Name
			s.Exe, s.StartDir = `"`+w.Exe+`"`, `"`+w.StartDir+`"`
			s.LaunchOptions = w.launchOptions()
			if !slices.Contains(s.Tags, VaporOSTag) {
				s.Tags = append(s.Tags, VaporOSTag)
			}
			if s.AppID == 0 {
				s.AppID = steam.ShortcutAppID(w.Owner, w.Key)
			}
			if s.Icon == "" && in.icon != nil {
				s.Icon = in.icon(w, s.AppID)
			}
			res.next[ref] = carried(in.st[ref], s.AppID)
			continue
		}
		if ss := in.st[ref]; ss != nil && in.existed {
			// VaporOS added it and it is gone: the user removed it.
			c := ss.clone()
			c.Deleted = true
			res.next[ref] = c
			continue
		}
		s := steam.NewShortcut(steam.ShortcutAppID(w.Owner, w.Key), w.Name, w.Exe, w.StartDir, w.launchOptions())
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
			list: list, existed: !f.missing, want: p.want.Shortcuts, keep: p.keepShortcuts,
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
			p.wrote(plan.f.path)
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

// artNameRe is a grid file VaporOS writes, as its record names it.
var artNameRe = regexp.MustCompile(`^[0-9]{1,10}(p|_hero|_logo|_icon)?\.png$`)

// art is step 4: each VaporOS shortcut's art, where the account has none
// for it yet (the user may have picked their own), recorded with its size
// and mtime; and for the shortcuts VaporOS removed, the files it wrote
// that are still as it wrote them.
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
		for _, rec := range plan.removed {
			p.removeArt(grid, rec)
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
				name := gridName(ss.AppID, a.suffix)
				dst := filepath.Join(grid, name)
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
				var fi fs.FileInfo
				if err == nil {
					fi, err = os.Lstat(dst)
				}
				if err != nil {
					p.fail(relName(p.root, dst), err)
					continue
				}
				p.wrote(dst)
				if ss.Art == nil {
					ss.Art = map[string]ArtFile{}
				}
				ss.Art[name] = ArtFile{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
			}
			p.commit()
		}
	}
}

// removeArt deletes the grid files VaporOS wrote for a shortcut it
// removed, each only while it is as VaporOS wrote it: art the user put in
// its place stays, as does art of a shortcut VaporOS has no record of.
func (p *prep) removeArt(grid string, rec *ShortcutState) {
	for _, name := range slices.Sorted(maps.Keys(rec.Art)) {
		path := filepath.Join(grid, name)
		fi, err := os.Lstat(path)
		if !artNameRe.MatchString(name) || err != nil || !fi.Mode().IsRegular() ||
			(ArtFile{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}) != rec.Art[name] {
			continue
		}
		switch err := os.Remove(path); {
		case err == nil:
			p.wrote(path)
		case !errors.Is(err, fs.ErrNotExist):
			p.fail(relName(p.root, path), err)
		}
	}
}

// keepShortcuts reports whether owner's VaporOS shortcuts stay though
// steam.json does not list them: always on a boot without extensions,
// else while steam.json names or owns their extension.
func (p *prep) keepShortcuts(owner string) bool { return p.extOff || p.want.keeps(owner) }
