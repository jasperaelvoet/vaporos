package display

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	root := t.TempDir()
	lib := t.TempDir()
	now := time.Now()
	mustWrite(t, filepath.Join(root, "steamapps", "libraryfolders.vdf"), `"libraryfolders"
{
	"0"	{ "path"	"`+root+`" }
	"1"	{ "path"	"`+lib+`" "label" "" }
}`)
	if libs := steamLibraries(root); len(libs) != 2 || libs[1] != lib {
		t.Fatalf("libraries = %v", libs)
	}
	old := filepath.Join(lib, "steamapps", "downloading", "730", "chunk")
	mustWrite(t, old, "x")
	os.Chtimes(old, now.Add(-time.Hour), now.Add(-time.Hour))
	if steamDownloading(root, now) {
		t.Fatal("stale download counted")
	}
	mustWrite(t, filepath.Join(lib, "steamapps", "temp", "730", "part"), "x")
	if !steamDownloading(root, now) {
		t.Fatal("fresh download not detected")
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
	if err != nil || len(c2) != 3 || c2["unknown"].W != 1920 {
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
	if _, ok := c2["Deck"]; ok {
		t.Error("oldest client not evicted")
	}
	os.WriteFile(path, []byte("{broken"), 0o644)
	if _, err := LoadClients(path); err == nil {
		t.Error("broken file accepted")
	}
}
