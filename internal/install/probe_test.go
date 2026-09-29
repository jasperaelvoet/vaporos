package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage"
)

func TestFindSteamLibraries(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
	}
	mk("SteamLibrary/libraryfolder.vdf")
	mk("SteamLibrary/steamapps/common/Game/x")
	mk("Program Files (x86)/Steam/steamapps/appmanifest_228980.acf")
	mk("Games/NotSteam/steamapps/readme.txt") // no manifests: not a library
	mk(".Trash/Old/libraryfolder.vdf")        // hidden: skipped
	mk("deep/a/b/libraryfolder.vdf")          // too deep
	got := strings.Join(findSteamLibraries(root), "|")
	if want := "/Program Files (x86)/Steam|/SteamLibrary"; got != want {
		t.Errorf("findSteamLibraries = %q, want %q", got, want)
	}

	// The reference machine: the library is the filesystem root.
	root2 := t.TempDir()
	os.WriteFile(filepath.Join(root2, "libraryfolder.vdf"), nil, 0o644)
	os.MkdirAll(filepath.Join(root2, "steamapps"), 0o755)
	if got := findSteamLibraries(root2); len(got) != 1 || got[0] != "/" {
		t.Errorf("root library = %v", got)
	}
	if got := findSteamLibraries(filepath.Join(root, "missing")); len(got) != 0 {
		t.Errorf("missing dir = %v", got)
	}
}

func TestGuessTimezone(t *testing.T) {
	f := newFakeSys(t)
	if got := guessTimezone(); got != "UTC" {
		t.Errorf("no localtime: %q", got)
	}
	for target, want := range map[string]string{
		"../usr/share/zoneinfo/Europe/Brussels":      "Europe/Brussels",
		"/usr/share/zoneinfo/posix/America/New_York": "America/New_York",
		"/usr/share/zoneinfo/../../../etc/shadow":    "UTC",
		"/somewhere/else":                            "UTC",
	} {
		f.symlink(target, "etc/localtime")
		if got := guessTimezone(); got != want {
			t.Errorf("localtime -> %s: %q, want %q", target, got, want)
		}
	}
}

func probeMachine(t *testing.T) (*fakeSys, *fakeRunner, *Service) {
	f := newFakeSys(t)
	f.addDisk("nvme0n1", "259:0", "nvme", 500*gib, "")
	for i, label := range partNames {
		f.addPart("nvme0n1", fmt.Sprintf("nvme0n1p%d", i+1), fmt.Sprintf("259:%d", i+1), i+1, label, 8*gib)
	}
	f.addDisk("sda", "8:0", "ata1", 1000*gib, "SATA1TB")
	f.addPart("sda", "sda1", "8:1", 1, "", 1000*gib)
	f.addDisk("sdb", "8:16", "usb1", 16*gib, "Stick")
	f.addPart("sdb", "sdb2", "8:18", 2, "", 1*gib)
	f.addDisk("sdc", "8:32", "ata2", 2000*gib, "Windows HDD")
	f.addPart("sdc", "sdc2", "8:34", 2, "Basic data partition", 2000*gib)
	f.addVirtual("loop0", "7:0")
	f.setMounts("8:16 " + f.mediumMount() + " iso9660 /dev/sdb")

	mounted := t.TempDir()
	os.WriteFile(filepath.Join(mounted, "libraryfolder.vdf"), nil, 0o644)
	f.scan = []storage.Disk{
		{Path: "/dev/nvme0n1", Size: 500 * gib, Transport: "nvme"},
		{Path: "/dev/nvme0n1p4", Parent: "/dev/nvme0n1", UUID: "data-uuid", Label: "vos_data", FSType: "ext4"},
		{Path: "/dev/sda", Model: "SATA1TB", Size: 1000 * gib, Transport: "sata"},
		{Path: "/dev/sda1", Parent: "sda", UUID: "87dbdc4a", Label: "SATA1TB", FSType: "ext4", MountedAt: mounted},
		{Path: "/dev/sdb", Model: "Stick", Size: 16 * gib, Transport: "usb", Removable: true, UUID: "2026-09-29", FSType: "iso9660"},
		{Path: "/dev/sdb2", Parent: "sdb", UUID: "live-esp", FSType: "vfat"},
		{Path: "/dev/sdc", Model: "Windows HDD", Size: 2000 * gib, Transport: "sata"},
		{Path: "/dev/sdc2", Parent: "sdc", UUID: "01D9ABCDEF", Label: "Games", FSType: "ntfs"},
		{Path: "/dev/loop0", UUID: "erofs-uuid", FSType: "erofs"},
	}
	r := f.runner()
	// Mounting the NTFS disk shows a Windows Steam library.
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "mount" && strings.Contains(strings.Join(args, " "), "/probe/") {
			dir := args[len(args)-1]
			os.MkdirAll(filepath.Join(dir, "SteamLibrary"), 0o755)
			os.WriteFile(filepath.Join(dir, "SteamLibrary/libraryfolder.vdf"), nil, 0o644)
		}
		if name == "umount" {
			os.RemoveAll(filepath.Join(args[len(args)-1], "SteamLibrary"))
		}
		return "", nil, false
	}
	s := NewService(config.Defaults())
	s.env = f.env(r)
	s.reboot = func(context.Context) error { return nil }
	return f, r, s
}

