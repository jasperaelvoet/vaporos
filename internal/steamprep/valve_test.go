package steamprep

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// Games of the tests, with what Valve's Deck profile recommends.
const (
	starfield = 1716740 // proton-experimental
	forza     = 1551360 // proton-stable
	elden     = 1245620 // proton-stable
	// The fixture's config.vdf has the user's own entries for these two,
	// a Proton and "none": both stay.
	cyberpunk = 1091500 // proton-experimental
	forzaSix  = 2483190 // proton-stable
	fever     = 3493540 // none: the default reaches it
	bus       = 491540  // pinned to proton-7.0-6
	ffxiv     = 39210   // proton-stable, but in the Steam Play manifest
	unowned   = 555     // proton-stable, not installed
)

// valveApp is one app in the fake appinfo.vdf.
type valveApp struct {
	typ, runtime string
}

func defaultPicks() map[uint32]valveApp {
	return map[uint32]valveApp{
		starfield: {"Game", "proton-experimental"},
		forza:     {"Game", "proton-stable"},
		cyberpunk: {"Game", "proton-experimental"},
		forzaSix:  {"Game", "proton-stable"},
		elden:     {"game", "Proton-Stable"},
		fever:     {"Game", ""},
		bus:       {"Game", "proton-7.0-6"},
		ffxiv:     {"Game", "proton-stable"},
		unowned:   {"Game", "proton-stable"},
		ets2:      {"Game", "native"},
		1493710:   {"Tool", "proton-stable"},
	}
}

// writeAppInfo writes Steam's appinfo cache (version 29) with apps and,
// unless noManifest, the Steam Play manifest mapping ffxiv.
func (b *box) writeAppInfo(apps map[uint32]valveApp, noManifest bool) {
	b.t.Helper()
	var keys []string
	index := map[string]uint32{}
	le := binary.LittleEndian
	key := func(w *bytes.Buffer, typ byte, k string) {
		i, ok := index[k]
		if !ok {
			i = uint32(len(keys))
			index[k] = i
			keys = append(keys, k)
		}
		w.WriteByte(typ)
		w.Write(le.AppendUint32(nil, i))
	}
	str := func(w *bytes.Buffer, k, v string) {
		key(w, 0x01, k)
		w.WriteString(v)
		w.WriteByte(0)
	}
	var body bytes.Buffer
	entry := func(app uint32, kv []byte) {
		body.Write(le.AppendUint32(nil, app))
		body.Write(le.AppendUint32(nil, uint32(60+len(kv))))
		body.Write(make([]byte, 60))
		body.Write(kv)
	}
	ids := make([]uint32, 0, len(apps))
	for id := range apps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		a := apps[id]
		var w bytes.Buffer
		key(&w, 0x00, "appinfo")
		key(&w, 0x02, "appid")
		w.Write(le.AppendUint32(nil, id))
		key(&w, 0x00, "common")
		str(&w, "name", fmt.Sprint("app ", id))
		str(&w, "type", a.typ)
		if a.runtime != "" {
			key(&w, 0x00, "steam_deck_compatibility")
			key(&w, 0x00, "configuration")
			str(&w, "recommended_runtime", a.runtime)
			w.Write([]byte{0x08, 0x08})
		}
		w.Write([]byte{0x08, 0x08, 0x08})
		entry(id, w.Bytes())
	}
	if !noManifest {
		var w bytes.Buffer
		key(&w, 0x00, "appinfo")
		key(&w, 0x00, "extended")
		key(&w, 0x00, "app_mappings")
		key(&w, 0x00, "0")
		key(&w, 0x02, "appid")
		w.Write(le.AppendUint32(nil, ffxiv))
		str(&w, "tool", "proton-11.0-beta")
		w.Write([]byte{0x08, 0x08, 0x08, 0x08, 0x08})
		entry(steamPlayManifests, w.Bytes())
	}
	body.Write(make([]byte, 4))

	var out bytes.Buffer
	out.Write(le.AppendUint32(nil, 0x07564429))
	out.Write(le.AppendUint32(nil, 1))
	out.Write(le.AppendUint64(nil, uint64(16+body.Len())))
	out.Write(body.Bytes())
	out.Write(le.AppendUint32(nil, uint32(len(keys))))
	for _, k := range keys {
		out.WriteString(k)
		out.WriteByte(0)
	}
	b.write(steam.AppInfoPath(b.root), out.Bytes())
}

