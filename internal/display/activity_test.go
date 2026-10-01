package display

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSteamGameRunning(t *testing.T) {
	proc := t.TempDir()
	write := func(pid, uid, env string) {
		mustWrite(t, filepath.Join(proc, pid, "status"), "Name:\tx\nUid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\n")
		mustWrite(t, filepath.Join(proc, pid, "environ"), env)
	}
	write("100", "1000", "HOME=/var/home/vapor\x00SteamAppId=0\x00")         // Steam itself
	write("101", "0", "SteamAppId=730\x00")                                  // not the gamer
	write("102", "1000", "SteamAppIdX=730\x00STEAM=1\x00SteamAppId=abc\x00") // near misses
	mustWrite(t, filepath.Join(proc, "self", "status"), "Uid:\t1000\n")
	if steamGameRunning(proc, 1000) {
		t.Fatal("no game should be detected")
	}
	write("103", "1000", "DISPLAY=:0\x00SteamAppId=1091500\x00")
	if !steamGameRunning(proc, 1000) {
		t.Fatal("game not detected")
	}
	if steamGameRunning(filepath.Join(proc, "missing"), 1000) {
		t.Fatal("missing /proc")
	}
}

func TestSteamDownloading(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, steamRootRel)
	lib := t.TempDir()
	now := time.Now()
	vdf := filepath.Join(root, "steamapps", "libraryfolders.vdf")
	mustWrite(t, vdf, `"libraryfolders"
{
	"0"	{ "path"	"`+root+`" }
	"1"	{ "path"	"`+lib+`" "label" "" }
}`)
	if libs := steamLibraries(home); len(libs) != 2 || libs[0] != root || libs[1] != lib {
		t.Fatalf("libraries = %v", libs)
	}
	old := filepath.Join(lib, "steamapps", "downloading", "730", "chunk")
	mustWrite(t, old, "x")
	os.Chtimes(old, now.Add(-time.Hour), now.Add(-time.Hour))
	if steamDownloading(home, now) {
		t.Fatal("stale download counted")
	}
	mustWrite(t, filepath.Join(lib, "steamapps", "temp", "730", "part"), "x")
	if !steamDownloading(home, now) {
		t.Fatal("fresh download not detected")
	}
}

// TestSteamLibrariesHostileFile: libraryfolders.vdf belongs to the gaming
// user. A symlink to another file, a FIFO or a huge file must neither be
// followed, hang vosd nor be read whole: only the Steam root is left.
func TestSteamLibrariesHostileFile(t *testing.T) {
	home := t.TempDir()
	vdf := filepath.Join(home, steamRootRel, "steamapps", "libraryfolders.vdf")
	elsewhere := filepath.Join(t.TempDir(), "secret.vdf")
	mustWrite(t, elsewhere, `"libraryfolders" { "1" { "path" "/secret" } }`)
	only := func(what string) {
		t.Helper()
		done := make(chan []string, 1)
		go func() { done <- steamLibraries(home) }()
		select {
		case libs := <-done:
			if len(libs) != 1 {
				t.Errorf("%s: libraries = %v", what, libs)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: steamLibraries hangs", what)
		}
	}
	os.MkdirAll(filepath.Dir(vdf), 0o755)
	if err := os.Symlink(elsewhere, vdf); err != nil {
		t.Fatal(err)
	}
	only("symlink")
	os.Remove(vdf)
	if err := syscall.Mkfifo(vdf, 0o644); err != nil {
		t.Fatal(err)
	}
	only("fifo")
	os.Remove(vdf)
	f, err := os.Create(vdf)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`"libraryfolders" { "1" { "path" "/big" } }`)
	f.Truncate(maxVDF + 1) // sparse
	f.Close()
	only("oversized")
	// A symlinked directory on the way is refused as well.
	os.RemoveAll(filepath.Join(home, ".local"))
	os.MkdirAll(filepath.Join(home, ".local"), 0o755)
	realSteam := filepath.Join(t.TempDir(), "Steam")
	mustWrite(t, filepath.Join(realSteam, "steamapps", "libraryfolders.vdf"), `"libraryfolders" { "1" { "path" "/linked" } }`)
	if err := os.Symlink(filepath.Dir(realSteam), filepath.Join(home, ".local", "share")); err != nil {
		t.Fatal(err)
	}
	only("symlinked directory")
}