func TestProbe(t *testing.T) {
	f, r, s := probeMachine(t)
	res := s.probe(context.Background(), "", "")

	if len(res.Disks) != 4 {
		t.Fatalf("disks = %+v", res.Disks)
	}
	by := map[string]ProbeDisk{}
	for _, d := range res.Disks {
		by[d.Path] = d
		if d.SteamLibraries == nil {
			t.Errorf("%s: steam_libraries is null", d.Path)
		}
	}
	nv := by["/dev/nvme0n1"]
	if !nv.HasVaporOS || nv.IsLive || nv.Transport != "nvme" || len(nv.SteamLibraries) != 0 {
		t.Errorf("nvme0n1 = %+v", nv)
	}
	sda := by["/dev/sda"]
	if sda.HasVaporOS || sda.Model != "SATA1TB" || len(sda.SteamLibraries) != 1 ||
		sda.SteamLibraries[0] != (SteamLibrary{UUID: "87dbdc4a", Label: "SATA1TB", Path: "/"}) {
		t.Errorf("sda = %+v", sda)
	}
	sdb := by["/dev/sdb"]
	if !sdb.IsLive || !sdb.Removable || len(sdb.SteamLibraries) != 0 {
		t.Errorf("sdb = %+v", sdb)
	}
	sdc := by["/dev/sdc"]
	if len(sdc.SteamLibraries) != 1 || sdc.SteamLibraries[0].Path != "/SteamLibrary" || sdc.SteamLibraries[0].UUID != "01D9ABCDEF" {
		t.Errorf("sdc = %+v", sdc)
	}
	if res.Timezone != "UTC" || !res.GPU.Supported || res.IPs == nil {
		t.Errorf("probe = %+v", res)
	}

	// The unmounted NTFS partition was mounted read-only with ntfs3 and
	// unmounted again; the live disk and the VaporOS data were not touched.
	assertSubsequence(t, r.relCalls(), []string{
		"mount -t ntfs3 -o ro,nosuid,nodev,noexec /dev/sdc2 @/run/vos/probe/01D9ABCDEF",
		"umount @/run/vos/probe/01D9ABCDEF",
	})
	if len(r.callList()) != 2 || len(r.mounted) != 0 {
		t.Errorf("probe ran %v, mounted %v", r.callList(), r.mounted)
	}
	if exists(f.path("run/vos/probe/01D9ABCDEF")) {
		t.Error("probe mount point left behind")
	}

	// Results are cached: a second probe mounts nothing.
	s.probe(context.Background(), "", "")
	if len(r.callList()) != 2 {
		t.Errorf("second probe ran %v", r.callList())
	}
}