func (b *box) installGame(id uint32) {
	b.t.Helper()
	b.write(filepath.Join(b.root, "steamapps", fmt.Sprintf("appmanifest_%d.acf", id)),
		fmt.Appendf(nil, "\"AppState\"\n{\n\t\"appid\"\t\t\"%d\"\n\t\"name\"\t\t\"app %d\"\n\t\"StateFlags\"\t\t\"4\"\n}\n", id, id))
}

func (b *box) uninstallGame(id uint32) {
	b.t.Helper()
	b.check(os.Remove(filepath.Join(b.root, "steamapps", fmt.Sprintf("appmanifest_%d.acf", id))))
}

func (b *box) setMapping(app uint32, t steam.CompatTool) {
	b.t.Helper()
	b.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.SetCompatToolMapping(d, app, t)
		b.check(err)
		return out
	})
}

func (b *box) deleteMapping(app uint32) {
	b.t.Helper()
	b.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.DeleteCompatToolMapping(d, app)
		b.check(err)
		return out
	})
}

// gameBox has the games installed and Valve's picks in Steam's cache.
func gameBox(t *testing.T) *box {
	b := newBox(t)
	b.desire(proton())
	for _, id := range []uint32{starfield, forza, elden, cyberpunk, forzaSix, fever, bus, ffxiv, 1493710} {
		b.installGame(id)
	}
	b.writeAppInfo(defaultPicks(), false)
	return b
}

// wantCopies checks that exactly copies have VaporOS's copy of the
// default, in config.vdf and in the record.
func (b *box) wantCopies(copies ...uint32) {
	b.t.Helper()
	st := b.state()
	for _, id := range []uint32{starfield, forza, elden, cyberpunk, forzaSix, fever, bus, ffxiv, unowned, ets2, 1493710} {
		got, _ := b.mapping(id)
		a := st.peekApp(id)
		has := got == oursApp && a != nil && a.Mapping.owned() && a.Mapping.FollowsDefault && !a.Mapping.Suspended
		if want := slices.Contains(copies, id); has != want {
			b.t.Errorf("app %d: copy of the default %v, want %v (%+v)", id, has, want, got)
		}
	}
}

func TestGamesFollowTheDefault(t *testing.T) {
	b := gameBox(t)
	b.run(false)
	if got, _ := b.mapping(0); got != ours {
		t.Fatalf("default %+v", got)
	}
	// Valve's generic Proton picks get a copy of the default; native
	// games, pins, the Steam Play manifest's mappings, tools, and games
	// the default reaches anyway do not.
	b.wantCopies(starfield, forza, elden)
	for _, id := range []uint32{fever, bus, ffxiv, unowned, ets2, 1493710} {
		if got, ok := b.mapping(id); ok {
			t.Errorf("app %d mapped: %+v", id, got)
		}
	}
	if m := b.state().peekApp(starfield).Mapping; !m.FollowsDefault || m.Before != nil || m.Declined {
		t.Errorf("record %+v", m)
	}
	// What the user set for a game before stays theirs, a forced "none"
	// too.
	if got, _ := b.mapping(cyberpunk); got.Name != "proton_9" {
		t.Errorf("user's Proton replaced: %+v", got)
	}
	if got, ok := b.mapping(forzaSix); !ok || got.Name != "" || got.Priority != "250" {
		t.Errorf("user's none replaced: %+v", got)
	}
	cfg := b.steamFile("config/config.vdf")
	b.run(false)
	if !bytes.Equal(b.steamFile("config/config.vdf"), cfg) {
		t.Error("a second run changed config.vdf")
	}

	// The user picks another Proton for one game: theirs from then on.
	b.setMapping(forza, steam.CompatTool{Name: "proton_9", Priority: "250"})
	// And unticks Force for another: declined, never written again.
	b.deleteMapping(starfield)
	for range 2 {
		b.run(false)
		if got, _ := b.mapping(forza); got.Name != "proton_9" {
			t.Errorf("user's pick undone: %+v", got)
		}
		if got, ok := b.mapping(starfield); ok {
			t.Errorf("declined copy written again: %+v", got)
		}
	}
	if a := b.state().peekApp(forza); a != nil && a.Mapping != nil {
		t.Errorf("user's entry still recorded: %+v", a.Mapping)
	}
	if m := b.state().peekApp(starfield).Mapping; !m.Declined || m.owned() {
		t.Errorf("declined record %+v", m)
	}

	// A game no longer installed keeps its copy (a drive may be away).
	b.uninstallGame(elden)
	b.run(false)
	b.wantCopies(elden)

	// Valve pins it to one Proton: the copy goes, and Valve's pick holds.
	picks := defaultPicks()
	picks[elden] = valveApp{"Game", "proton-9.0-4RC"}
	b.writeAppInfo(picks, false)
	b.run(false)
	if got, ok := b.mapping(elden); ok {
		t.Errorf("copy kept over Valve's pin: %+v", got)
	}
	if a := b.state().peekApp(elden); a != nil {
		t.Errorf("record %+v", a)
	}

	// A newly installed game gets one at the next start.
	b.installGame(unowned)
	b.run(false)
	b.wantCopies(unowned)

	// The user picks their own default: VaporOS gives its copies back, and
	// forgets the declined one.
	b.setMapping(0, steam.CompatTool{Name: "proton_experimental", Priority: "75"})
	b.run(false)
	if got, ok := b.mapping(unowned); ok {
		t.Errorf("copy kept under the user's default: %+v", got)
	}
	if got, _ := b.mapping(forza); got.Name != "proton_9" {
		t.Errorf("user's pick touched: %+v", got)
	}
	for k, a := range b.state().Apps {
		if a.Mapping != nil {
			t.Errorf("app %s still recorded: %+v", k, a.Mapping)
		}
	}
}

