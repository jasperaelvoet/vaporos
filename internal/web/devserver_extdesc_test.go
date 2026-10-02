package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// descriptorCard is what a card of GET /extensions takes from the shipped
// descriptor d (internal/extensions document.go), as JSON: everything but
// the state, the build's facts (size, permissions, runs_as_root), the
// settings' values, the helper's status lines, Steam's app names and
// whether its web UI is served now.
func descriptorCard(d *descriptor.Descriptor) map[string]any {
	x := extensions.ExtensionDoc{ID: d.ID, Name: d.Name, Summary: d.Summary, Category: d.Category, Core: d.Core, Upstream: d.Upstream,
		Caveats: append([]string{}, d.Caveats...), Copy: extensions.CopyDoc{Install: d.Copy.Install, Remove: d.Copy.Remove},
		Requires: append([]string{}, d.Requires...), Downloads: []extensions.DownloadDoc{}, Settings: []extensions.SettingDoc{},
		Actions: []extensions.ActionDoc{}, ModuleOptions: d.HasModuleOptions()}
	if d.Web != nil {
		x.Web = &extensions.WebDoc{Port: d.Web.Port, Label: d.Web.Label}
	}
	for _, dl := range d.Downloads {
		x.Downloads = append(x.Downloads, extensions.DownloadDoc{What: dl.What, From: dl.From, Checked: dl.Checked, RunsCode: dl.RunsCode, When: dl.When})
	}
	modules := map[string]bool{}
	for _, m := range d.ModuleOptions {
		modules[m.Setting] = true
	}
	for _, s := range d.Settings {
		x.Settings = append(x.Settings, extensions.SettingDoc{Key: s.Key, Type: s.Type, Label: s.Label, Help: s.Help, Restart: s.Restart,
			Choices: append([]string{}, s.Choices...), NeedsPassword: modules[s.Key], Required: s.Required})
	}
	for _, a := range d.Actions {
		ad := extensions.ActionDoc{Name: a.Name, Label: a.Label}
		if c := a.Confirm; c != nil {
			ad.Confirm = &extensions.ConfirmDoc{Title: c.Title, Body: c.Body, Button: c.Button, Tone: c.Tone}
		}
		x.Actions = append(x.Actions, ad)
	}
	if st := d.Steam; st != nil && (st.CompatTool != "" || len(st.ForceCompatTool) > 0 || len(st.Hooks) > 0 || len(st.Shortcuts) > 0) {
		x.Steam = &extensions.SteamDoc{CompatTool: st.CompatTool, Forces: []extensions.SteamAppDoc{}, Hooks: []extensions.SteamAppDoc{}, Shortcuts: []extensions.ShortcutDoc{}}
		for _, a := range st.ForceCompatTool {
			x.Steam.Forces = append(x.Steam.Forces, extensions.SteamAppDoc{App: a})
		}
		for _, h := range st.Hooks {
			for _, a := range h.Apps {
				if !slices.ContainsFunc(x.Steam.Hooks, func(s extensions.SteamAppDoc) bool { return s.App == a }) {
					x.Steam.Hooks = append(x.Steam.Hooks, extensions.SteamAppDoc{App: a})
				}
			}
		}
		for _, sc := range st.Shortcuts {
			x.Steam.Shortcuts = append(x.Steam.Shortcuts, extensions.ShortcutDoc{Name: sc.Name})
		}
	}
	b, _ := json.Marshal(x)
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"state", "wanted", "mounted", "size", "progress", "reason", "permissions", "runs_as_root", "status", "required_by", "needs_password", "web_running"} {
		delete(m, k)
	}
	for _, s := range asList(m["settings"]) {
		delete(asObj(s), "value")
	}
	if st, ok := m["steam"].(map[string]any); ok {
		for _, k := range []string{"forces", "hooks"} {
			for _, a := range asList(st[k]) {
				delete(asObj(a), "name")
			}
		}
	}
	return m
}

// The fixtures' cards of the shipped extensions say what their descriptors
// in extensions/ say, so the control center is designed against the real
// catalogue.
func TestFixturesFollowTheDescriptors(t *testing.T) {
	files := []string{filepath.Join(fixturesDir, "base", "extensions.json")}
	presets, _ := filepath.Glob(filepath.Join(fixturesDir, "presets", "*.json"))
	files = append(files, presets...)
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		doc := v
		if filepath.Base(filepath.Dir(f)) == "presets" {
			doc = asObj(asObj(v["patch"])["extensions"])
		}
		for _, c := range asList(doc["extensions"]) {
			card := asObj(c)
			id := asStr(card["id"])
			d, err := descriptor.Load(filepath.Join("..", "..", "extensions", id, "extension.json"))
			if err != nil {
				t.Errorf("%s: %s: %v", f, id, err)
				continue
			}
			seen[id] = true
			want := descriptorCard(d)
			for k, w := range want {
				got := card[k]
				if k == "settings" || k == "steam" {
					got = trimCard(k, got)
				}
				if !reflect.DeepEqual(got, w) {
					t.Errorf("%s: %s: %s is %v, its descriptor says %v", f, id, k, got, w)
				}
			}
		}
	}
	dirs, _ := filepath.Glob(filepath.Join("..", "..", "extensions", "*", "extension.json"))
	for _, p := range dirs {
		if id := filepath.Base(filepath.Dir(p)); !seen[id] {
			t.Errorf("no fixture card for the shipped extension %s", id)
		}
	}
}

// trimCard drops from a fixture's settings or steam what descriptorCard
// leaves out.
func trimCard(k string, v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	switch k {
	case "settings":
		for _, s := range asList(out) {
			delete(asObj(s), "value")
		}
	case "steam":
		if st, ok := out.(map[string]any); ok {
			for _, kk := range []string{"forces", "hooks"} {
				for _, a := range asList(st[kk]) {
					delete(asObj(a), "name")
				}
			}
		}
	}
	return out
}
