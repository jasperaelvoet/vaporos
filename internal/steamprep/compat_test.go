package steamprep

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

var (
	ours    = steam.CompatTool{Name: tool, Priority: "75"}
	oursApp = steam.CompatTool{Name: tool, Priority: "250"}
)

func TestUserDefaultWins(t *testing.T) {
	b := newBox(t)
	b.desire(proton())

	// A default the user picked before VaporOS had one is theirs.
	userCfg := b.steamFile("config/config.vdf")
	userCfg, _, err := steam.SetCompatToolMapping(userCfg, 0, steam.CompatTool{Name: "proton_experimental", Priority: "75"})
	b.check(err)
	b.write(b.root+"/config/config.vdf", userCfg)
	b.run(false)
	if !bytes.Equal(b.steamFile("config/config.vdf"), userCfg) || b.state().Default.Wrote != "" {
		t.Fatalf("the user's default was taken over: %+v", b.state().Default)
	}

	// Once VaporOS set it, a change in Steam's settings is the user's
	// choice from then on.
	c := newBox(t)
	c.desire(proton())
	c.run(false)
	if got, _ := c.mapping(0); got != ours {
		t.Fatalf("default %+v", got)
	}
	c.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.SetCompatToolMapping(d, 0, steam.CompatTool{Name: "proton_9", Priority: "75"})
		c.check(err)
		return out
	})
	for range 2 {
		c.run(false)
		if got, _ := c.mapping(0); got.Name != "proton_9" || c.state().Default.Wrote != "" {
			t.Fatalf("user's change undone: %+v %+v", got, c.state().Default)
		}
	}
	// Even when the tool goes: the entry is the user's, not VaporOS's.
	c.removeTool(tool)
	c.run(false)
	if got, _ := c.mapping(0); got.Name != "proton_9" {
		t.Errorf("user's entry restored away: %+v", got)
	}

	// Removing the default in Steam is not a choice of one: VaporOS sets
	// it again where it is missing.
	d := newBox(t)
	d.desire(proton())
	d.run(false)
	d.edit("config/config.vdf", func(data []byte) []byte {
		out, _, err := steam.DeleteCompatToolMapping(data, 0)
		d.check(err)
		return out
	})
	d.run(false)
	if got, _ := d.mapping(0); got != ours {
		t.Errorf("default not set again: %+v", got)
	}
}

func TestMissingToolSuspendsAndComesBack(t *testing.T) {
	b := newBox(t)
	b.desire(truckers(proton()))
	orig := b.steamFile("config/config.vdf")
	b.run(false)
	if got, _ := b.mapping(ets2); got != oursApp {
		t.Fatalf("ETS2 %+v", got)
	}

	// The tool is gone (the extension did not mount): every entry VaporOS
	// owns gets its value from before back, and stays VaporOS's.
	b.removeTool(tool)
	b.run(false)
	if got := b.steamFile("config/config.vdf"); !bytes.Equal(got, orig) {
		t.Fatalf("not restored:\n%s", got)
	}
	st := b.state()
	if !st.Default.Suspended || st.Default.Wrote != tool || !st.peekApp(ets2).Mapping.Suspended {
		t.Fatalf("not suspended: %+v %+v", st.Default, st.peekApp(ets2).Mapping)
	}

	// Steam changes the default meanwhile: that counts as Steam's.
	b.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.SetCompatToolMapping(d, 0, steam.CompatTool{Name: "proton_experimental", Priority: "75"})
		b.check(err)
		return out
	})
	b.run(false)
	if got, _ := b.mapping(0); got.Name != "proton_experimental" {
		t.Fatalf("changed while the tool was away: %+v", got)
	}

	// The tool is back: VaporOS's values are too, and what Steam left is
	// what comes back the next time.
	b.installTool(tool)
	b.run(false)
	if got, _ := b.mapping(0); got != ours {
		t.Errorf("default %+v", got)
	}
	if got, _ := b.mapping(ets2); got != oursApp {
		t.Errorf("ETS2 %+v", got)
	}
	st = b.state()
	if st.Default.Suspended || st.Default.Before == nil || st.Default.Before.Name != "proton_experimental" {
		t.Errorf("default record %+v", st.Default)
	}

	// steam.json no longer asks for a tool that is missing (vosd leaves it
	// out): the same.
	b.removeTool(tool)
	b.desire(Desired{Dispatcher: true})
	b.run(false)
	if got, _ := b.mapping(0); got.Name != "proton_experimental" {
		t.Errorf("default %+v", got)
	}
	if _, ok := b.mapping(ets2); ok {
		t.Error("ETS2 still mapped to a missing tool")
	}
	b.installTool(tool)
	b.desire(truckers(proton()))
	b.run(false)
	if got, _ := b.mapping(ets2); got != oursApp {
		t.Errorf("ETS2 after %+v", got)
	}
}

