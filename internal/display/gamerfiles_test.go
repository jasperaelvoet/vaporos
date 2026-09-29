package display

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// The gaming user (Steam, every game) owns ~/.config/gamescope and
// /run/user/1000/vos; vosd writes there as root. None of these tricks may
// leak a root-only file, write outside the tree, or hang the display.

func TestModesCfgSymlinkToSecret(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	secret := filepath.Join(t.TempDir(), "auth.json")
	const content = "{\"user\":\"admin\",\"hash\":\"$argon2id$v=19$secret\"}\nroot:$6$salt$hash:19000:0:99999:7:::\nVOS VaporOS:1x1@1\n"
	mustWrite(t, secret, content)
	os.MkdirAll(filepath.Dir(ModesCfgPath()), 0o755)
	if err := os.Symlink(secret, ModesCfgPath()); err != nil {
		t.Fatal(err)
	}
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	if !resp.OK {
		t.Fatalf("Begin = %+v %v", resp, h.callLog())
	}
	fi, err := os.Lstat(ModesCfgPath())
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("modes.cfg is not a regular file now: %v %v", fi, err)
	}
	got, _ := os.ReadFile(ModesCfgPath())
	if strings.Contains(string(got), "argon2") || strings.Contains(string(got), "root:") || string(got) != "VOS VaporOS:1280x800@90\n" {
		t.Errorf("modes.cfg = %q", got)
	}
	if b, _ := os.ReadFile(secret); string(b) != content {
		t.Errorf("the link's target changed: %q", b)
	}
}

func TestModesCfgDirectorySymlink(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(config.GamerHome, ".config"), 0o755)
	if err := os.Symlink(outside, filepath.Join(config.GamerHome, ".config", "gamescope")); err != nil {
		t.Fatal(err)
	}
	m.writeModesCfg(ctx, "DP-1", edid.Mode{W: 1920, H: 1080, Refresh: 60}, false)
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("wrote through a symlinked directory: %v", ents)
	}
	// The same one level up.
	os.RemoveAll(filepath.Join(config.GamerHome, ".config"))
	if err := os.Symlink(outside, filepath.Join(config.GamerHome, ".config")); err != nil {
		t.Fatal(err)
	}
	m.writeModesCfg(ctx, "DP-1", edid.Mode{W: 1920, H: 1080, Refresh: 60}, false)
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("wrote through a symlinked ~/.config: %v", ents)
	}
}

func TestModesCfgFIFOAndHugeFile(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	os.MkdirAll(filepath.Dir(ModesCfgPath()), 0o755)
	if err := syscall.Mkfifo(ModesCfgPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		m.writeModesCfg(ctx, "DP-1", edid.Mode{W: 1920, H: 1080, Refresh: 60}, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writeModesCfg blocks on a FIFO")
	}
	if got, _ := os.ReadFile(ModesCfgPath()); string(got) != "VOS VaporOS:1920x1080@60\n" {
		t.Errorf("modes.cfg after a FIFO = %q", got)
	}
	// A huge (sparse) file is not read, just replaced.
	f, err := os.Create(ModesCfgPath())
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("Dell Inc. DELL U2723QE:3840x2160@60\n")
	f.Truncate(1 << 40)
	f.Close()
	m.writeModesCfg(ctx, "DP-1", edid.Mode{W: 2560, H: 1440, Refresh: 120}, false)
	if got, _ := os.ReadFile(ModesCfgPath()); string(got) != "VOS VaporOS:2560x1440@120\n" {
		t.Errorf("modes.cfg after a huge file = %q", got)
	}
}

func TestGamescopeEnvSymlinks(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	outside := t.TempDir()
	// A planted link at the file itself is replaced, its target untouched.
	target := filepath.Join(outside, "target")
	mustWrite(t, target, "keep")
	os.MkdirAll(filepath.Dir(GamescopeEnvPath()), 0o755)
	if err := os.Symlink(target, GamescopeEnvPath()); err != nil {
		t.Fatal(err)
	}
	if err := m.writeGamescopeEnv("DP-1", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("target = %q", b)
	}
	if e := readGamescopeEnv(); e != (gamescopeEnv{"DP-1", true}) {
		t.Errorf("env = %+v", e)
	}
	// A symlinked vos directory is refused.
	os.RemoveAll(filepath.Join(UserRuntimeDir, "vos"))
	if err := os.Symlink(outside, filepath.Join(UserRuntimeDir, "vos")); err != nil {
		t.Fatal(err)
	}
	if err := m.writeGamescopeEnv("DP-1", false); err == nil {
		t.Error("wrote gamescope.env through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "gamescope.env")); err == nil {
		t.Error("gamescope.env created outside the runtime dir")
	}
	if e := readGamescopeEnv(); e != (gamescopeEnv{}) {
		t.Errorf("read through a symlinked directory: %+v", e)
	}
}

func TestUpdateModesCfgDropsForeignLines(t *testing.T) {
	m := edid.Mode{W: 1920, H: 1080, Refresh: 60}
	old := strings.Join([]string{
		`{"user":"admin","hash":"$argon2id$v=19$m=65536"}`,
		"root:$6$salt$hash:19000:0:99999:7:::",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"Dell Inc. DELL U2723QE:3840x2160@60 0",
		"Other:1920x1080@60 trailing",
		"Bad\x01Key:1920x1080@60",
		"LG Electronics LG TV:3840x2160@120",
		"",
	}, "\n")
	got := string(updateModesCfg([]byte(old), []string{"VOS VaporOS"}, m))
	want := "Dell Inc. DELL U2723QE:3840x2160@60 0\nLG Electronics LG TV:3840x2160@120\nVOS VaporOS:1920x1080@60\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	// Bounded, whatever the old file holds.
	var many strings.Builder
	for i := range 1000 {
		many.WriteString("Display " + strings.Repeat("x", i%50) + ":1920x1080@60\n")
	}
	if n := strings.Count(string(updateModesCfg([]byte(many.String()), []string{"VOS VaporOS"}, m)), "\n"); n != maxModesCfgKept+1 {
		t.Errorf("%d lines kept", n)
	}
	// A key that cannot be an entry is not written.
	if got := string(updateModesCfg(nil, []string{"Bad:Key", "Fine Key"}, m)); got != "Fine Key:1920x1080@60\n" {
		t.Errorf("keys = %q", got)
	}
}
