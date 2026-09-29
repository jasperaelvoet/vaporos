package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

func TestLibraryFS(t *testing.T) {
	for fs, want := range map[string]bool{
		"ext4": true, "ext3": true, "ext2": true, "btrfs": true, "xfs": true, "f2fs": true,
		"ntfs": true, "ntfs3": true, // through ntfs3, owned by vapor (uid/gid)
		"exfat": false, "vfat": false, // no POSIX permissions: Proton cannot run from them
		"": false, "swap": false, "iso9660": false, "crypto_LUKS": false,
	} {
		if got := LibraryFS(fs); got != want {
			t.Errorf("LibraryFS(%q) = %v, want %v", fs, got, want)
		}
		if _, probed := probeOptions[fs]; want && !probed {
			t.Errorf("%s is adoptable but never probed for a library", fs)
		}
	}
}

// steamHome gives the gaming user a Steam install whose library list has
// the reference PC's libraries, and returns the list's path.
func steamHome(t *testing.T) string {
	t.Helper()
	list := filepath.Join(config.GamerHome, ".local", "share", "Steam", "steamapps", "libraryfolders.vdf")
	data, err := os.ReadFile("steam/testdata/libraryfolders.vdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(list), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list, data, 0o640); err != nil {
		t.Fatal(err)
	}
	return list
}

func listedIn(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := steam.ParseLibraryFolders(data)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func listDisks(t *testing.T, s *Service) map[string]Disk {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleList(w, httptest.NewRequest("GET", "/api/v1/storage", nil))
	var out struct{ Disks []Disk }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("GET /storage: %d %s", w.Code, w.Body)
	}
	m := map[string]Disk{}
	for _, d := range out.Disks {
		m[d.UUID] = d
	}
	return m
}

const sata500 = "87dbdc4a-1e76-4f22-9ac6-6f98263ca530" // ext4, unmounted, in lsblk-installed.json

// withLibrary makes the scan report sata500 as holding a Steam library at
// its root, and the fake mount put one there (with Steam's marker).
func withLibrary(t *testing.T, s *Service, fs *fakeSystemd) string {
	t.Helper()
	scan := s.scan
	s.scan = func(ctx context.Context) ([]Disk, error) {
		disks, err := scan(ctx)
		for i := range disks {
			if disks[i].UUID == sata500 {
				disks[i].SteamLibrary, disks[i].LibraryDir = true, "."
			}
		}
		return disks, err
	}
	mp := filepath.Join(s.mntBase, "SATA500GB")
	fs.start = func(string) {
		os.MkdirAll(filepath.Join(mp, "steamapps"), 0o755)
		os.WriteFile(filepath.Join(mp, "libraryfolder.vdf"), []byte("\"libraryfolder\"\n{\n\t\"contentid\"\t\t\"77\"\n\t\"label\"\t\t\"\"\n}\n"), 0o644)
	}
	return mp
}

func TestAdoptAddsLibraryToSteam(t *testing.T) {
	s, fs := newTestService(t)
	list := steamHome(t)
	mp := withLibrary(t, s, fs)

	w, out := call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`)
	if w.Code != http.StatusOK || out["registered"] != true || out["registration_pending"] != false || out["library"] != mp {
		t.Fatalf("adopt: %d %v", w.Code, out)
	}
	want := []string{"/home/jasper/.local/share/Steam", "/mnt/SATA500GB", "/mnt/SATA1TB", mp}
	if got := listedIn(t, list); !reflect.DeepEqual(got, want) {
		t.Errorf("Steam's list = %q", got)
	}
	data, _ := os.ReadFile(list)
	if !strings.Contains(string(data), "\"contentid\"\t\t\"77\"") {
		t.Errorf("the library's content id was not kept:\n%s", data)
	}
	if fi, _ := os.Stat(list); fi.Mode().Perm() != 0o640 {
		t.Errorf("list mode changed to %v", fi.Mode().Perm())
	}
	if d := listDisks(t, s)[sata500]; !d.Adopted || !d.Registered || d.RegistrationPending {
		t.Errorf("GET /storage: %+v", d)
	}

	// Adopting again adds nothing twice.
	call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`)
	if got := listedIn(t, list); len(got) != 4 {
		t.Errorf("added twice: %q", got)
	}
}

// Steam writes its list back when it exits, so while it runs the library
// waits, and is added once Steam is seen stopped.
func TestAdoptWaitsForSteamToStop(t *testing.T) {
	s, fs := newTestService(t)
	list := steamHome(t)
	mp := withLibrary(t, s, fs)
	running := true
	s.steamRunning = func() bool { return running }
	before, _ := os.ReadFile(list)

	w, out := call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`)
	if w.Code != http.StatusOK || out["registered"] != false || out["registration_pending"] != true ||
		!strings.Contains(out["hint"].(string), "Add Drive") {
		t.Fatalf("adopt: %d %v", w.Code, out)
	}
	if after, _ := os.ReadFile(list); string(after) != string(before) {
		t.Error("Steam's list changed while Steam runs")
	}
	if d := listDisks(t, s)[sata500]; d.Registered || !d.RegistrationPending {
		t.Errorf("GET /storage while waiting: %+v", d)
	}
	s.applyPending()
	if after, _ := os.ReadFile(list); string(after) != string(before) {
		t.Error("applied while Steam runs")
	}

	running = false
	s.applyPending()
	if got := listedIn(t, list); got[len(got)-1] != mp {
		t.Errorf("not added once Steam stopped: %q", got)
	}
	if d := listDisks(t, s)[sata500]; !d.Registered || d.RegistrationPending {
		t.Errorf("GET /storage after: %+v", d)
	}
	var p pendingLibraries
	if err := config.ReadJSON(pendingPath(), &p); err != nil || len(p.Pending) != 0 {
		t.Errorf("still pending: %+v %v", p, err)
	}
}

// Before Steam's first start there is no list to add to: the library
// waits until there is one.
func TestRegistrationWaitsForSteamsFirstStart(t *testing.T) {
	s, fs := newTestService(t)
	mp := withLibrary(t, s, fs)
	if _, out := call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`); out["registration_pending"] != true {
		t.Fatalf("adopt: %v", out)
	}
	s.applyPending()
	list := steamHome(t) // Steam ran once
	s.applyPending()
	if got := listedIn(t, list); got[len(got)-1] != mp {
		t.Errorf("not added after Steam's first start: %q", got)
	}
}