// The disk scan (storage.ScanDisks) mounts filesystems to look for
// libraries too: during an install it must not run at all, or a mount on
// the target could make the install's wipefs fail. The last scan stands in.
func TestProbeNoMountWhileInstalling(t *testing.T) {
	_, r, s := probeMachine(t)
	scans := 0
	scan := s.env.scanDisks
	s.env.scanDisks = func(ctx context.Context) ([]storage.Disk, error) { scans++; return scan(ctx) }
	s.probe(context.Background(), "", "")
	calls := len(r.callList())
	s.status.State = StateRunning
	res := s.probe(context.Background(), "", "")
	if scans != 1 || len(r.callList()) != calls {
		t.Errorf("probe scanned (%d scans) or mounted during an install: %v", scans, r.callList()[calls:])
	}
	for _, d := range res.Disks {
		if d.Path == "/dev/sda" && len(d.SteamLibraries) != 1 {
			t.Error("already-mounted libraries should still be found")
		}
	}

	// Without an earlier scan, sysfs lists the disks.
	_, r, s = probeMachine(t)
	s.env.scanDisks = func(context.Context) ([]storage.Disk, error) { t.Error("scanned during an install"); return nil, nil }
	s.status.State = StateRunning
	if res := s.probe(context.Background(), "", ""); len(res.Disks) != 4 || len(r.callList()) != 0 {
		t.Errorf("disks %+v, calls %v", res.Disks, r.callList())
	}
}

func TestProbeFailedMountNotCached(t *testing.T) {
	_, r, s := probeMachine(t)
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "mount" {
			return "", errors.New("mount: wrong fs type"), true
		}
		return "", nil, false
	}
	s.probe(context.Background(), "", "")
	if _, cached := s.libCache["01D9ABCDEF"]; cached {
		t.Error("a failed mount was cached")
	}
}

func TestProbeWithoutLsblk(t *testing.T) {
	f, _, s := probeMachine(t)
	s.env.scanDisks = func(context.Context) ([]storage.Disk, error) { return nil, errors.New("lsblk: not found") }
	res := s.probe(context.Background(), "", "")
	var names []string
	for _, d := range res.Disks {
		names = append(names, d.Path)
		if d.Path == "/dev/sda" && (d.Model != "SATA1TB" || d.Size != 1000*gib || d.Transport != "sata") {
			t.Errorf("sysfs fallback = %+v", d)
		}
		if d.Path == "/dev/nvme0n1" && !d.HasVaporOS {
			t.Error("has_vaporos from sysfs partition names")
		}
	}
	if got := strings.Join(names, ","); got != "/dev/nvme0n1,/dev/sda,/dev/sdb,/dev/sdc" {
		t.Errorf("disks = %s", got)
	}
	_ = f
}

// floorMin is the smallest min_size there is: 512 MiB + 2 x 8 GiB + 8 GiB.
const floorMin = (512 + 2*8192 + 8192) * mib

