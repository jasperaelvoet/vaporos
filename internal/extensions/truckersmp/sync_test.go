package truckersmp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

func testSyncer(srv *tmpServer, installed ...string) (*syncer, *bytes.Buffer) {
	var out bytes.Buffer
	s := &syncer{home: homeDir(), client: srv.srv.Client(), progress: &out,
		games: func() []string { return installed }, now: time.Now,
		free: func(string) (int64, error) { return 1 << 40, nil }}
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

// Without HEAD, a part an earlier sync downloaded whole is asked for the
// bytes after it, which the server answers 416 "bytes */<its size>": the
// part is kept for its MD5 to decide, not downloaded again.
func TestSyncKeepsWholePartWithoutHead(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	srv.noHead = true
	srv.cut["ui/ui.zip"] = 3000 // the last download breaks off
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil {
		t.Fatal("a broken download passed")
	}
	for _, p := range []string{"core_ets2mp.dll", "data/ets2mp.adb"} {
		part := filepath.Join(homeDir(), partialRel, sum(srv.files[p])+".part")
		if fi, err := os.Stat(part); err != nil || fi.Size() != int64(len(srv.files[p])) {
			t.Fatalf("%s: %v %v", p, fi, err)
		}
	}
	s, out := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One Range request each, and no download from the start.
	for _, p := range []string{"core_ets2mp.dll", "data/ets2mp.adb", "ui/ui.zip"} {
		if n := srv.getCount(p); n != 2 {
			t.Errorf("%s: %d GETs", p, n)
		}
		if read(t, modFilePath(p)) != string(srv.files[p]) {
			t.Errorf("%s differs", p)
		}
	}
	if srv.ranges != 3 || !strings.HasSuffix(out.String(), "{\"bytes\":15000,\"total\":15000}\n") {
		t.Errorf("ranges %d, progress %q", srv.ranges, out.String())
	}
	if err := quickCheck(homeDir(), games[0]); err != nil {
		t.Error(err)
	}
	if ents, _ := os.ReadDir(filepath.Join(homeDir(), partialRel)); len(ents) != 0 {
		t.Errorf("left over: %v", ents)
	}

	// A 416 that sizes the file otherwise starts it over.
	newBox(t)
	part := filepath.Join(homeDir(), partialRel, sum(srv.files["ui/ui.zip"])+".part")
	write(t, part, string(srv.files["ui/ui.zip"])+"tail")
	before := srv.getCount("ui/ui.zip")
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := srv.getCount("ui/ui.zip") - before; n != 2 || read(t, modFilePath("ui/ui.zip")) != string(srv.files["ui/ui.zip"]) {
		t.Errorf("%d GETs", n)
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
	if err := s.run(context.Background()); !errors.Is(err, errUnchecked) || !strings.Contains(err.Error(), "core_ets2mp.dll") {
		t.Fatalf("err %v", err)
	}
	if srv.getCount("core_ets2mp.dll") != 0 {
		t.Error("downloaded anyway")
	}
}

// Without the API's checksum for a wanted game's core library nothing is
// downloaded; a game nobody wants needs none.
func TestSyncNeedsCoreChecksum(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	srv.noCore = true
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); !errors.Is(err, errUnchecked) || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("err %v", err)
	}
	if len(srv.gets) != 0 {
		t.Errorf("gets %v", srv.gets)
	}
	s, _ = testSyncer(srv, "ats")
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("ATS alone: %v", err)
	}
}

// A file of no bytes is downloaded as one, and kept by the next sync.
func TestSyncEmptyFile(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	srv.add("data/empty.txt", "ets2", nil)
	s, _ := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(modFilePath("data/empty.txt")); err != nil || fi.Size() != 0 {
		t.Fatalf("%v %v", fi, err)
	}
	if err := quickCheck(homeDir(), games[0]); err != nil {
		t.Error(err)
	}
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil || srv.getCount("data/empty.txt") != 1 {
		t.Errorf("%v, gets %v", err, srv.gets)
	}
	// Also when the server does not answer HEAD.
	os.Remove(modFilePath("data/empty.txt"))
	srv.noHead = true
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(modFilePath("data/empty.txt")); err != nil {
		t.Error(err)
	}
}

