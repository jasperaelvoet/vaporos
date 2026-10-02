package coolercontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// fakeHelper is the helper with every outside call faked.
type fakeHelper struct {
	*helper
	t         time.Time
	mounted   bool
	wanted    bool
	state     string
	answers   bool
	states    int // state calls
	systemctl []string
}

func newFakeHelper() *fakeHelper {
	f := &fakeHelper{helper: newHelper(), t: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		mounted: true, wanted: true, state: "active", answers: true}
	f.helper.now = func() time.Time { return f.t }
	f.helper.mounted = func(string) bool { return f.mounted }
	f.helper.wanted = func(string) bool { return f.wanted }
	f.helper.state = func(_ context.Context, u string) string {
		if u != unit {
			panic(u)
		}
		f.states++
		return f.state
	}
	f.helper.handshake = func(_ context.Context, up string) bool { return up == "127.0.0.1:11986" && f.answers }
	f.helper.systemctl = func(_ context.Context, args ...string) error {
		f.systemctl = append(f.systemctl, strings.Join(args, " "))
		return nil
	}
	return f
}

func (f *fakeHelper) status() []extensions.StatusLine {
	f.t = f.t.Add(time.Minute) // past the cache
	return f.Status(context.Background(), &extensions.Ext{ID: id, Desc: ccDesc()})
}

func ccDesc() *descriptor.Descriptor {
	return &descriptor.Descriptor{ID: id, Network: &descriptor.Network{Ports: []descriptor.Port{
		{Proto: "tcp", Port: 11987, Mode: "proxied", Upstream: "127.0.0.1:11986"}}}}
}

func TestStatus(t *testing.T) {
	f := newFakeHelper()
	starting := []extensions.StatusLine{{Text: startingText}}
	notRunning := []extensions.StatusLine{{Text: notRunningText, Tone: "warning"}}
	for _, tc := range []struct {
		name            string
		mounted, wanted bool
		state           string
		answers         bool
		want            []extensions.StatusLine
	}{
		{"running", true, true, "active", true, nil},
		{"not answering", true, true, "active", false, notRunning},
		{"starting", true, true, "activating", false, starting},
		{"failed", true, true, "failed", false, notRunning},
		{"stopped", true, true, "inactive", false, notRunning},
		{"not added yet", false, true, "", false, nil},
		{"removed until the restart", true, false, "inactive", false, nil},
	} {
		f.mounted, f.wanted, f.state, f.answers = tc.mounted, tc.wanted, tc.state, tc.answers
		if got := f.status(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}

	n := f.states
	f.Status(context.Background(), &extensions.Ext{ID: id, Desc: ccDesc()})
	f.Status(context.Background(), &extensions.Ext{ID: id, Desc: ccDesc()})
	if f.states != n {
		t.Errorf("asked systemd %d times within %v", f.states-n, statusTTL)
	}
}

func TestHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/handshake" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"shake":true}`))
	}))
	defer srv.Close()
	up := strings.TrimPrefix(srv.URL, "http://")
	if !handshake(context.Background(), up) {
		t.Fatal("no handshake from a server that answers")
	}
	srv.Close()
	if handshake(context.Background(), up) {
		t.Fatal("handshake from a server that is gone")
	}
}

// pciDevice adds a PCI device with vendor and class to the fake bus.
func pciDevice(t *testing.T, name, vendor, class string) {
	t.Helper()
	d := filepath.Join(pciDevicesDir, name)
	must(t, os.MkdirAll(d, 0o755))
	must(t, os.WriteFile(filepath.Join(d, "vendor"), []byte(vendor+"\n"), 0o444))
	must(t, os.WriteFile(filepath.Join(d, "class"), []byte(class+"\n"), 0o444))
}

// fakeModuleSys points ModuleOptions at an empty sysfs and a boot without
// module options.
func fakeModuleSys(t *testing.T) *helper {
	t.Helper()
	savedMask, savedPCI := amdgpuFeatureMask, pciDevicesDir
	t.Cleanup(func() { amdgpuFeatureMask, pciDevicesDir = savedMask, savedPCI })
	root := t.TempDir()
	amdgpuFeatureMask = filepath.Join(root, "module", "amdgpu", "parameters", "ppfeaturemask")
	pciDevicesDir = filepath.Join(root, "bus", "pci", "devices")
	must(t, os.MkdirAll(filepath.Dir(amdgpuFeatureMask), 0o755))
	h := newHelper()
	h.booted = func() []string { return nil }
	return h
}

func TestModuleOptions(t *testing.T) {
	h := fakeModuleSys(t)
	opts := func(gpu, it87 bool) string {
		return strings.Join(h.ModuleOptions(&extensions.Ext{ID: id, Settings: map[string]any{"gpu_fan_curves": gpu, "it87_conflicts": it87}}), "|")
	}
	if got := opts(true, false); got != "" {
		t.Errorf("no AMD graphics card: %q", got)
	}
	for mask, want := range map[string]string{
		"4294426623\n": "options amdgpu ppfeaturemask=0xfff7ffff", // the kernel's default, as it prints it
		"0xfff7ffff":   "options amdgpu ppfeaturemask=0xfff7ffff", // this boot already has it: the same line
		"0xffffbfff":   "",                                        // would be every feature
		"0xffffffff":   "",
		"garbage":      "",
	} {
		must(t, os.WriteFile(amdgpuFeatureMask, []byte(mask), 0o444))
		if got := opts(true, false); got != want {
			t.Errorf("mask %q: %q, want %q", mask, got, want)
		}
		must(t, os.Remove(amdgpuFeatureMask))
	}
	if got := opts(false, true); got != "options it87 ignore_resource_conflict=1" {
		t.Errorf("it87: %q", got)
	}
	if got := opts(false, false); got != "" {
		t.Errorf("both off: %q", got)
	}
}

