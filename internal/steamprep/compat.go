package steamprep

import (
	"bytes"
	"slices"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// Steam's priorities: the default for every Windows game, and a choice
// made for one app.
const (
	defaultPriority = "75"
	appPriority     = "250"
)

// mapInput is one CompatToolMapping entry to decide.
type mapInput struct {
	key      uint32            // 0: the default
	cur      *steam.CompatTool // the entry in config.vdf, nil when none
	m        Mapping           // VaporOS's record of it
	want     string            // the tool steam.json asks for, "" for none
	toolOK   func(string) bool
	release  bool // a removed extension hands the app to the user
	shortcut bool // a VaporOS shortcut's app id
	unwrap   bool
}

// mapAction is what to do to the entry: nothing, set it, or delete it.
type mapAction struct {
	set *steam.CompatTool
	del bool
}

func (a mapAction) none() bool { return a.set == nil && !a.del }

// decideMapping applies the rules of "Steam" in docs/CONTRACTS.md to one
// entry: the default only where it is missing or still VaporOS's (the
// user's choice wins), a forced app's always, nothing that maps to a tool
// that is not installed (the entry from before comes back meanwhile and
// VaporOS keeps it, suspended, until the tool does), released apps
// handed over as they are.
func decideMapping(in mapInput) (mapAction, Mapping) {
	m := in.m
	owned := m.Wrote != ""
	held := owned && !m.Suspended && in.cur != nil && in.cur.Name == m.Wrote
	// restore puts the entry from before back, where VaporOS's value is
	// still there. For a tool that is missing, a before that names it
	// counts as none: Steam could not run that either.
	restore := func(forMissing bool) mapAction {
		if !held {
			return mapAction{}
		}
		if forMissing && m.Before != nil && !in.toolOK(m.Before.Name) &&
			(m.Before.Name == m.Wrote || m.Before.Name == in.want) {
			return mapAction{del: true}
		}
		return restoreTo(m.Before)
	}
	switch {
	case in.unwrap:
		switch {
		case !owned || m.Suspended:
			return mapAction{}, m
		case !held && in.cur != nil && in.key == 0:
			return mapAction{}, Mapping{} // the user's own choice
		}
		m.Suspended = true
		return restore(false), m

	case in.release && owned && in.want == "":
		if !in.toolOK(m.Wrote) {
			return restore(true), Mapping{}
		}
		return mapAction{}, Mapping{}

	case owned && m.Suspended:
		if in.want != "" && in.toolOK(in.want) {
			if in.cur != nil && in.cur.Name == in.want {
				return mapAction{}, Mapping{Wrote: in.want}
			}
			// What is there now is Steam's: whatever changed while the
			// tool was missing is what comes back later.
			return setTo(in.key, in.want), Mapping{Wrote: in.want, Before: clone(in.cur)}
		}
		if in.want == "" && in.shortcut {
			return mapAction{}, Mapping{}
		}
		return mapAction{}, m

	case owned:
		if !held && (in.cur != nil && (in.key == 0 || in.want == "") || in.cur == nil && in.want == "") {
			return mapAction{}, Mapping{} // changed or removed by the user
		}
		if in.want == "" {
			switch {
			case in.shortcut:
				return restore(false), Mapping{}
			case !in.toolOK(m.Wrote):
				m.Suspended = true
				return restore(true), m
			}
			return mapAction{}, m // not asked for any more: kept as it is
		}
		if !in.toolOK(in.want) {
			m.Suspended = true
			return restore(true), m
		}
		if held && in.cur.Name == in.want {
			return mapAction{}, m
		}
		m.Wrote = in.want
		return setTo(in.key, in.want), m
	}

	if in.want == "" || !in.toolOK(in.want) {
		return mapAction{}, Mapping{}
	}
	if in.cur != nil && in.cur.Name == in.want {
		// Already so (VaporOS's own value from a run that stopped before
		// it was recorded, or the user's same choice): VaporOS owns it,
		// with no entry of its own to put back.
		return mapAction{}, Mapping{Wrote: in.want}
	}
	if in.key == 0 && in.cur != nil {
		return mapAction{}, Mapping{} // the user's choice wins
	}
	return setTo(in.key, in.want), Mapping{Wrote: in.want, Before: clone(in.cur)}
}

func setTo(key uint32, tool string) mapAction {
	prio := appPriority
	if key == 0 {
		prio = defaultPriority
	}
	return mapAction{set: &steam.CompatTool{Name: tool, Priority: prio}}
}

func restoreTo(before *steam.CompatTool) mapAction {
	if before == nil {
		return mapAction{del: true}
	}
	return mapAction{set: clone(before)}
}

func clone(t *steam.CompatTool) *steam.CompatTool {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// compatTools is step 1: config.vdf's CompatToolMapping. Steam creates
// config.vdf on its first start; VaporOS never does, but sets the default
// from the first start where it exists, before anyone signs in.
func (p *prep) compatTools() {
	f, err := readFile(steam.ConfigVDFPath(p.root), steam.VDFMax)
	if err == nil && f.missing {
		return
	}
	if err == nil {
		_, err = steam.ParseVDF(f.data)
	}
	if err != nil {
		p.fail("config.vdf", err)
		return
	}
	data := f.data
	type result struct {
		key uint32
		m   Mapping
	}
	var results []result
	for _, in := range p.mapInputs() {
		cur, ok, err := steam.CompatToolMapping(data, in.key)
		if err != nil {
			p.fail("config.vdf", err)
			return
		}
		if ok {
			in.cur = &cur
		}
		act, m := decideMapping(in)
		switch {
		case act.del:
			data, _, err = steam.DeleteCompatToolMapping(data, in.key)
		case act.set != nil:
			data, _, err = steam.SetCompatToolMapping(data, in.key, *act.set)
		}
		if err != nil {
			p.fail("config.vdf", err)
			return
		}
		if !act.none() {
			p.o.Log.Printf("prepare: config.vdf: compatibility tool of %s: %s", appName(in.key), describe(act))
		}
		results = append(results, result{in.key, m})
	}
	if !bytes.Equal(data, f.data) {
		if !p.step("writing config.vdf") {
			return
		}
		if err := f.write(data); err != nil {
			p.fail("config.vdf", err)
			return
		}
	}
	for _, r := range results {
		if r.key == 0 {
			p.st.Default = r.m
			continue
		}
		if r.m.owned() {
			m := r.m
			p.st.app(r.key).Mapping = &m
		} else if a := p.st.peekApp(r.key); a != nil {
			a.Mapping = nil
		}
	}
	p.commit()
}

// mapInputs lists every entry to decide: the default, the apps and
// shortcuts that ask for a tool, released apps, and every entry VaporOS
// owns.
func (p *prep) mapInputs() []mapInput {
	unwrap := p.o.Unwrap
	release := p.want.releases()
	ins := map[uint32]*mapInput{}
	add := func(key uint32) *mapInput {
		if in := ins[key]; in != nil {
			return in
		}
		in := &mapInput{key: key, toolOK: p.toolOK, unwrap: unwrap, release: release[key] && key != 0,
			shortcut: key&0x80000000 != 0}
		if key == 0 {
			in.m = p.st.Default
		} else if a := p.st.peekApp(key); a != nil && a.Mapping != nil {
			in.m = *a.Mapping
		}
		ins[key] = in
		return in
	}
	add(0)
	for k, a := range p.st.Apps {
		if a.Mapping.owned() {
			if id, err := parseAppID(k); err == nil {
				add(id)
			}
		}
	}
	for id := range release {
		if !p.isTool(id) {
			add(id)
		}
	}
	if !unwrap {
		add(0).want = p.want.DefaultCompatTool
		for _, a := range p.want.Apps {
			if a.CompatTool != "" && !p.isTool(a.App) && a.App&0x80000000 == 0 {
				add(a.App).want = a.CompatTool
			}
		}
		for appid, tool := range p.shortcutTools() {
			add(appid).want = tool
		}
	}
	keys := make([]uint32, 0, len(ins))
	for k := range ins {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]mapInput, 0, len(keys))
	for _, k := range keys {
		in := ins[k]
		if in.shortcut && in.want == "" && !unwrap {
			if p.shortcutsUnread {
				continue // its shortcut may still be there
			}
			if p.keptShortcuts[k] {
				// Its shortcut stays though steam.json does not list it:
				// so does the entry, like an app's no longer asked for.
				in.shortcut = false
			}
		}
		out = append(out, *in)
	}
	return out
}

func appName(key uint32) string {
	switch {
	case key == 0:
		return "every Windows game"
	case key&0x80000000 != 0:
		return "shortcut " + acctKey(key)
	}
	return "app " + acctKey(key)
}

func describe(a mapAction) string {
	if a.del {
		return "removed"
	}
	if a.set.Name == "" {
		return "set to none"
	}
	return a.set.Name
}