func TestForcedAppsAndRelease(t *testing.T) {
	b := newBox(t)
	// The user ran ETS2 with Proton 9 before.
	b.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.SetCompatToolMapping(d, ets2, steam.CompatTool{Name: "proton_9", Priority: "250"})
		b.check(err)
		return out
	})
	b.desire(truckers(proton()))
	b.run(false)
	if got, _ := b.mapping(ets2); got != oursApp {
		t.Fatalf("ETS2 %+v", got)
	}
	if m := b.state().peekApp(ets2).Mapping; m.Before == nil || m.Before.Name != "proton_9" {
		t.Fatalf("record %+v", m)
	}

	// A forced app stays forced while its extension is there.
	b.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.SetCompatToolMapping(d, ets2, steam.CompatTool{Name: "proton_experimental", Priority: "250"})
		b.check(err)
		return out
	})
	b.run(false)
	if got, _ := b.mapping(ets2); got != oursApp {
		t.Errorf("not forced again: %+v", got)
	}
	if m := b.state().peekApp(ets2).Mapping; m.Before.Name != "proton_9" {
		t.Errorf("before %+v", m.Before)
	}

	// Not asked for while the tool is there (TruckersMP did not mount):
	// kept as it is, still VaporOS's.
	b.desire(proton())
	b.run(false)
	if got, _ := b.mapping(ets2); got != oursApp || !b.state().peekApp(ets2).Mapping.owned() {
		t.Errorf("not kept: %+v", got)
	}

	// TruckersMP removed: ETS2 and ATS stay on the tool, and are the
	// user's now.
	d := proton()
	d.Release = []AppRelease{{App: ets2}, {App: ats}}
	b.desire(d)
	b.run(false)
	if got, _ := b.mapping(ets2); got != oursApp {
		t.Errorf("released ETS2 %+v", got)
	}
	if a := b.state().peekApp(ets2); a != nil && a.Mapping != nil {
		t.Errorf("still owned: %+v", a.Mapping)
	}
	b.removeTool(tool)
	b.run(false)
	if got, _ := b.mapping(ats); got != oursApp {
		t.Errorf("released ATS restored: %+v", got)
	}
}

func TestToolAppsNeverMapped(t *testing.T) {
	b := newBox(t)
	// A Proton this VaporOS does not know by its id: its manifest says.
	const future = 9999999
	manifest := "\"AppState\"\n{\n\t\"appid\"\t\t\"9999999\"\n\t\"name\"\t\t\"Proton 12.0\"\n\t\"StateFlags\"\t\t\"4\"\n\t\"UserConfig\"\n\t{\n\t}\n}\n"
	b.write(filepath.Join(b.root, "steamapps", "appmanifest_9999999.acf"), []byte(manifest))
	d := proton()
	d.Apps = []AppWant{{App: 1493710, CompatTool: tool, Hooks: []string{"x"}}, {App: 4183110, CompatTool: tool},
		{App: 2805730, CompatTool: tool, Hooks: []string{"x"}},
		{App: future, CompatTool: tool, Hooks: []string{"x"}, Beta: &BetaWant{Branch: "beta", Request: "1"}}}
	b.desire(d)
	b.run(false)
	for _, app := range []uint32{1493710, 4183110, 2805730, future} {
		if _, ok := b.mapping(app); ok {
			t.Errorf("tool app %d mapped", app)
		}
		if _, ok := b.launchOptions(acctA, app); ok {
			t.Errorf("tool app %d wrapped", app)
		}
	}
	if got := b.steamFile("steamapps/appmanifest_9999999.acf"); string(got) != manifest {
		t.Errorf("tool's branch changed:\n%s", got)
	}
}