// A server that refuses HEAD: the GET's length gives the file's size,
// for the progress total and the bounds.
func TestSyncWithoutHead(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	srv.noHead = true
	s, out := testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.total != 15000 || !strings.HasSuffix(out.String(), "{\"bytes\":15000,\"total\":15000}\n") {
		t.Errorf("total %d, progress %q", s.total, out.String())
	}
	if lines := progressLines(t, out.String()); slices.ContainsFunc(lines[:len(lines)-1], func(p [2]int64) bool { return p[1] != 0 }) {
		t.Errorf("a total before the end: %v", lines)
	}

	// A file over the cap is refused before a byte of it is written.
	newBox(t)
	saved := [2]int64{maxFileSize, maxTotalSize}
	t.Cleanup(func() { maxFileSize, maxTotalSize = saved[0], saved[1] })
	maxFileSize = 6000
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir(), partialRel, sum(srv.files["ui/ui.zip"])+".part")); err == nil {
		t.Error("a file over the cap was written")
	}

	// So is a total over the cap.
	newBox(t)
	maxFileSize, maxTotalSize = saved[0], 10000
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir(), partialRel, sum(srv.files["ui/ui.zip"])+".part")); err == nil || s.done > 10000 {
		t.Errorf("done %d", s.done)
	}

	// Without any length the bytes themselves are counted.
	newBox(t)
	srv.chunked = true
	s, _ = testSyncer(srv, "ets2")
	if err := s.run(context.Background()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("err %v", err)
	}
	if s.done > 10000 {
		t.Errorf("downloaded %d bytes", s.done)
	}
}

// progressLines reads a sync's {"bytes","total"} lines.
func progressLines(t *testing.T, out string) [][2]int64 {
	t.Helper()
	var lines [][2]int64
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var p struct{ Bytes, Total int64 }
		if err := json.Unmarshal([]byte(l), &p); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		lines = append(lines, [2]int64{p.Bytes, p.Total})
	}
	return lines
}

// The progress total is the whole download's, known before the first
// byte from HEAD's sizes; when HEAD gives none it is 0 (bytes without a
// percentage) until the end, never a total that grows file by file.
func TestSyncProgressTotal(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	s, out := testSyncer(srv, "ets2")
	tick := time.Now()
	s.now = func() time.Time { tick = tick.Add(time.Second); return tick } // every line printed
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := progressLines(t, out.String())
	if len(lines) < 4 || lines[0] != [2]int64{0, 15000} || lines[len(lines)-1] != [2]int64{15000, 15000} {
		t.Fatalf("lines %v", lines)
	}
	for i, p := range lines {
		if p[1] != 15000 || (i > 0 && p[0] < lines[i-1][0]) {
			t.Errorf("lines %v", lines)
			break
		}
	}

	newBox(t)
	srv.noHead = true
	s, out = testSyncer(srv, "ets2")
	s.now = func() time.Time { tick = tick.Add(time.Second); return tick }
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines = progressLines(t, out.String())
	if len(lines) < 4 || lines[len(lines)-1] != [2]int64{15000, 15000} {
		t.Fatalf("lines %v", lines)
	}
	for _, p := range lines[:len(lines)-1] {
		if p[1] != 0 {
			t.Errorf("a total that is not the whole download's: %v", lines)
			break
		}
	}
}

