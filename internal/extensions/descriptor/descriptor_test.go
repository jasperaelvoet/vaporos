package descriptor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func valid() *Descriptor {
	return &Descriptor{
		Schema: 1, ID: "coolercontrol", Name: "CoolerControl", Summary: "Fan control.",
		Category: System,
		Upstream: Upstream{Name: "CoolerControl", URL: "https://gitlab.com/coolercontrol/coolercontrol", License: "GPL-3.0-or-later"},
		Packages: []string{"cachyos/coolercontrold", "extra/liquidctl"},
		Strip:    []string{"usr/lib/udev/rules.d/71-liquidctl.rules"},
		Fetch: []Fetch{{URL: "https://example.com/a.tar.xz", SHA256: strings.Repeat("a", 64), License: "MIT",
			Extract: "a/b.exe", LicenseFile: "a/LICENSE", Dest: "b.exe"}},
		Permissions:   []string{PermService, PermModules},
		Services:      []Service{{Unit: "coolercontrold.service", Scope: "system"}},
		ModuleOptions: []ModuleOption{{Module: "amdgpu", Param: "ppfeaturemask", Setting: "gpu_fan_curves"}},
		Network:       &Network{Ports: []Port{{Proto: "tcp", Port: 11987, Mode: "proxied", Upstream: "127.0.0.1:11986"}}},
		Web:           &Web{Port: 11987, Label: "Open CoolerControl"},
		Data:          []Data{{Name: "config", Where: "system"}},
		Settings:      []Setting{{Key: "gpu_fan_curves", Type: "bool", Label: "GPU fan curves", Restart: true}},
		Steam: &Steam{CompatTool: "proton-cachyos-slr", ForceCompatTool: []uint32{227300},
			Hooks: []Hook{{Apps: []uint32{227300}}}, Shortcuts: []Shortcut{{Key: "ets2", Name: "TruckersMP (ETS2)", CompatTool: true}}},
		Downloads: []Download{{What: "The mod", From: "download.ets2mp.com", Checked: "publisher-hash", RunsCode: true, When: "update"}},
		Actions:   []Action{{Name: "copy-profiles", Label: "Copy profiles", Confirm: &Confirm{Title: "Copy?", Body: "It copies.", Button: "Copy"}}},
	}
}

