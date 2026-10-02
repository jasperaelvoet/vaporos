package truckersmp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testSyncer(srv *tmpServer, installed ...string) (*syncer, *bytes.Buffer) {
	var out bytes.Buffer
	s := &syncer{home: homeDir(), client: srv.srv.Client(), progress: &out,
		games: func() []string { return installed }, now: time.Now}
	return s, &out
}

func modFilePath(p string) string { return filepath.Join(homeDir(), filesRel, filepath.FromSlash(p)) }

// A first sync downloads the system files and those of the installed
// games, checks them and writes the manifest; the next one downloads
// nothing; one after TruckersMP changed a file fetches that file alone.
func TestSync(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	s, out := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"core_ets2mp.dll", "data/ets2mp.adb", "ui/ui.zip"} {
		if read(t, modFilePath(p)) != string(srv.files[p]) {
			t.Errorf("%s differs", p)
		}
	}
	for _, p := range []string{"core_atsmp.dll", "launcher/readme.txt"} {
		if _, err := os.Stat(modFilePath(p)); err == nil {
			t.Errorf("%s was downloaded", p)
		}
	}
	m, err := readManifest(homeDir())
	if err != nil || m == nil {
		t.Fatalf("manifest %v %v", m, err)
	}
	if m.Version != "0.7.7.9" || !slices.Equal(m.Games, []string{"ets2"}) || len(m.Files) != 3 || time.Since(m.Checked) > time.Minute {
		t.Errorf("manifest %+v", m)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if last := lines[len(lines)-1]; last != `{"bytes":15000,"total":15000}` {
		t.Errorf("progress %q", lines)
	}
	if err := quickCheck(homeDir(), games[0]); err != nil {
		t.Errorf("quick check after the sync: %v", err)
	}
	if err := quickCheck(homeDir(), games[1]); err == nil {
		t.Error("ATS's files pass without being there")
	}

	// Nothing changed: nothing is downloaded, the manifest is renewed.
	before := srv.getCount("ui/ui.zip")
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.getCount("ui/ui.zip") != before || srv.getCount("core_ets2mp.dll") != 1 {
		t.Errorf("downloaded again: %v", srv.gets)
	}

	// A new mod version changes one file and drops another; ATS is
	// installed meanwhile.
	srv.mu.Lock()
	srv.version = "0.7.8.0"
	srv.files["data/ets2mp.adb"] = bytes.Repeat([]byte("D"), 4000)
	delete(srv.files, "ui/ui.zip")
	srv.add("ui/ui2.zip", "system", []byte("new ui"))
	srv.mu.Unlock()
	s, _ = testSyncer(srv, "ets2", "ats")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.getCount("core_ets2mp.dll") != 1 || srv.getCount("data/ets2mp.adb") != 2 || srv.getCount("core_atsmp.dll") != 1 {
		t.Errorf("gets %v", srv.gets)
	}
	if _, err := os.Stat(modFilePath("ui/ui.zip")); err == nil {
		t.Error("a file the mod dropped is still there")
	}
	m, _ = readManifest(homeDir())
	if m.Version != "0.7.8.0" || !slices.Equal(m.Games, []string{"ets2", "ats"}) {
		t.Errorf("manifest %+v", m)
	}
	for _, g := range games {
		if err := quickCheck(homeDir(), g); err != nil {
			t.Errorf("%s: %v", g.key, err)
		}
	}
}

// A download that breaks off is resumed where it stopped (a Range
// request), by the same sync or the next.
func TestSyncResumes(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	srv.cut["ui/ui.zip"] = 3000
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil {
		t.Fatal("a broken download passed")
	}
	if m, _ := readManifest(homeDir()); m != nil {
		t.Fatal("a manifest after a broken sync")
	}
	part := filepath.Join(homeDir(), partialRel, sum(srv.files["ui/ui.zip"])+".part")
	if fi, err := os.Stat(part); err != nil || fi.Size() != 3000 {
		t.Fatalf("partial download: %v %v", fi, err)
	}
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.ranges != 1 || read(t, modFilePath("ui/ui.zip")) != string(srv.files["ui/ui.zip"]) {
		t.Errorf("ranges %d", srv.ranges)
	}
	if ents, _ := os.ReadDir(filepath.Join(homeDir(), partialRel)); len(ents) != 0 {
		t.Errorf("left over: %v", ents)
	}
}

// A partial download that is not the start of the file (another
// version's leftovers under the same MD5 cannot be, but a damaged one can)
// fails its MD5 and is not kept.
func TestSyncChecksMD5(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	part := filepath.Join(homeDir(), partialRel, sum(srv.files["ui/ui.zip"])+".part")
	write(t, part, "garbage")
	s, _ := testSyncer(srv, "ets2")
	err := s.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "MD5") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(part); err == nil {
		t.Error("a damaged download is kept")
	}
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// files.json and the version API must agree on the core library.
func TestSyncCrossChecksCore(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	srv.badCore = strings.Repeat("0", 32)
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil || !strings.Contains(err.Error(), "core_ets2mp.dll") {
		t.Fatalf("err %v", err)
	}
	if srv.getCount("core_ets2mp.dll") != 0 {
		t.Error("downloaded anyway")
	}
}

// Without an installed game there is nothing to download yet.
func TestSyncWithoutGames(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	s, _ := testSyncer(srv)
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, _ := readManifest(homeDir())
	if m == nil || len(m.Games) != 0 || len(m.Files) != 0 || len(srv.gets) != 0 {
		t.Errorf("manifest %+v, gets %v", m, srv.gets)
	}
}

// One sync at a time.
func TestSyncLock(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	mkdir(t, homeDir())
	unlock, err := lockSync(filepath.Join(homeDir(), lockRel))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); !errors.Is(err, errSyncRunning) {
		t.Fatalf("err %v", err)
	}
}

// Files larger than the cap are refused before a byte is downloaded.
func TestSyncSizeCap(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	saved := maxTotalSize
	maxTotalSize = 10000
	t.Cleanup(func() { maxTotalSize = saved })
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("err %v", err)
	}
	if len(srv.gets) != 0 {
		t.Errorf("gets %v", srv.gets)
	}
}

// The quick check notices a file that changed after the sync.
func TestQuickCheck(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ets2 := games[0]
	if err := quickCheck(homeDir(), ets2); err != nil {
		t.Fatal(err)
	}
	p := modFilePath("data/ets2mp.adb")
	write(t, p, strings.Repeat("x", 5000))
	if err := quickCheck(homeDir(), ets2); !errors.Is(err, errStale) {
		t.Errorf("a changed file passes: %v", err)
	}
	// The core library is also checked by its content.
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	core := modFilePath("core_ets2mp.dll")
	fi, _ := os.Stat(core)
	write(t, core, strings.Repeat("Z", 3000))
	os.Chtimes(core, fi.ModTime(), fi.ModTime())
	if err := quickCheck(homeDir(), ets2); !errors.Is(err, errStale) {
		t.Errorf("a changed core library passes: %v", err)
	}
	os.Remove(filepath.Join(homeDir(), manifestRel))
	if err := quickCheck(homeDir(), ets2); !errors.Is(err, errStale) {
		t.Errorf("no manifest passes: %v", err)
	}
}