// A sync that is refused or stops writes only into partial/: the files
// and the manifest stay as they were, so multiplayer keeps starting.
func TestSyncRefusalKeepsFiles(t *testing.T) {
	b := syncedBox(t, "ets2")
	b.install(b.disk, games[0])
	b.install(b.steam, games[1])
	srv := newTMPServer(t)
	ctx := context.Background()
	before := read(t, filepath.Join(homeDir(), manifestRel))
	stays := func(what string) {
		t.Helper()
		if got := read(t, filepath.Join(homeDir(), manifestRel)); got != before {
			t.Errorf("%s: the manifest changed", what)
		}
		if err := quickCheck(homeDir(), games[0]); err != nil {
			t.Errorf("%s: ETS2's files: %v", what, err)
		}
		m, started, told := testMP(t, shortcutProcs(t))
		if err := m.run(ctx, games[0]); err != nil || len(*started) != 1 {
			t.Errorf("%s: mp ets2: %v %q", what, err, *told)
		}
		os.Remove(flagPath())
	}

	// ATS's files do not fit: sized by HEAD, or by the GET when HEAD is
	// refused.
	var space *spaceError
	for _, noHead := range []bool{false, true} {
		srv.noHead = noHead
		s, _ := testSyncer(srv, "ets2", "ats")
		s.free = func(string) (int64, error) { return store.ExtReserve + 100, nil }
		if err := s.run(ctx); !errors.As(err, &space) || space.short != 1900 {
			t.Fatalf("no space (HEAD refused: %v): %v", noHead, err)
		}
		stays("no space")
	}
	srv.noHead = false

	// A new version of a file, whose download breaks off.
	srv.mu.Lock()
	srv.version = "0.7.8.0"
	srv.files["data/ets2mp.adb"] = bytes.Repeat([]byte("D"), 4000)
	srv.cut["data/ets2mp.adb"] = 1000
	srv.mu.Unlock()
	s, _ := testSyncer(srv, "ets2", "ats")
	if err := s.run(ctx); err == nil {
		t.Fatal("a broken download passed")
	}
	stays("a broken download")

	// Over the size cap.
	saved := maxTotalSize
	maxTotalSize = 1000
	s, _ = testSyncer(srv, "ets2", "ats")
	if err := s.run(ctx); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("over the cap: %v", err)
	}
	maxTotalSize = saved
	stays("over the cap")

	// The next sync that finishes moves everything into place.
	s, _ = testSyncer(srv, "ets2", "ats")
	if err := s.run(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if err := quickCheck(homeDir(), g); err != nil {
			t.Errorf("%s: %v", g.key, err)
		}
	}
	if read(t, modFilePath("data/ets2mp.adb")) != string(srv.files["data/ets2mp.adb"]) {
		t.Error("the new file is not in place")
	}
}

// TruckersMP lists some files for both games under one MD5: each
// downloads once, and every file gets its own copy.
func TestSyncSharedDownload(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	logo := bytes.Repeat([]byte("L"), 1500)
	srv.add("data/ats/ui/logo.png", "ats", logo)
	srv.add("data/ets2/ui/logo.png", "ets2", logo)
	s, out := testSyncer(srv, "ets2", "ats")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := srv.getCount("data/ats/ui/logo.png") + srv.getCount("data/ets2/ui/logo.png"); n != 1 {
		t.Errorf("downloaded %d times", n)
	}
	a, err1 := os.Stat(modFilePath("data/ats/ui/logo.png"))
	e, err2 := os.Stat(modFilePath("data/ets2/ui/logo.png"))
	if err1 != nil || err2 != nil || os.SameFile(a, e) ||
		read(t, modFilePath("data/ats/ui/logo.png")) != string(logo) || read(t, modFilePath("data/ets2/ui/logo.png")) != string(logo) {
		t.Fatalf("%v %v", err1, err2)
	}
	for _, g := range games {
		if err := quickCheck(homeDir(), g); err != nil {
			t.Errorf("%s: %v", g.key, err)
		}
	}
	if !strings.HasSuffix(out.String(), "{\"bytes\":18500,\"total\":18500}\n") {
		t.Errorf("progress %q", out.String())
	}
	if ents, _ := os.ReadDir(filepath.Join(homeDir(), partialRel)); len(ents) != 0 {
		t.Errorf("left over: %v", ents)
	}
}