// Stop using a disk: whatever still waited for Steam is dropped.
func TestRemoveDropsPendingRegistration(t *testing.T) {
	s, fs := newTestService(t)
	list := steamHome(t)
	withLibrary(t, s, fs)
	s.steamRunning = func() bool { return true }
	call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`)
	if w, _ := call(t, s.handleRemove, "DELETE", "/", "", "uuid", sata500); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	s.steamRunning = func() bool { return false }
	s.applyPending()
	if got := listedIn(t, list); len(got) != 3 {
		t.Errorf("a removed disk was added to Steam: %q", got)
	}
}

// A disk with no library gets the folder Steam would make, owned by the
// gaming user, and that folder is registered.
func TestAdoptEmptyDiskMakesLibrary(t *testing.T) {
	s, fs := newTestService(t)
	list := steamHome(t)
	mp := filepath.Join(s.mntBase, "SATA500GB")
	fs.start = func(string) { os.MkdirAll(filepath.Join(mp, "lost+found"), 0o700) }

	w, out := call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`)
	lib := filepath.Join(mp, "SteamLibrary")
	if w.Code != http.StatusOK || out["library"] != lib || out["registered"] != true {
		t.Fatalf("adopt: %d %v", w.Code, out)
	}
	if fi, err := os.Stat(filepath.Join(lib, "steamapps")); err != nil || !fi.IsDir() {
		t.Errorf("no steamapps: %v", err)
	}
	if label, id := steam.ParseLibraryFolder(mustRead(t, filepath.Join(lib, "libraryfolder.vdf"))); label != "" || id != "0" {
		t.Errorf("marker = %q %q", label, id)
	}
	if got := listedIn(t, list); got[len(got)-1] != lib {
		t.Errorf("Steam's list = %q", got)
	}
}

// Steam's files are in the gaming user's home: a symlink there is never
// followed, and whatever it points at stays untouched.
func TestRegistrationRefusesSymlinks(t *testing.T) {
	s, fs := newTestService(t)
	withLibrary(t, s, fs)
	elsewhere := t.TempDir()
	victim := filepath.Join(elsewhere, "libraryfolders.vdf")
	orig, _ := os.ReadFile("steam/testdata/libraryfolders.vdf")
	os.WriteFile(victim, orig, 0o644)
	steamDir := filepath.Join(config.GamerHome, ".local", "share", "Steam")
	os.MkdirAll(steamDir, 0o755)
	if err := os.Symlink(elsewhere, filepath.Join(steamDir, "steamapps")); err != nil {
		t.Fatal(err)
	}
	_, out := call(t, s.handleAdopt, "POST", "/", `{"uuid":"`+sata500+`"}`)
	if out["registered"] != false {
		t.Errorf("registered through a symlink: %v", out)
	}
	if got, _ := os.ReadFile(victim); string(got) != string(orig) {
		t.Error("a file outside the home was changed")
	}
}

func TestGamerSteamRunning(t *testing.T) {
	proc := t.TempDir()
	mk := func(pid, comm string) {
		os.MkdirAll(filepath.Join(proc, pid), 0o755)
		os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(comm+"\n"), 0o644)
	}
	uid := os.Getuid()
	mk("1", "systemd")
	mk("42", "pipewire")
	mk("self", "steam")
	if gamerSteamRunning(proc, uid) {
		t.Error("Steam seen without a Steam process")
	}
	if gamerSteamRunning(proc, uid+1) {
		t.Error("another user's processes counted")
	}
	for _, comm := range []string{"steam", "steamwebhelper", "gamescope"} {
		mk("100", comm)
		if !gamerSteamRunning(proc, uid) {
			t.Errorf("%s not seen", comm)
		}
	}
	if !gamerSteamRunning(filepath.Join(proc, "missing"), uid) {
		t.Error("an unreadable /proc must count as running")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A library the installer adopted is offered to Steam once when vosd
// starts, and once only: if the user later removes it in Steam, it stays
// removed.
func TestInstallerAdoptedLibraryIsOfferedOnce(t *testing.T) {
	s, _ := newTestService(t)
	list := steamHome(t)
	mp := filepath.Join(s.mntBase, "SATA500GB")
	if err := os.MkdirAll(filepath.Join(mp, "steamapps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Mutate(func(c *config.Config) {
		c.Storage.Libraries = append(c.Storage.Libraries, config.Library{UUID: sata500, Label: "SATA500GB", Mountpoint: mp, FSType: "ext4"})
	}); err != nil {
		t.Fatal(err)
	}

	s.seedAdopted()
	if got := listedIn(t, list); got[len(got)-1] != mp {
		t.Fatalf("Steam's list after the first start = %q", got)
	}

	// The user removes it in Steam; later starts leave it alone.
	steamHome(t)
	s.seedAdopted()
	for _, p := range listedIn(t, list) {
		if p == mp {
			t.Errorf("a library the user removed was added again")
		}
	}
}
