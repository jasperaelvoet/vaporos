package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// isolate points every config path this package touches into temp dirs
// for the duration of one test.
func isolate(t *testing.T) {
	t.Helper()
	oldState, oldImage, oldCmdline, oldRun := config.StateDir, config.ImageInfoPath, config.ProcCmdline, config.RunDir
	t.Cleanup(func() {
		config.StateDir, config.ImageInfoPath, config.ProcCmdline, config.RunDir = oldState, oldImage, oldCmdline, oldRun
	})
	config.StateDir = t.TempDir()
	config.ImageInfoPath = filepath.Join(t.TempDir(), "image.json")
	config.ProcCmdline = filepath.Join(t.TempDir(), "cmdline")
	config.RunDir = t.TempDir()
}

func TestEscapePath(t *testing.T) {
	cases := map[string]string{
		"/":                               "-",
		"/var/mnt/SATA1TB":                "var-mnt-SATA1TB",
		"//var//mnt/games/":               "var-mnt-games",
		"/var/./mnt/x":                    "var-mnt-x",
		"/var/mnt/My Games":               `var-mnt-My\x20Games`,
		"/var/mnt/a-b":                    `var-mnt-a\x2db`,
		"/var/mnt/back\\sl":               `var-mnt-back\x5csl`,
		"/.dotdir":                        `\x2edotdir`,
		"/var/mnt/.hidden":                "var-mnt-.hidden",
		"/var/mnt/c:_d.e":                 "var-mnt-c:_d.e",
		"/var/mnt/Jeux-é":                 `var-mnt-Jeux\x2d\xc3\xa9`,
		"/dev/disk/by-uuid/87dbdc4a-1e76": `dev-disk-by\x2duuid-87dbdc4a\x2d1e76`,
	}
	for in, want := range cases {
		got, err := EscapePath(in)
		if err != nil || got != want {
			t.Errorf("EscapePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"relative/path", "/var/../etc", ""} {
		if _, err := EscapePath(bad); err == nil {
			t.Errorf("EscapePath(%q) succeeded", bad)
		}
	}
	if _, err := MountUnitName("/var/mnt/" + strings.Repeat("x", 300)); err == nil {
		t.Error("overlong unit name accepted")
	}
	if got, _ := DeviceUnitName("/dev/disk/by-uuid/ABCD-1234"); got != `dev-disk-by\x2duuid-ABCD\x2d1234.device` {
		t.Errorf("DeviceUnitName = %q", got)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func byPath(disks []Disk) map[string]Disk {
	m := map[string]Disk{}
	for _, d := range disks {
		m[d.Path] = d
	}
	return m
}

func TestParseLsblkInstalled(t *testing.T) {
	disks, err := parseLsblk(readFixture(t, "lsblk-installed.json"), "VOS_LIVE", false)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, d := range disks {
		paths = append(paths, d.Path)
	}
	want := []string{
		"/dev/sda", "/dev/sda1", "/dev/sdb", "/dev/sdb1", "/dev/sdc", "/dev/sdc1", "/dev/sdc2", "/dev/sdc3",
		"/dev/nvme0n1", "/dev/nvme0n1p1", "/dev/nvme0n1p2", "/dev/nvme0n1p3", "/dev/nvme0n1p4",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %q", paths) // zram0 must be gone
	}
	m := byPath(disks)
	if d := m["/dev/sdb1"]; d.Parent != "/dev/sdb" || d.Model != "CT1000BX500SSD1" || d.Transport != "sata" ||
		d.MountedAt != "/var/mnt/SATA1TB" || d.Free != 637702316032 || d.Size != 1000202241024 || d.IsSystem || d.Type != "part" {
		t.Errorf("sdb1 = %+v", d)
	}
	if d := m["/dev/sdc2"]; !d.Removable || d.Model != "WDC WD20EZRZ-00Z5HB0" || d.Transport != "usb" || d.Label != "Games & Stuff" || d.FSType != "ntfs" {
		t.Errorf("sdc2 = %+v", d)
	}
	if d := m["/dev/sda1"]; d.MountedAt != "" || d.IsSystem {
		t.Errorf("sda1 = %+v", d)
	}
	for _, p := range []string{"/dev/nvme0n1", "/dev/nvme0n1p1", "/dev/nvme0n1p3", "/dev/nvme0n1p4"} {
		if !m[p].IsSystem {
			t.Errorf("%s must be system: %+v", p, m[p])
		}
	}
	if d := m["/dev/nvme0n1p4"]; d.MountedAt != "/var" || d.PartLabel != "vos_data" {
		t.Errorf("vos_data = %+v", d)
	}
}

func TestParseLsblkLive(t *testing.T) {
	disks, err := parseLsblk(readFixture(t, "lsblk-live.json"), "VOS_LIVE", true)
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(disks)
	if _, ok := m["/dev/loop0"]; ok {
		t.Error("loop device listed")
	}
	for _, p := range []string{"/dev/sda", "/dev/sda1", "/dev/sda2"} {
		if !m[p].IsSystem {
			t.Errorf("live medium %s not system", p)
		}
	}
	for _, p := range []string{"/dev/nvme0n1", "/dev/nvme0n1p2", "/dev/sr0"} {
		if m[p].IsSystem {
			t.Errorf("%s wrongly system", p)
		}
	}
	if d := m["/dev/sr0"]; d.Type != "rom" || !d.Removable {
		t.Errorf("sr0 = %+v", d)
	}
}

func TestExistingInstallIsNotSystemWhenLive(t *testing.T) {
	data := readFixture(t, "lsblk-installed.json")
	// Booted from the ISO, nothing of the old install is mounted.
	data = []byte(strings.NewReplacer(`["/efi"]`, `[null]`, `["/"]`, `[null]`, `["/var", "/state"]`, `[null]`).Replace(string(data)))
	live, err := parseLsblk(data, "VOS_LIVE", true)
	if err != nil {
		t.Fatal(err)
	}
	installed, _ := parseLsblk(data, "VOS_LIVE", false)
	if byPath(live)["/dev/nvme0n1p4"].IsSystem {
		t.Error("an existing install is hidden from the installer")
	}
	if !byPath(installed)["/dev/nvme0n1p4"].IsSystem {
		t.Error("a second VaporOS disk is adoptable on an installed system")
	}
	if byPath(live)["/dev/nvme0n1p4"].PartLabel != "vos_data" {
		t.Error("partlabel lost: the installer needs it to recognise VaporOS")
	}
}

func TestParseLsblkOldFormat(t *testing.T) {
	disks, err := parseLsblk(readFixture(t, "lsblk-old.json"), "VOS_LIVE", false)
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(disks)
	if d := m["/dev/vda1"]; d.Size != 53686042624 || d.MountedAt != "/mnt/games" || d.Free != 12345 || d.Removable {
		t.Errorf("vda1 = %+v", d)
	}
	if d := m["/dev/vda2"]; d.Size != 0 || !d.Removable {
		t.Errorf("vda2 = %+v (human size must read as unknown, rm \"1\" as true)", d)
	}
	if d := m["/dev/mapper/luks-x"]; d.Parent != "/dev/vda2" || d.FSType != "xfs" || d.Type != "crypt" {
		t.Errorf("crypt child = %+v", d)
	}
	if _, err := parseLsblk([]byte("not json"), "", false); err == nil {
		t.Error("garbage accepted")
	}
}

// stubScan feeds lsblk output to ScanDisks and records probes.
func stubScan(t *testing.T, lsblk []byte, libs map[string]string) *[]string {
	t.Helper()
	oldRun, oldProbe := runLsblk, probeLibrary
	t.Cleanup(func() {
		runLsblk, probeLibrary = oldRun, oldProbe
		probeCache = map[string]probeResult{}
	})
	probeCache = map[string]probeResult{}
	runLsblk = func(_ context.Context, cols string) ([]byte, error) {
		if strings.Contains(cols, "MOUNTPOINTS") {
			return nil, errors.New(`lsblk: unknown column: MOUNTPOINTS`)
		}
		return lsblk, nil
	}
	probed := &[]string{}
	probeLibrary = func(_ context.Context, d Disk) (string, int64, error) {
		*probed = append(*probed, d.Path)
		if dir, ok := libs[d.UUID]; ok {
			return dir, 42, nil
		}
		return "", 7, nil
	}
	isolate(t)
	return probed
}

func TestScanDisksDetectsLibraries(t *testing.T) {
	mnt := t.TempDir()
	os.MkdirAll(filepath.Join(mnt, "SteamLibrary", "steamapps"), 0o755)
	data := strings.ReplaceAll(string(readFixture(t, "lsblk-installed.json")), `"/var/mnt/SATA1TB"`, `"`+mnt+`"`)
	probed := stubScan(t, []byte(data), map[string]string{"87dbdc4a-1e76-4f22-9ac6-6f98263ca530": "."})

	disks, err := ScanDisks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(disks)
	if d := m["/dev/sdb1"]; !d.SteamLibrary || d.LibraryDir != "SteamLibrary" {
		t.Errorf("mounted library not found: %+v", d)
	}
	if d := m["/dev/sda1"]; !d.SteamLibrary || d.LibraryDir != "." || d.Free != 42 {
		t.Errorf("probed library not found: %+v", d)
	}
	if d := m["/dev/sdc2"]; d.SteamLibrary || d.Free != 7 {
		t.Errorf("ntfs disk = %+v", d)
	}
	// Only unmounted, non-system, probeable filesystems are mounted to look.
	if want := []string{"/dev/sda1", "/dev/sdc2"}; !reflect.DeepEqual(*probed, want) {
		t.Errorf("probed %q, want %q", *probed, want)
	}
	if _, err := ScanDisks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*probed) != 2 {
		t.Errorf("second scan re-probed: %q", *probed)
	}
}

// newTestService returns a Service over a temp state dir, with systemctl
// calls recorded and scan fed from the installed-system fixture.
type fakeSystemd struct {
	calls  []string
	fail   map[string]error
	active map[string]bool
}

func (f *fakeSystemd) systemctl(_ context.Context, args ...string) error {
	c := strings.Join(args, " ")
	f.calls = append(f.calls, c)
	return f.fail[c]
}

func newTestService(t *testing.T) (*Service, *fakeSystemd) {
	t.Helper()
	isolate(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	disks, err := parseLsblk(readFixture(t, "lsblk-installed.json"), "VOS_LIVE", false)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSystemd{fail: map[string]error{}, active: map[string]bool{}}
	s := NewService(cfg)
	s.scan = func(context.Context) ([]Disk, error) { return append([]Disk(nil), disks...), nil }
	s.systemctl = fs.systemctl
	s.isActive = func(_ context.Context, unit string) bool { return fs.active[unit] }
	s.mntBase = filepath.Join(t.TempDir(), "mnt")
	return s, fs
}

func call(t *testing.T, h http.HandlerFunc, method, target, body string, pathValues ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(pathValues); i += 2 {
		r.SetPathValue(pathValues[i], pathValues[i+1])
	}
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func savedLibraries(t *testing.T) []config.Library {
	t.Helper()
	var c config.Config
	if err := config.ReadJSON(config.ConfigPath(), &c); err != nil {
		t.Fatal(err)
	}
	return c.Storage.Libraries
}

func TestAdoptLibrary(t *testing.T) {
	s, fs := newTestService(t)
	w, out := call(t, s.handleAdopt, "POST", "/api/v1/storage/libraries", `{"uuid":"5C3A4F9E3A4F75A8"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	mp := filepath.Join(s.mntBase, "Games___Stuff")
	if out["mountpoint"] != mp || !strings.Contains(out["hint"].(string), mp) {
		t.Errorf("response = %v", out)
	}
	libs := savedLibraries(t)
	want := []config.Library{{UUID: "5C3A4F9E3A4F75A8", Label: "Games & Stuff", Mountpoint: mp, FSType: "ntfs3"}}
	if !reflect.DeepEqual(libs, want) {
		t.Errorf("config libraries = %+v", libs)
	}
	unit, _ := MountUnitName(mp)
	if wantCalls := []string{"daemon-reload", "start " + unit}; !reflect.DeepEqual(fs.calls, wantCalls) {
		t.Errorf("systemctl calls = %q", fs.calls)
	}
	if fi, err := os.Stat(s.mntBase); err != nil || !fi.IsDir() {
		t.Errorf("mount base not created: %v", err)
	}

	// Adopting again only retries the mount.
	fs.calls = nil
	if w, _ := call(t, s.handleAdopt, "POST", "/", `{"uuid":"5C3A4F9E3A4F75A8"}`); w.Code != http.StatusOK {
		t.Fatalf("re-adopt status %d", w.Code)
	}
	if len(savedLibraries(t)) != 1 || len(fs.calls) != 2 {
		t.Errorf("re-adopt changed config or skipped mount: %q", fs.calls)
	}
}

func TestAdoptRejects(t *testing.T) {
	s, fs := newTestService(t)
	cases := []struct {
		body string
		code int
	}{
		{`{"uuid":"146c482e-c30e-4195-937a-a66b6f6d0784"}`, http.StatusBadRequest}, // vos_data
		{`{"uuid":"ABCD-1234"}`, http.StatusBadRequest},                            // exfat
		{`{"uuid":"00000000-0000-0000-0000-000000000000"}`, http.StatusNotFound},
		{`{"uuid":"../../etc"}`, http.StatusBadRequest},
		{`{"uuid":`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if w, _ := call(t, s.handleAdopt, "POST", "/", c.body); w.Code != c.code {
			t.Errorf("%s: status %d, want %d (%s)", c.body, w.Code, c.code, w.Body)
		}
	}
	if len(fs.calls) != 0 {
		t.Errorf("rejected adoptions touched systemd: %q", fs.calls)
	}
}

func TestAdoptRollsBackWhenMountFails(t *testing.T) {
	s, fs := newTestService(t)
	unit, _ := MountUnitName(filepath.Join(s.mntBase, "SATA500GB"))
	fs.fail["start "+unit] = errors.New("job failed")
	w, _ := call(t, s.handleAdopt, "POST", "/", `{"uuid":"87dbdc4a-1e76-4f22-9ac6-6f98263ca530"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", w.Code)
	}
	if libs := savedLibraries(t); len(libs) != 0 {
		t.Errorf("failed adoption left %+v in config", libs)
	}
	if got := fs.calls[len(fs.calls)-1]; got != "daemon-reload" {
		t.Errorf("no daemon-reload after rollback: %q", fs.calls)
	}
}

func TestMountNameCollisions(t *testing.T) {
	s, _ := newTestService(t)
	s.cfg.Storage.Libraries = []config.Library{{UUID: "x", Mountpoint: filepath.Join(s.mntBase, "SATA500GB")}}
	os.MkdirAll(filepath.Join(s.mntBase, "SATA500GB-2", "lost+found"), 0o755)
	if got := s.mountName(Disk{Label: "SATA500GB", UUID: "u"}); got != "SATA500GB-3" {
		t.Errorf("mountName = %q", got)
	}
	if got := s.mountName(Disk{UUID: "87DBDC4A-1e76-4f22"}); got != "disk-87dbdc4a" {
		t.Errorf("unlabelled mountName = %q", got)
	}
	for in, want := range map[string]string{"..x": "x", "a/b": "a_b", "***": "", "ok-1.2_x": "ok-1.2_x"} {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListStorage(t *testing.T) {
	s, _ := newTestService(t)
	s.cfg.Storage.Libraries = []config.Library{
		{UUID: "1de127b9-77c4-4ca1-ae49-15f137071861", Label: "SATA1TB", Mountpoint: "/var/mnt/SATA1TB", FSType: "ext4"},
		{UUID: "dead-beef", Label: "Gone", Mountpoint: "/var/mnt/Gone", FSType: "xfs"},
	}
	w := httptest.NewRecorder()
	s.handleList(w, httptest.NewRequest("GET", "/api/v1/storage", nil))
	var out struct{ Disks []Disk }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	m := map[string]Disk{}
	for _, d := range out.Disks {
		if d.FSType == "" {
			t.Errorf("entry without filesystem listed: %+v", d)
		}
		m[d.UUID] = d
	}
	if !m["1de127b9-77c4-4ca1-ae49-15f137071861"].Adopted || m["87dbdc4a-1e76-4f22-9ac6-6f98263ca530"].Adopted {
		t.Errorf("adopted flags wrong: %+v", out.Disks)
	}
	if g := m["dead-beef"]; !g.Adopted || !g.Missing || g.Label != "Gone" {
		t.Errorf("missing library = %+v", g)
	}
}

func TestRemoveLibrary(t *testing.T) {
	s, fs := newTestService(t)
	mp := filepath.Join(s.mntBase, "SATA1TB")
	os.MkdirAll(mp, 0o755)
	s.cfg.Storage.Libraries = []config.Library{{UUID: "1de127b9-77c4-4ca1-ae49-15f137071861", Mountpoint: mp, FSType: "ext4"}}
	unit, _ := MountUnitName(mp)

	fs.fail["stop "+unit] = errors.New("target is busy")
	fs.active[unit] = true
	if w, _ := call(t, s.handleRemove, "DELETE", "/", "", "uuid", "1de127b9-77c4-4ca1-ae49-15f137071861"); w.Code != http.StatusConflict {
		t.Fatalf("busy remove status %d", w.Code)
	}
	if len(s.cfg.Storage.Libraries) != 1 {
		t.Fatal("busy library forgotten")
	}

	fs.active[unit] = false
	fs.calls = nil
	if w, _ := call(t, s.handleRemove, "DELETE", "/", "", "uuid", "1de127b9-77c4-4ca1-ae49-15f137071861"); w.Code != http.StatusOK {
		t.Fatalf("remove status %d: %s", w.Code, w.Body)
	}
	if libs := savedLibraries(t); len(libs) != 0 {
		t.Errorf("config still has %+v", libs)
	}
	if want := []string{"stop " + unit, "daemon-reload"}; !reflect.DeepEqual(fs.calls, want) {
		t.Errorf("calls = %q", fs.calls)
	}
	if _, err := os.Stat(mp); !os.IsNotExist(err) {
		t.Errorf("empty mountpoint left behind: %v", err)
	}
	if w, _ := call(t, s.handleRemove, "DELETE", "/", "", "uuid", "1de127b9-77c4-4ca1-ae49-15f137071861"); w.Code != http.StatusNotFound {
		t.Errorf("second remove status %d", w.Code)
	}
	if w, _ := call(t, s.handleRemove, "DELETE", "/", "", "uuid", "a/b"); w.Code != http.StatusBadRequest {
		t.Errorf("bad uuid status %d", w.Code)
	}
}

func TestGenerator(t *testing.T) {
	isolate(t)
	cfg := config.Defaults()
	cfg.Storage.Libraries = []config.Library{
		{UUID: "1de127b9-77c4-4ca1-ae49-15f137071861", Label: "SATA1TB", Mountpoint: "/var/mnt/SATA1TB", FSType: "ext4"},
		{UUID: "5C3A4F9E3A4F75A8", Label: "100% Games\n", Mountpoint: "/var/mnt/Games", FSType: "ntfs"},
		{UUID: "11112222", Label: "evil", Mountpoint: "/usr", FSType: "ext4"},
		{UUID: "33334444", Label: "odd", Mountpoint: "/var/mnt/x", FSType: "vfat"},
	}
	cfg.SSH.Enabled = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if code := CLIGenerator([]string{dir, dir + "/early", dir + "/late"}); code != 0 {
		t.Fatalf("exit %d", code)
	}

	unit, err := os.ReadFile(filepath.Join(dir, "var-mnt-SATA1TB.mount"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Description=Game library SATA1TB\n",
		"What=/dev/disk/by-uuid/1de127b9-77c4-4ca1-ae49-15f137071861\n",
		"Where=/var/mnt/SATA1TB\n",
		"Type=ext4\n",
		"Options=nofail,noatime,x-systemd.device-timeout=10s\n",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("ext4 unit lacks %q:\n%s", want, unit)
		}
	}
	ntfs, _ := os.ReadFile(filepath.Join(dir, "var-mnt-Games.mount"))
	for _, want := range []string{"Description=Game library 100%% Games\n", "Type=ntfs3\n", "Options=nofail,noatime,x-systemd.device-timeout=10s,uid=1000,gid=1000\n"} {
		if !strings.Contains(string(ntfs), want) {
			t.Errorf("ntfs unit lacks %q:\n%s", want, ntfs)
		}
	}
	for _, u := range []string{"var-mnt-SATA1TB.mount", "var-mnt-Games.mount"} {
		if target, err := os.Readlink(filepath.Join(dir, "local-fs.target.wants", u)); err != nil || target != "../"+u {
			t.Errorf("wants link for %s = %q, %v", u, target, err)
		}
	}
	dropin, err := os.ReadFile(filepath.Join(dir, `dev-disk-by\x2duuid-1de127b9\x2d77c4\x2d4ca1\x2dae49\x2d15f137071861.device.d`, "50-vos-device-timeout.conf"))
	if err != nil || !strings.Contains(string(dropin), "JobRunningTimeoutSec=10s") {
		t.Errorf("device timeout drop-in = %q, %v", dropin, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "multi-user.target.wants", "sshd.service")); err != nil || target != "/usr/lib/systemd/system/sshd.service" {
		t.Errorf("sshd link = %q, %v", target, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, n := range names {
		if strings.HasPrefix(n, "usr") || n == "var-mnt-x.mount" {
			t.Errorf("unsafe library generated %s", n)
		}
	}

	// Running again into the same directory (daemon-reload) must work.
	if code := CLIGenerator([]string{dir}); code != 0 {
		t.Fatal("rerun failed")
	}
}

func TestGeneratorNeverFails(t *testing.T) {
	isolate(t)
	os.WriteFile(config.ConfigPath(), []byte("{broken"), 0o644)
	dir := t.TempDir()
	if code := CLIGenerator([]string{dir}); code != 0 {
		t.Errorf("exit %d on corrupt config", code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("corrupt config generated %d files", len(entries))
	}
	if code := CLIGenerator(nil); code != 0 {
		t.Errorf("exit %d without arguments", code)
	}
	// An unwritable output directory is logged, not fatal.
	os.Remove(config.ConfigPath())
	cfg := config.Defaults()
	cfg.SSH.Enabled = true
	cfg.Save()
	if code := CLIGenerator([]string{filepath.Join(dir, "missing", "\x00bad")}); code != 0 {
		t.Errorf("exit %d on bad dir", code)
	}
}
