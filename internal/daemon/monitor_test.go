package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// fakeDRM builds a /sys/class/drm-like tree: name → status ("" = no
// status file).
func fakeDRM(t *testing.T, conns map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"card0", "card1", "renderD128", "version"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, status := range conns {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if status != "" {
			if err := os.WriteFile(filepath.Join(p, "status"), []byte(status+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func TestMonitorState(t *testing.T) {
	cases := []struct {
		name             string
		conns            map[string]string
		args             []string
		connected, known bool
	}{
		{"no driver yet", nil, nil, false, false},
		{"headless AMD", map[string]string{"card1-DP-1": "disconnected", "card1-DP-2": "disconnected", "card1-HDMI-A-1": "disconnected"}, nil, false, true},
		{"monitor on HDMI", map[string]string{"card1-DP-1": "disconnected", "card1-HDMI-A-1": "connected"}, nil, true, true},
		{"second GPU's monitor", map[string]string{"card0-DP-1": "disconnected", "card1-eDP-1": "connected"}, nil, true, true},
		{"writeback is not a monitor", map[string]string{"card0-Writeback-1": "connected", "card0-DP-1": "disconnected"}, nil, false, true},
		{"forced virtual display is not a monitor", map[string]string{"card1-DP-1": "connected", "card1-DP-2": "disconnected"},
			[]string{"quiet", "video=DP-1:e", "drm.edid_firmware=DP-1:edid/vaporos.bin"}, false, true},
		{"forced one plus a real monitor", map[string]string{"card1-DP-1": "connected", "card1-DP-2": "connected"}, []string{"video=DP-1:e"}, true, true},
		{"a mode without e does not force", map[string]string{"card1-DP-1": "connected"}, []string{"video=DP-1:1920x1080@60"}, true, true},
		{"a VM's virtual GPU shows the code", map[string]string{"card0-Virtual-1": "connected"}, nil, true, true},
		{"unknown status is not connected", map[string]string{"card0-DP-1": "unknown"}, nil, false, true},
		{"unreadable status", map[string]string{"card0-DP-1": ""}, nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			connected, known := monitorState(fakeDRM(t, c.conns), c.args)
			if connected != c.connected || known != c.known {
				t.Fatalf("monitorState = %v, %v; want %v, %v", connected, known, c.connected, c.known)
			}
		})
	}
	if connected, known := monitorState(filepath.Join(t.TempDir(), "missing"), nil); connected || known {
		t.Fatal("a missing sysfs dir must be unknown")
	}
}

func TestHeadlessFollowsHotplug(t *testing.T) {
	dir := fakeDRM(t, map[string]string{"card1-DP-1": "disconnected", "card1-HDMI-A-1": "disconnected"})
	old := drmClassDir
	drmClassDir = dir
	t.Cleanup(func() { drmClassDir = old })
	if !headless() {
		t.Fatal("no monitor: not headless")
	}
	if err := os.WriteFile(filepath.Join(dir, "card1-HDMI-A-1", "status"), []byte("connected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if headless() {
		t.Fatal("monitor plugged in: still headless")
	}
}

func TestAnnouncedCodeFollowsTheWaiver(t *testing.T) {
	dirs(t)
	srv := api.New(api.Options{Installer: true})
	srv.SetSetupCode("ABCD-EFGH")
	waived := false
	srv.SetSetupWaiver(func() bool { return waived })
	if got := announcedCode(srv); got != "ABCD-EFGH" {
		t.Fatalf("monitor attached: code=%q", got)
	}
	waived = true
	if got := announcedCode(srv); got != "" {
		t.Fatalf("waived: code=%q, want none (printed -)", got)
	}
	if got := readyLine("installer", "v", "10.0.0.2", announcedCode(srv)); got != "VOS-READY mode=installer version=v ip=10.0.0.2 code=-" {
		t.Fatalf("line %q", got)
	}
}