func TestGameCopiesStepAside(t *testing.T) {
	b := gameBox(t)
	// The user forced the tool on one game before VaporOS: theirs.
	b.setMapping(fever, oursApp)
	b.setMapping(bus, oursApp)
	b.run(false)
	b.wantCopies(starfield, forza, elden)
	if a := b.state().peekApp(bus); a != nil {
		t.Errorf("user's entry taken over: %+v", a)
	}
	orig := b.steamFile("config/config.vdf")

	// The tool is missing: the copies go and come back with it.
	b.removeTool(tool)
	b.desire(Desired{Dispatcher: true})
	b.run(false)
	for _, id := range []uint32{starfield, forza, elden} {
		if got, ok := b.mapping(id); ok {
			t.Errorf("app %d mapped to a missing tool: %+v", id, got)
		}
		if m := b.state().peekApp(id).Mapping; !m.Suspended || !m.FollowsDefault {
			t.Errorf("app %d not suspended: %+v", id, m)
		}
	}
	b.installTool(tool)
	b.desire(proton())
	b.run(false)
	if got := b.steamFile("config/config.vdf"); !bytes.Equal(got, orig) {
		t.Errorf("not back as it was:\n%s", got)
	}

	// --unwrap takes them out, and the next run puts them back.
	b.run(true)
	for _, id := range []uint32{starfield, forza, elden} {
		if got, ok := b.mapping(id); ok {
			t.Errorf("unwrap left app %d: %+v", id, got)
		}
	}
	if got, _ := b.mapping(bus); got != oursApp {
		t.Errorf("unwrap took the user's entry: %+v", got)
	}
	b.run(false)
	b.wantCopies(starfield, forza, elden)

	// Steam's files reset (no default either): the copies come back, none
	// declined.
	b.copyFixture("config.vdf", "config/config.vdf")
	b.deleteMapping(0)
	b.run(false)
	b.wantCopies(starfield, forza, elden)
	if got, _ := b.mapping(0); got != ours {
		t.Errorf("default %+v", got)
	}

	// TruckersMP forces a game Valve picks Proton for: forced, and not a
	// copy of the default.
	picks := defaultPicks()
	picks[ets2] = valveApp{"Game", "proton-stable"}
	b.writeAppInfo(picks, false)
	b.desire(truckers(proton()))
	b.run(false)
	if m := b.state().peekApp(ets2).Mapping; !m.owned() || m.FollowsDefault {
		t.Errorf("ETS2 record %+v", m)
	}
}