// Before amdgpu has loaded, the line stays what the booted set carries,
// and a PC with an AMD graphics card gets the kernel's default mask with
// OverDrive; only a PC without one gets no line.
func TestModuleOptionsBeforeAmdgpuLoads(t *testing.T) {
	h := fakeModuleSys(t)
	gpuLine := func() string {
		return strings.Join(h.ModuleOptions(&extensions.Ext{ID: id, Settings: map[string]any{"gpu_fan_curves": true}}), "|")
	}
	pciDevice(t, "0000:00:02.0", "0x8086", "0x030000") // another vendor's graphics
	pciDevice(t, "0000:03:00.1", "0x1002", "0x040300") // an AMD card's HDMI audio
	if got := gpuLine(); got != "" {
		t.Errorf("no AMD display controller: %q", got)
	}

	pciDevice(t, "0000:03:00.0", "0x1002", "0x030000")
	if got := gpuLine(); got != "options amdgpu ppfeaturemask=0xfff7ffff" {
		t.Errorf("an AMD card, amdgpu not loaded: %q", got)
	}

	h.booted = func() []string {
		return []string{"options it87 ignore_resource_conflict=1", "", "options  amdgpu ppfeaturemask=0xfff5ffff"}
	}
	if got := gpuLine(); got != "options amdgpu ppfeaturemask=0xfff5ffff" {
		t.Errorf("the booted set's line: %q", got)
	}
	must(t, os.WriteFile(amdgpuFeatureMask, []byte("4294426623\n"), 0o444))
	if got := gpuLine(); got != "options amdgpu ppfeaturemask=0xfff7ffff" {
		t.Errorf("amdgpu loaded: %q", got)
	}
}

// The booted options are the initramfs's file, then the booted set's.
func TestBootedOptions(t *testing.T) {
	savedState, savedRun, savedModprobe := config.StateDir, config.RunDir, config.ModprobeRunDir
	t.Cleanup(func() { config.StateDir, config.RunDir, config.ModprobeRunDir = savedState, savedRun, savedModprobe })
	root := t.TempDir()
	config.StateDir, config.RunDir, config.ModprobeRunDir = filepath.Join(root, "state"), filepath.Join(root, "run"), filepath.Join(root, "modprobe.d")
	if got := bootedOptions(); len(got) != 0 {
		t.Fatalf("nothing booted: %q", got)
	}
	set := filepath.Join(config.ExtSetsDir(), "4")
	must(t, os.MkdirAll(set, 0o755))
	must(t, os.WriteFile(filepath.Join(set, "ids"), []byte("proton\ncoolercontrol\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(set, "modprobe.conf"), []byte("options amdgpu ppfeaturemask=0xfff7ffff\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(set, "tries"), []byte("0\n"), 0o644))
	must(t, config.WriteJSONAtomic(config.ExtBootPath(), map[string]any{"mode": "enabled", "set": "4"}, 0o644))
	if got := bootedOptions(); !slices.Contains(got, "options amdgpu ppfeaturemask=0xfff7ffff") {
		t.Fatalf("the booted set's: %q", got)
	}
	must(t, os.MkdirAll(config.ModprobeRunDir, 0o755))
	must(t, os.WriteFile(filepath.Join(config.ModprobeRunDir, "vos-ext.conf"), []byte("options amdgpu ppfeaturemask=0xfff5ffff\n"), 0o644))
	if got := bootedOptions(); len(got) == 0 || got[0] != "options amdgpu ppfeaturemask=0xfff5ffff" {
		t.Fatalf("the initramfs's first: %q", got)
	}
}

func TestRemoveStopsAndRestores(t *testing.T) {
	fs := newFakeSys(t)
	fs.chip("3", gpu, map[string]string{"pwm1_enable": "2"})
	must(t, snapshotFans())
	fs.set("3", "pwm1_enable", "1")
	f := newFakeHelper()
	data := filepath.Join(t.TempDir(), "coolercontrol")
	must(t, os.MkdirAll(filepath.Join(data, "config"), 0o700))

	must(t, f.Remove(context.Background(), &extensions.Ext{ID: id, DataDir: data}, false))
	if !slices.Equal(f.systemctl, []string{"stop -- " + unit}) || fs.get("3", "pwm1_enable") != "2" {
		t.Fatalf("systemctl %q, fan %s", f.systemctl, fs.get("3", "pwm1_enable"))
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("removing without purge deleted its settings")
	}
	f.mounted = false
	must(t, f.Remove(context.Background(), &extensions.Ext{ID: id, DataDir: data}, true))
	if len(f.systemctl) != 1 {
		t.Fatalf("stopped a unit that is not there: %q", f.systemctl)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("purge kept its settings")
	}
}

func TestRegistered(t *testing.T) {
	h := extensions.HelperFor(id)
	if _, ok := h.(*helper); !ok {
		t.Fatal("the coolercontrol helper is not registered")
	}
	// vosd finds the optional hook by this method.
	if _, ok := h.(interface {
		PasswordChanged(context.Context, *extensions.Ext) error
	}); !ok {
		t.Fatal("the helper has no PasswordChanged")
	}
}