// TestSteamGameRunningHugeEnviron: a game can make its environment huge;
// the scan is bounded and still finds SteamAppId past a very long variable.
func TestSteamGameRunningHugeEnviron(t *testing.T) {
	proc := t.TempDir()
	mustWrite(t, filepath.Join(proc, "200", "status"), "Uid:\t1000\t1000\t1000\t1000\n")
	long := "JUNK=" + strings.Repeat("SteamAppId=1", 10000)
	mustWrite(t, filepath.Join(proc, "200", "environ"), long+"\x00SteamAppId=440\x00")
	if !steamGameRunning(proc, 1000) {
		t.Error("SteamAppId after a long variable not found")
	}
	mustWrite(t, filepath.Join(proc, "200", "environ"), long+"\x00")
	if steamGameRunning(proc, 1000) {
		t.Error("a long variable's tail was taken for SteamAppId")
	}
	mustWrite(t, filepath.Join(proc, "200", "environ"), strings.Repeat("A=1\x00", maxEnviron/4+10)+"SteamAppId=440\x00")
	if steamGameRunning(proc, 1000) {
		t.Error("read past the environ bound")
	}
}

func TestSunshineStreaming(t *testing.T) {
	state := "SUNSHINE_SERVER_FREE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<root><state>" + state + "</state></root>"))
	}))
	defer srv.Close()
	save := SunshineServerInfo
	defer func() { SunshineServerInfo = save }()
	SunshineServerInfo = srv.URL
	if sunshineStreaming(context.Background()) {
		t.Fatal("free server reported busy")
	}
	state = "SUNSHINE_SERVER_BUSY"
	if !sunshineStreaming(context.Background()) {
		t.Fatal("busy server not detected")
	}
	SunshineServerInfo = "http://127.0.0.1:1/serverinfo"
	if sunshineStreaming(context.Background()) {
		t.Fatal("unreachable Sunshine reported busy")
	}
}

func TestClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(path)
	if err != nil || len(c) != 0 {
		t.Fatalf("missing file: %v %v", c, err)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Record("Deck", ClientMode{W: 1280, H: 800, FPS: 90, LastSeen: t0})
	c.Record("", ClientMode{W: 1920, H: 1080, FPS: 60, LastSeen: t0.Add(time.Hour)})
	c.Record("Mac", ClientMode{W: 1280, H: 800, FPS: 90, LastSeen: t0.Add(2 * time.Hour)})
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadClients(path)
	if err != nil || len(c2) != 3 || c2["unknown 1920x1080@60"].Name != "unknown" {
		t.Fatalf("reload = %+v %v", c2, err)
	}
	modes := c2.Modes()
	if len(modes) != 2 || modes[0].String() != "1280x800@90" || modes[1].String() != "1920x1080@60" {
		t.Errorf("modes = %v", modes)
	}
	for i := 0; i < maxClients+5; i++ {
		c2.Record(string(rune('A'+i%26))+time.Duration(i).String(), ClientMode{W: 800, H: 600, FPS: 60, LastSeen: t0.Add(time.Duration(i+10) * time.Hour)})
	}
	if len(c2) != maxClients {
		t.Errorf("not bounded: %d", len(c2))
	}
	if _, ok := c2["Deck 1280x800@90"]; ok {
		t.Error("oldest client not evicted")
	}

	// Every device that streams as "Moonlight" keeps its own mode learned.
	c3 := Clients{}
	c3.Record("Moonlight", ClientMode{W: 2560, H: 1664, FPS: 60, LastSeen: t0})
	c3.Record("Moonlight", ClientMode{W: 2532, H: 1170, FPS: 60, LastSeen: t0.Add(time.Hour)})
	c3.Record("Moonlight", ClientMode{W: 2560, H: 1664, FPS: 60, LastSeen: t0.Add(2 * time.Hour)})
	if modes := c3.Modes(); len(modes) != 2 || modes[0].String() != "2560x1664@60" || modes[1].String() != "2532x1170@60" {
		t.Errorf("modes = %v", modes)
	}
	if d := c3.devices(); len(d) != 2 || d[0].Name != "Moonlight" || d[0].Mode != "2560x1664@60" {
		t.Errorf("devices = %+v", d)
	}

	// A file from before names were stored moves to per-mode keys.
	os.WriteFile(path, []byte(`{"Deck":{"w":1280,"h":800,"fps":90,"hdr":false,"last_seen":"2026-01-01T00:00:00Z"}}`), 0o644)
	c4, err := LoadClients(path)
	if err != nil || len(c4) != 1 || c4["Deck 1280x800@90"].Name != "Deck" {
		t.Fatalf("old file = %+v %v", c4, err)
	}
	c4.Record("Deck", ClientMode{W: 1920, H: 1080, FPS: 60, LastSeen: t0.Add(time.Hour)})
	if len(c4.Modes()) != 2 {
		t.Errorf("old mode lost: %+v", c4)
	}

	os.WriteFile(path, []byte("{broken"), 0o644)
	if _, err := LoadClients(path); err == nil {
		t.Error("broken file accepted")
	}
}