func encode(t *testing.T, d *Descriptor) []byte {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseValid(t *testing.T) {
	d, err := Parse(encode(t, valid()))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "coolercontrol" || d.Web.Port != 11987 || !d.HasModuleOptions() || d.RunsAsRoot() {
		t.Fatalf("%+v", d)
	}
	if s, ok := d.Setting("gpu_fan_curves"); !ok || !s.Restart {
		t.Fatal("setting")
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	b := encode(t, valid())
	b = append(b[:len(b)-1], []byte(`,"permisions":["udev"]}`)...)
	if _, err := Parse(b); err == nil {
		t.Fatal("a misspelt key was accepted")
	}
}

// Hooks run in catalog order: a descriptor cannot ask for another.
func TestParseRejectsHookOrder(t *testing.T) {
	b := []byte(strings.Replace(string(encode(t, valid())), `"hooks":[{"apps":[227300]}]`, `"hooks":[{"apps":[227300],"order":1}]`, 1))
	if !strings.Contains(string(b), `"order":1`) {
		t.Fatal("the test descriptor has no hook")
	}
	if _, err := Parse(b); err == nil {
		t.Fatal("a hook order was accepted")
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(d *Descriptor){
		"schema":             func(d *Descriptor) { d.Schema = 2 },
		"id":                 func(d *Descriptor) { d.ID = "Cooler" },
		"no name":            func(d *Descriptor) { d.Name = "" },
		"long summary":       func(d *Descriptor) { d.Summary = strings.Repeat("x", 141) },
		"control chars":      func(d *Descriptor) { d.Summary = "a\nb" },
		"category":           func(d *Descriptor) { d.Category = "game" },
		"http upstream":      func(d *Descriptor) { d.Upstream.URL = "http://x.org" },
		"no license":         func(d *Descriptor) { d.Upstream.License = "" },
		"package":            func(d *Descriptor) { d.Packages = []string{"coolercontrold"} },
		"strip outside usr":  func(d *Descriptor) { d.Strip = []string{"etc/x"} },
		"strip dotdot":       func(d *Descriptor) { d.Strip = []string{"usr/../etc"} },
		"exempt":             func(d *Descriptor) { d.ELFExempt = []string{"/usr/x"} },
		"fetch http":         func(d *Descriptor) { d.Fetch[0].URL = "http://example.com/a" },
		"fetch sha":          func(d *Descriptor) { d.Fetch[0].SHA256 = "abc" },
		"fetch dest":         func(d *Descriptor) { d.Fetch[0].Dest = "../x" },
		"license no extract": func(d *Descriptor) { d.Fetch[0].Extract = ""; d.Fetch[0].LicenseFile = "LICENSE" },
		"self require":       func(d *Descriptor) { d.Requires = []string{"coolercontrol"} },
		"permission":         func(d *Descriptor) { d.Permissions = []string{"everything"} },
		"repeated perm":      func(d *Descriptor) { d.Permissions = []string{PermService, PermService} },
		"tmpfiles perm":      func(d *Descriptor) { d.Permissions = []string{PermService, "tmpfiles"} },
		"service perm":       func(d *Descriptor) { d.Permissions = []string{PermModules} },
		"unit":               func(d *Descriptor) { d.Services[0].Unit = "../x.service" },
		"scope":              func(d *Descriptor) { d.Services[0].Scope = "global" },
		"option setting":     func(d *Descriptor) { d.ModuleOptions[0].Setting = "nope" },
		"option no restart":  func(d *Descriptor) { d.Settings[0].Restart = false },
		"port low":           func(d *Descriptor) { d.Network.Ports[0].Port = 80 },
		"proxied public":     func(d *Descriptor) { d.Network.Ports[0].Upstream = "0.0.0.0:11986" },
		"mode":               func(d *Descriptor) { d.Network.Ports[0].Mode = "public" },
		"web port":           func(d *Descriptor) { d.Web.Port = 12000 },
		"default tool":       func(d *Descriptor) { d.Steam.DefaultCompatTool = "proton-cachyos-slr" },
		"force no tool":      func(d *Descriptor) { d.Steam.CompatTool = "" },
		"hook app 0":         func(d *Descriptor) { d.Steam.Hooks[0].Apps = []uint32{0} },
		"shortcut key":       func(d *Descriptor) { d.Steam.Shortcuts[0].Key = "E T S" },
		"data where":         func(d *Descriptor) { d.Data[0].Where = "cloud" },
		"download host":      func(d *Descriptor) { d.Downloads[0].From = "https://x" },
		"download checked":   func(d *Descriptor) { d.Downloads[0].Checked = "maybe" },
		"setting type":       func(d *Descriptor) { d.Settings[0].Type = "text" },
		"required bool":      func(d *Descriptor) { d.Settings[0].Required = true },
		"reserved id":        func(d *Descriptor) { d.ID = "skip-once" },
		"action run_as":      func(d *Descriptor) { d.Actions[0].RunAs = "admin" },
		"confirm tone":       func(d *Descriptor) { d.Actions[0].Confirm.Tone = "loud" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := valid()
			mutate(d)
			if err := d.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// Only a disk setting may be required: the drive an extension needs.
func TestRequiredDisk(t *testing.T) {
	d := valid()
	d.Settings = append(d.Settings, Setting{Key: "disk", Type: "disk", Label: "Game drive", Required: true})
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if s, _ := d.Setting("disk"); !s.Required {
		t.Fatal("not required")
	}
}

func TestCoreDefaultCompatTool(t *testing.T) {
	d := valid()
	d.Core = true
	d.Steam.DefaultCompatTool = "proton-cachyos-slr"
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Every descriptor in the repository is valid and has no build section.
func TestRepositoryDescriptors(t *testing.T) {
	dirs, err := filepath.Glob("../../../extensions/*/extension.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no extensions/*/extension.json found")
	}
	for _, f := range dirs {
		d, err := Load(f)
		if err != nil {
			t.Error(err)
			continue
		}
		if err := d.ValidateSource(); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if want := filepath.Base(filepath.Dir(f)); d.ID != want {
			t.Errorf("%s: id %q does not match its directory %q", f, d.ID, want)
		}
	}
}

func TestLoadTooBig(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(f, make([]byte, MaxSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil {
		t.Fatal("accepted")
	}
}