// The copies of a shared download count in the free space: up front when
// HEAD sizes it, so nothing is downloaded, and once it is downloaded when
// only the GET does.
func TestSyncSharedDownloadSpace(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	logo := bytes.Repeat([]byte("L"), 1500)
	srv.add("data/ats/ui/logo.png", "ats", logo)
	srv.add("data/ets2/ui/logo.png", "ets2", logo)
	s, _ := testSyncer(srv, "ets2", "ats")
	s.free = func(string) (int64, error) { return store.ExtReserve + 18500, nil } // the downloads, not the copy
	var space *spaceError
	if err := s.run(context.Background()); !errors.As(err, &space) || space.short != 1500 {
		t.Fatalf("err %v", err)
	}
	if len(srv.gets) != 0 {
		t.Errorf("gets %v", srv.gets)
	}

	srv.noHead = true
	s, _ = testSyncer(srv, "ets2", "ats")
	s.free = func(string) (int64, error) { return store.ExtReserve + 18500 + 1499 - s.done, nil }
	if err := s.run(context.Background()); !errors.As(err, &space) || space.short != 1 {
		t.Fatalf("sized by the GET: %v", err)
	}
	if m, _ := readManifest(homeDir()); m != nil {
		t.Error("a manifest after a refused sync")
	}
	if _, err := os.Stat(modFilePath("data/ats/ui/logo.png")); err == nil {
		t.Error("a file was placed")
	}
}

// A sync that does not fit with store.ExtReserve to spare downloads
// nothing, counting what an earlier sync left; one that learns a size
// from the GET checks again.
func TestSyncFreeSpace(t *testing.T) {
	newBox(t)
	srv := newTMPServer(t)
	s, _ := testSyncer(srv, "ets2")
	var asked string
	s.free = func(p string) (int64, error) { asked = p; return store.ExtReserve + 1000, nil }
	var space *spaceError
	if err := s.run(context.Background()); !errors.As(err, &space) || space.short != 14000 {
		t.Fatalf("err %v", err)
	}
	if asked != homeDir() || len(srv.gets) != 0 {
		t.Errorf("asked %q, gets %v", asked, srv.gets)
	}
	write(t, filepath.Join(homeDir(), partialRel, sum(srv.files["ui/ui.zip"])+".part"), string(srv.files["ui/ui.zip"][:3000]))
	s, _ = testSyncer(srv, "ets2")
	s.free = func(string) (int64, error) { return store.ExtReserve + 1000, nil }
	if err := s.run(context.Background()); !errors.As(err, &space) || space.short != 11000 {
		t.Fatalf("with a partial download: %v", err)
	}

	newBox(t)
	srv.noHead = true
	s, _ = testSyncer(srv, "ets2")
	s.free = func(string) (int64, error) { return store.ExtReserve + 5000, nil }
	if err := s.run(context.Background()); !errors.As(err, &space) || space.short != 2000 {
		t.Fatalf("sized by the GET: %v", err)
	}

	// Free space nobody can tell passes.
	s, _ = testSyncer(srv, "ets2")
	s.free = func(string) (int64, error) { return -1, errors.New("statfs") }
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// `sync` tells vosd by its exit code what the card says by itself.
func TestSyncExitCodes(t *testing.T) {
	b := newBox(t)
	b.install(b.disk, games[0])
	srv := newTMPServer(t)
	srv.noCore = true
	var out, errb bytes.Buffer
	if code := runCLI([]string{"sync"}, &out, &errb); code != exitUnchecked {
		t.Fatalf("no checksum: %d %s", code, errb.String())
	}
	srv.noCore = false
	saved := freeSpace
	t.Cleanup(func() { freeSpace = saved })
	freeSpace = func(string) (int64, error) { return 0, nil }
	out.Reset()
	if code := runCLI([]string{"sync"}, &out, &errb); code != exitNoSpace {
		t.Fatalf("no space: %d %s", code, errb.String())
	}
	if want := fmt.Sprintf("{\"short\":%d}\n", 15000+store.ExtReserve); !strings.HasSuffix(out.String(), want) {
		t.Errorf("stdout %q", out.String())
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
