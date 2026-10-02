package steamprep

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
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

// decideShortcuts returns an account's shortcuts as they should be, the
// records of VaporOS's ones, and the app ids of those it removed. A
// VaporOS shortcut is the one whose launch options carry its
// `--shortcut <owner>/<key>` (or, once --unwrap took that off, whose app
// id the record holds), whatever Steam or the user did to the rest.
func decideShortcuts(list []steam.Shortcut, existed bool, want []Shortcut, st map[string]*ShortcutState, unwrap bool) ([]steam.Shortcut, map[string]*ShortcutState, []uint32) {
	next := map[string]*ShortcutState{}
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
	refs := make([]string, 0, len(st))
	for ref := range st {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		ss := st[ref]
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

	if unwrap {
		for _, ref := range refs {
			if _, ok := byRef[ref]; !ok {
				c := *st[ref]
				next[ref] = &c
			}
		}
		for ref, i := range byRef {
			s := &list[i]
			s.LaunchOptions = strings.Replace(s.LaunchOptions, steam.Dispatcher+" --shortcut "+ref+" ", "", 1)
			next[ref] = newShortcutState(s.AppID)
		}
		return list, next, nil
	}

	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w.Ref()] = true
	}
	drop := map[int]bool{}
	var removed []uint32
	for ref, i := range byRef {
		if !wanted[ref] {
			drop[i] = true
			removed = append(removed, list[i].AppID)
		}
	}
	slices.Sort(removed)
	var added []steam.Shortcut
	for _, w := range want {
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
			next[ref] = newShortcutState(s.AppID)
			continue
		}
		if ss := st[ref]; ss != nil && existed {
			// VaporOS added it and it is gone: the user removed it.
			c := *ss
			c.Deleted = true
			next[ref] = &c
			continue
		}
		s := steam.NewShortcut(steam.ShortcutAppID(w.Owner, w.Key), w.Name, w.Exe, w.StartDir,
			steam.ShortcutLaunchOptions(w.Owner, w.Key))
		s.Tags = []string{VaporOSTag}
		added = append(added, s)
		next[ref] = newShortcutState(s.AppID)
	}
	out := make([]steam.Shortcut, 0, len(list)+len(added))
	for i, s := range list {
		if !drop[i] {
			out = append(out, s)
		}
	}
	return append(out, added...), next, removed
}

// planShortcuts reads every account's shortcuts.vdf and decides it.
func (p *prep) planShortcuts() {
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
		st := p.st.Shortcuts[acctKey(acct)]
		out, next, removed := decideShortcuts(list, !f.missing, p.want.Shortcuts, st, p.o.Unwrap)
		plan := &shortcutPlan{acct: acct, name: name, f: f, next: next, removed: removed}
		if data, err := steam.MarshalShortcuts(out); err != nil {
			p.fail(name, err)
			p.shortcutsUnread = true
			continue
		} else if !bytes.Equal(data, f.data) && !(f.missing && len(out) == 0) {
			plan.out = data
		}
		p.plans = append(p.plans, plan)
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
	{"capsule.png", "p"}, {"hero.png", "_hero"}, {"logo.png", "_logo"}, {"icon.png", "_icon"},
}

const maxArt = 16 << 20

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
		grid := filepath.Join(p.root, "userdata", acctKey(plan.acct), "config", "grid")
		for _, id := range plan.removed {
			for _, a := range artFiles {
				err := os.Remove(filepath.Join(grid, strconv.FormatUint(uint64(id), 10)+a.suffix+".png"))
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					p.fail(relName(p.root, grid), err)
				}
			}
		}
		refs := make([]string, 0, len(plan.next))
		for ref := range plan.next {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			w, ss := byRef[ref], plan.next[ref]
			if ss.Deleted || w.Art == "" {
				continue
			}
			if !p.step("art") {
				return
			}
			for _, a := range artFiles {
				dst := filepath.Join(grid, strconv.FormatUint(uint64(ss.AppID), 10)+a.suffix+".png")
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
