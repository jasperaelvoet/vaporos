package sunshine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// fakeGPU makes sysDRM look like an amdgpu card1 whose DP-1 is the
// virtual connector and whose render node is renderD129.
func fakeGPU(t *testing.T, sysDRM string) {
	t.Helper()
	os.MkdirAll(filepath.Join(sysDRM, "card1-DP-1"), 0o755)
	os.MkdirAll(filepath.Join(sysDRM, "card1", "device", "drm", "renderD129"), 0o755)
	os.MkdirAll(filepath.Join(sysDRM, "card1", "device", "drm", "card1"), 0o755)
}

func TestRenderConfDefaults(t *testing.T) {
	h := newHarness(t)
	fakeGPU(t, h.s.sysDRM)
	changed, err := h.s.writeConf()
	if err != nil || !changed {
		t.Fatalf("writeConf = %v, %v", changed, err)
	}
	data, err := os.ReadFile(confPath())
	if err != nil {
		t.Fatal(err)
	}
	got := parseConf(data)
	want := map[string]string{
		"capture":               "kms",
		"encoder":               "vulkan",
		"adapter_name":          "/dev/dri/renderD129",
		"output_name":           "DP-1",
		"max_bitrate":           "0",
		"gamepad":               "xone",
		"origin_web_ui_allowed": "pc",
		"upnp":                  "disabled",
		"system_tray":           "disabled",
		"global_prep_cmd":       `[{"do":"/usr/bin/vos session begin","undo":"/usr/bin/vos session end","elevated":false}]`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rendered keys:\n%v\nwant\n%v\nfile:\n%s", got, want, data)
	}
	// The prep-cmd value must be the JSON list Sunshine parses.
	var prep []map[string]any
	if err := json.Unmarshal([]byte(got["global_prep_cmd"]), &prep); err != nil || len(prep) != 1 || prep[0]["elevated"] != false {
		t.Errorf("global_prep_cmd = %v, %v", prep, err)
	}
	if fi, _ := os.Stat(confPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if changed, err := h.s.writeConf(); err != nil || changed {
		t.Errorf("identical re-render reported changed=%v, %v", changed, err)
	}
	os.Chmod(confPath(), 0o644)
	h.s.writeConf()
	if fi, _ := os.Stat(confPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode not repaired: %v", fi.Mode().Perm())
	}
}

func TestRenderConfKeepsUserAndSunshineKeys(t *testing.T) {
	h := newHarness(t)
	ref, err := os.ReadFile("testdata/reference-sunshine.conf")
	if err != nil {
		t.Fatal(err)
	}
	prev := strings.Replace(string(ref), "encoder = vulkan", "encoder = vaapi", 1) +
		"max_bitrate = 80000\naudio_sink = alsa_output.pci-0000_03_00.1.hdmi-stereo\ngamepad_driver = all\nbogus = 1\n"
	os.MkdirAll(sunshineDir(), 0o700)
	os.WriteFile(confPath(), []byte(prev), 0o600)

	if _, err := h.s.writeConf(); err != nil {
		t.Fatal(err)
	}
	want := Settings{Encoder: "vaapi", BitrateKbpsMax: 80000, AudioSink: "alsa_output.pci-0000_03_00.1.hdmi-stereo", Gamepad: "xone"}
	if got := h.s.currentSettings(); got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
	data, _ := os.ReadFile(confPath())
	vars := parseConf(data)
	if vars["gamepad_driver"] != "all" || vars["encoder"] != "vaapi" || vars["audio_sink"] != want.AudioSink {
		t.Errorf("carried keys lost:\n%s", data)
	}
	for _, gone := range []string{"csrf_allowed_origins", "bogus"} {
		if _, ok := vars[gone]; ok {
			t.Errorf("%s survived the re-render", gone)
		}
	}
	// No GPU in sysfs: the single-GPU default.
	if vars["adapter_name"] != "/dev/dri/renderD128" {
		t.Errorf("adapter_name = %q", vars["adapter_name"])
	}
}

func TestSettingsFromConfIgnoresInvalid(t *testing.T) {
	got := settingsFromConf(map[string]string{"encoder": "quicksync", "max_bitrate": "-5", "audio_sink": "a#b", "gamepad": "ds5"})
	if want := (Settings{Encoder: "vulkan", Gamepad: "ds5"}); got != want {
		t.Errorf("settings = %+v", got)
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := []Settings{
		DefaultSettings(),
		{Encoder: "software", BitrateKbpsMax: 150000, AudioSink: "sink.1", Gamepad: "auto"},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []Settings{
		{Encoder: "", Gamepad: "xone"},
		{Encoder: "auto", Gamepad: "xone"},
		{Encoder: "vulkan", Gamepad: "wiimote"},
		{Encoder: "vulkan", Gamepad: "xone", BitrateKbpsMax: -1},
		{Encoder: "vulkan", Gamepad: "xone", BitrateKbpsMax: maxBitrateKbps + 1},
		{Encoder: "vulkan", Gamepad: "xone", AudioSink: "sink # comment"},
		{Encoder: "vulkan", Gamepad: "xone", AudioSink: "two\nlines"},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}

func TestParseConf(t *testing.T) {
	src := "# comment\n" +
		"  capture = kms   # trailing comment\n" +
		"encoder=vulkan\r\n" +
		"empty =\n" +
		"= novalue\n" +
		"global_prep_cmd = [{\"do\":\"a\",\n  \"undo\":\"b\"}, [1]]\n" +
		"after_list = yes\n" +
		"capture = x11\n" +
		"broken = [unterminated\n"
	got := parseConf([]byte(src))
	want := map[string]string{
		"capture":         "kms",
		"encoder":         "vulkan",
		"global_prep_cmd": "[{\"do\":\"a\",\n  \"undo\":\"b\"}, [1]]",
		"after_list":      "yes",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseConf = %q\nwant %q", got, want)
	}
}

func TestOutputName(t *testing.T) {
	cases := map[string]string{
		"DP-1":        "DP-1",
		" DP-2 ":      "DP-2",
		"card1-DP-1":  "DP-1",
		"HDMI-A-1":    "HDMI-A-1",
		"card0-eDP-1": "eDP-1",
		"":            "",
		"0":           "", // never a plane index
		"DP-1\nx = 1": "",
		"cardX-DP-1":  "",
	}
	for in, want := range cases {
		if got := outputName(in); got != want {
			t.Errorf("outputName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImageTemplate(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(templatePath(), []byte("capture = kms\noutput_name = {{.OutputName}}\n# image template\n"), 0o644)
	h.s.tmpl = loadTemplate()
	h.s.writeConf()
	data, _ := os.ReadFile(confPath())
	if !strings.Contains(string(data), "# image template") || !strings.Contains(string(data), "output_name = DP-1") {
		t.Errorf("image template not used:\n%s", data)
	}

	for _, broken := range []string{"{{.OutputName", "capture = {{.NoSuchField}}\n"} {
		os.WriteFile(templatePath(), []byte(broken), 0o644)
		h.s.tmpl = loadTemplate()
		if _, err := h.s.writeConf(); err != nil {
			t.Fatal(err)
		}
		data, _ = os.ReadFile(confPath())
		if !strings.Contains(string(data), "origin_web_ui_allowed = pc") {
			t.Errorf("template %q did not fall back to the built-in one:\n%s", broken, data)
		}
	}
}

func TestWriteGamerFileRefusesSymlinks(t *testing.T) {
	isolate(t)
	// A planted directory symlink is refused.
	target := t.TempDir()
	os.Symlink(target, filepath.Join(config.GamerHome, ".config"))
	if _, err := writeGamerFile(confPath(), []byte("x"), 0o600); err == nil {
		t.Fatal("wrote through a symlinked directory")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("symlink target was written: %v", entries)
	}

	// A planted file symlink is replaced, its target untouched.
	isolate(t)
	os.MkdirAll(sunshineDir(), 0o700)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("precious"), 0o644)
	os.Symlink(victim, confPath())
	if changed, err := writeGamerFile(confPath(), []byte("new"), 0o600); err != nil || !changed {
		t.Fatalf("writeGamerFile = %v, %v", changed, err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Error("symlink target modified")
	}
	if fi, _ := os.Lstat(confPath()); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("symlink not replaced")
	}
}