func TestProbeMinSize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(f *fakeSys, img *image)
		min      int64
		errMatch string
	}{
		{"small image: the floor", nil, floorMin, ""},
		{"4 GiB image: 12 GiB slots", func(f *fakeSys, img *image) {
			a := img.man.Artifacts["root"]
			a.Size = 4 * gib
			img.man.Artifacts["root"] = a
			img.sign(t)
		}, (512 + 2*12288 + 8192) * mib, ""},
		{"image larger than a slot", func(f *fakeSys, img *image) {
			a := img.man.Artifacts["root"]
			a.Size = 17 * gib
			img.man.Artifacts["root"] = a
			img.sign(t)
		}, floorMin, "larger than the largest slot"},
		{"no image", func(f *fakeSys, img *image) {
			os.Remove(filepath.Join(img.dir, "manifest.json"))
		}, floorMin, "no VaporOS image found on the installation medium"},
		{"unsigned release medium", func(f *fakeSys, img *image) {
			os.Remove(filepath.Join(img.dir, "manifest.json.sig"))
		}, floorMin, "only debug builds may install without a signature"},
		{"unsigned debug medium", func(f *fakeSys, img *image) {
			os.Remove(filepath.Join(img.dir, "manifest.json.sig"))
			f.setImageInfo(img.man.Version, "main", true)
		}, floorMin, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, s := probeMachine(t)
			img := writeImage(t, config.LiveMedium, 1<<20)
			if tc.setup != nil {
				tc.setup(f, img)
			}
			res := s.probe(context.Background(), "", "")
			if res.MinSize != tc.min {
				t.Errorf("min_size = %d (%s), want %d (%s)", res.MinSize, humanBytes(res.MinSize), tc.min, humanBytes(tc.min))
			}
			if tc.errMatch == "" {
				if res.SourceError != "" || res.Version != img.man.Version {
					t.Errorf("version %q, source_error %q", res.Version, res.SourceError)
				}
			} else if !strings.Contains(res.SourceError, tc.errMatch) {
				t.Errorf("source_error = %q, want %q", res.SourceError, tc.errMatch)
			}
			if res.Source != "" || len(res.Disks) != 4 {
				t.Errorf("source %q, %d disks", res.Source, len(res.Disks))
			}
		})
	}
}

func getProbe(t *testing.T, s *Service, query string) (int, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/install/probe"+query, nil)
	w := httptest.NewRecorder()
	s.handleProbe(w, req)
	var out map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func jsonString(raw json.RawMessage) string {
	var s string
	json.Unmarshal(raw, &s)
	return s
}

func TestProbeHandlerSource(t *testing.T) {
	_, _, s := probeMachine(t)
	live := writeImage(t, config.LiveMedium, 1<<20)
	dir := t.TempDir()
	other := writeImage(t, dir, 1<<20)
	other.man.Version = "20261001.000000"
	other.sign(t)
	reg := newFakeRegistry(t, dir, []string{"beta"}, nil)

	for _, tc := range []struct {
		query, source, channel, version string
	}{
		{"", "", "", live.man.Version},
		{"?source=live", "", "", live.man.Version},
		{"?source=" + url.QueryEscape(dir), dir, "", "20261001.000000"},
		{"?source=" + url.QueryEscape(reg.spec()) + "&channel=beta", reg.spec(), "beta", "20261001.000000"},
	} {
		code, out := getProbe(t, s, tc.query)
		if code != http.StatusOK {
			t.Fatalf("%q: %d %s", tc.query, code, out["error"])
		}
		if jsonString(out["source"]) != tc.source || jsonString(out["channel"]) != tc.channel || jsonString(out["version"]) != tc.version {
			t.Errorf("%q: source %s channel %s version %s", tc.query, out["source"], out["channel"], out["version"])
		}
		if _, ok := out["min_size"]; !ok {
			t.Errorf("%q: no min_size", tc.query)
		}
		if e, ok := out["source_error"]; ok {
			t.Errorf("%q: source_error %s", tc.query, e)
		}
	}

	for query, want := range map[string]string{
		"?source=ftp%3A%2F%2Fx%2F":                          "unsupported source",
		"?source=relative%2Fdir":                            "unsupported source",
		"?source=oci%3A%2F%2Fghcr.io%2Fx%2Fy&channel=a%2Fb": "invalid channel",
	} {
		code, out := getProbe(t, s, query)
		if code != http.StatusBadRequest || !strings.Contains(jsonString(out["error"]), want) {
			t.Errorf("%s -> %d %s, want 400 %q", query, code, out["error"], want)
		}
	}
}

// A source that does not answer costs the probe probeImageTimeout, not the
// updater's minutes of retries, and the disks are still listed.
func TestProbeSlowSource(t *testing.T) {
	_, _, s := probeMachine(t)
	probeImageTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	res := s.probe(context.Background(), srv.URL+"/vos/", "")
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("probe took %v", took)
	}
	if res.SourceError == "" || res.MinSize != floorMin || len(res.Disks) != 4 || res.Source != srv.URL+"/vos/" {
		t.Errorf("probe = %+v", res)
	}
}