func TestGameCopiesWithoutValvesPicks(t *testing.T) {
	b := gameBox(t)
	b.run(false)
	b.wantCopies(starfield, forza, elden)

	// Steam's cache without the Steam Play manifest, or none at all: what
	// Valve picks is not known, so nothing new is written and the copies
	// VaporOS has stay.
	b.installGame(unowned)
	b.writeAppInfo(defaultPicks(), true)
	b.run(false)
	b.wantCopies(starfield, forza, elden)
	b.check(os.Remove(steam.AppInfoPath(b.root)))
	b.run(false)
	b.wantCopies(starfield, forza, elden)

	// A cache of another version says nothing either, and logs why.
	b.write(steam.AppInfoPath(b.root), []byte("\x27\x44\x56\x07\x01\x00\x00\x00"))
	b.run(false)
	b.wantCopies(starfield, forza, elden)
	if !bytes.Contains(b.logs.Bytes(), []byte("unknown version")) {
		t.Errorf("not logged:\n%s", b.logs.String())
	}
	if b.state().Error != "" {
		t.Errorf("record error %q", b.state().Error)
	}
}

func TestDecideFollow(t *testing.T) {
	present := func(string) bool { return true }
	absent := func(string) bool { return false }
	mine := Mapping{Wrote: tool, FollowsDefault: true}
	user := &steam.CompatTool{Name: "proton_9", Priority: "250"}
	for _, c := range []struct {
		name string
		in   mapInput
		act  mapAction
		next Mapping
	}{
		{"new", mapInput{key: forza, want: tool, toolOK: present},
			mapAction{set: &oursApp}, mine},
		{"new, the user's entry", mapInput{key: forza, cur: user, want: tool, toolOK: present},
			mapAction{}, Mapping{}},
		{"new, the same as the user's", mapInput{key: forza, cur: &oursApp, want: tool, toolOK: present},
			mapAction{}, Mapping{}},
		{"dropped", mapInput{key: forza, cur: &oursApp, m: mine, toolOK: present, drop: true},
			mapAction{del: true}, Mapping{}},
		{"removed in Steam", mapInput{key: forza, m: mine, want: tool, toolOK: present, defaultThere: true},
			mapAction{}, Mapping{Declined: true, FollowsDefault: true}},
		{"lost with Steam's files", mapInput{key: forza, m: mine, want: tool, toolOK: present},
			mapAction{set: &oursApp}, mine},
		{"declined stays", mapInput{key: forza, m: Mapping{Declined: true, FollowsDefault: true}, want: tool, toolOK: present, defaultThere: true},
			mapAction{}, Mapping{Declined: true, FollowsDefault: true}},
		{"declined, then picked", mapInput{key: forza, cur: &oursApp, m: Mapping{Declined: true, FollowsDefault: true}, want: tool, toolOK: present},
			mapAction{}, Mapping{}},
		{"not known now", mapInput{key: forza, cur: &oursApp, m: mine, toolOK: present},
			mapAction{}, mine},
		{"tool missing", mapInput{key: forza, cur: &oursApp, m: mine, toolOK: absent},
			mapAction{del: true}, Mapping{Wrote: tool, Suspended: true, FollowsDefault: true}},
		{"suspended, dropped", mapInput{key: forza, m: Mapping{Wrote: tool, Suspended: true, FollowsDefault: true}, toolOK: present, drop: true},
			mapAction{}, Mapping{}},
		{"suspended, back", mapInput{key: forza, cur: user, m: Mapping{Wrote: tool, Suspended: true, FollowsDefault: true}, want: tool, toolOK: present},
			mapAction{set: &oursApp}, Mapping{Wrote: tool, Before: user, FollowsDefault: true}},
		{"unwrap", mapInput{key: forza, cur: &oursApp, m: mine, want: tool, toolOK: present, unwrap: true},
			mapAction{del: true}, Mapping{Wrote: tool, Suspended: true, FollowsDefault: true}},
		{"unwrap the user's", mapInput{key: forza, cur: user, m: mine, toolOK: present, unwrap: true},
			mapAction{}, Mapping{}},
	} {
		act, next := decideFollow(c.in)
		if !sameAction(act, c.act) || !sameMapping(next, c.next) || next.FollowsDefault != c.next.FollowsDefault || next.Declined != c.next.Declined {
			t.Errorf("%s: %+v %+v, want %+v %+v", c.name, act, next, c.act, c.next)
		}
	}
}