func TestDecideMapping(t *testing.T) {
	present := func(string) bool { return true }
	absent := func(string) bool { return false }
	user := &steam.CompatTool{Name: "proton_9", Priority: "250"}
	owned := Mapping{Wrote: tool, Before: user}
	for _, c := range []struct {
		name string
		in   mapInput
		act  mapAction
		next Mapping
	}{
		{"shortcut appears", mapInput{key: 0x80000001, want: tool, toolOK: present, shortcut: true},
			mapAction{set: &oursApp}, Mapping{Wrote: tool}},
		{"shortcut goes", mapInput{key: 0x80000001, cur: &oursApp, m: Mapping{Wrote: tool}, toolOK: present, shortcut: true},
			mapAction{del: true}, Mapping{}},
		{"shortcut gone while suspended", mapInput{key: 0x80000001, m: Mapping{Wrote: tool, Suspended: true}, toolOK: present, shortcut: true},
			mapAction{}, Mapping{}},
		{"unwrap", mapInput{key: ets2, cur: &oursApp, m: owned, want: tool, toolOK: present, unwrap: true},
			mapAction{set: user}, Mapping{Wrote: tool, Before: user, Suspended: true}},
		{"unwrap the user's default", mapInput{cur: user, m: Mapping{Wrote: tool}, toolOK: present, unwrap: true},
			mapAction{}, Mapping{}},
		{"release with the tool missing", mapInput{key: ets2, cur: &oursApp, m: owned, toolOK: absent, release: true},
			mapAction{set: user}, Mapping{}},
		{"adopt the default VaporOS wrote", mapInput{cur: &ours, want: tool, toolOK: present},
			mapAction{}, Mapping{Wrote: tool}},
		{"tool asked for is missing", mapInput{key: ets2, cur: user, want: tool, toolOK: absent},
			mapAction{}, Mapping{}},
		{"a new tool for an owned entry", mapInput{key: ets2, cur: &oursApp, m: owned, want: "proton-other", toolOK: present},
			mapAction{set: &steam.CompatTool{Name: "proton-other", Priority: "250"}}, Mapping{Wrote: "proton-other", Before: user}},
		// An entry that already holds the tool is taken over with nothing
		// of its own from before, so a missing tool removes it.
		{"adopt an app's entry", mapInput{key: ets2, cur: &oursApp, want: tool, toolOK: present},
			mapAction{}, Mapping{Wrote: tool}},
		{"adopt a shortcut's entry", mapInput{key: 0x80000001, cur: &oursApp, want: tool, toolOK: present, shortcut: true},
			mapAction{}, Mapping{Wrote: tool}},
		{"back while it holds the tool", mapInput{key: ets2, cur: &oursApp, m: Mapping{Wrote: tool, Suspended: true}, want: tool, toolOK: present},
			mapAction{}, Mapping{Wrote: tool}},
		// A record from before that names the missing tool counts as none.
		{"before is the missing tool", mapInput{key: ets2, cur: &oursApp, m: Mapping{Wrote: tool, Before: &oursApp}, want: tool, toolOK: absent},
			mapAction{del: true}, Mapping{Wrote: tool, Before: &oursApp, Suspended: true}},
		{"before is the missing tool, not asked for", mapInput{key: ets2, cur: &oursApp, m: Mapping{Wrote: tool, Before: &oursApp}, toolOK: absent},
			mapAction{del: true}, Mapping{Wrote: tool, Before: &oursApp, Suspended: true}},
		{"released, before is the missing tool", mapInput{key: ets2, cur: &oursApp, m: Mapping{Wrote: tool, Before: &oursApp}, toolOK: absent, release: true},
			mapAction{del: true}, Mapping{}},
		{"unwrap puts back even the same tool", mapInput{key: ets2, cur: &oursApp, m: Mapping{Wrote: tool, Before: &oursApp}, want: tool, toolOK: present, unwrap: true},
			mapAction{set: &oursApp}, Mapping{Wrote: tool, Before: &oursApp, Suspended: true}},
	} {
		act, next := decideMapping(c.in)
		if !sameAction(act, c.act) || !sameMapping(next, c.next) {
			t.Errorf("%s: %+v %+v, want %+v %+v", c.name, act, next, c.act, c.next)
		}
	}
}

func sameAction(a, b mapAction) bool {
	return a.del == b.del && (a.set == nil) == (b.set == nil) && (a.set == nil || *a.set == *b.set)
}

func sameMapping(a, b Mapping) bool {
	return a.Wrote == b.Wrote && a.Suspended == b.Suspended && (a.Before == nil) == (b.Before == nil) &&
		(a.Before == nil || *a.Before == *b.Before)
}
