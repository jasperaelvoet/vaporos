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

func TestModuleOptions(t *testing.T) {
	saved := amdgpuFeatureMask
	t.Cleanup(func() { amdgpuFeatureMask = saved })
	amdgpuFeatureMask = filepath.Join(t.TempDir(), "ppfeaturemask")
	h := newHelper()
	opts := func(gpu, it87 bool) string {
		return strings.Join(h.ModuleOptions(&extensions.Ext{ID: id, Settings: map[string]any{"gpu_fan_curves": gpu, "it87_conflicts": it87}}), "|")
	}
	if got := opts(true, false); got != "" {
		t.Errorf("no amdgpu loaded: %q", got)
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
	if _, ok := extensions.HelperFor(id).(*helper); !ok {
		t.Fatal("the coolercontrol helper is not registered")
	}
}
