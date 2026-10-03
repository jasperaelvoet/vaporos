package boot

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// newESP returns an empty ESP with loader/entries, and a directory holding
// a kernel pair to install from.
func newESP(t *testing.T) (esp, src string) {
	t.Helper()
	esp = filepath.Join(t.TempDir(), "esp")
	if err := os.MkdirAll(filepath.Join(esp, "loader", "entries"), 0o755); err != nil {
		t.Fatal(err)
	}
	src = t.TempDir()
	os.WriteFile(filepath.Join(src, "vmlinuz"), []byte("kernel"), 0o644)
	os.WriteFile(filepath.Join(src, "initramfs.img"), []byte("initrd"), 0o644)
	return esp, src
}

func entryNames(t *testing.T, esp string) []string {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(esp, "loader", "entries"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	sort.Strings(names)
	return names
}

func mustInstall(t *testing.T, esp, version, slot, src, options string, tries int) {
	t.Helper()
	if err := InstallEntry(esp, version, slot, src, options, tries); err != nil {
		t.Fatal(err)
	}
}

func TestCmdline(t *testing.T) {
	cases := []struct{ slot, image, machine, want string }{
		{"a", "quiet loglevel=3", "video=DP-1:e", "vos.slot=a quiet loglevel=3 video=DP-1:e"},
		{"b", "  quiet   panic=10 ", "", "vos.slot=b quiet panic=10"},
		// Stray slots from either part never survive, duplicates collapse.
		{"b", "vos.slot=a quiet console=ttyS0,115200", "console=ttyS0,115200 vos.slot=a video=DP-1:e",
			"vos.slot=b quiet console=ttyS0,115200 video=DP-1:e"},
		// Quoted values stay one argument.
		{"a", `quiet foo="a b" bar`, "", `vos.slot=a quiet foo="a b" bar`},
		{"", "quiet", "", "quiet"},
	}
	for _, c := range cases {
		if got := Cmdline(c.slot, c.image, c.machine); got != c.want {
			t.Errorf("Cmdline(%q, %q, %q) = %q, want %q", c.slot, c.image, c.machine, got, c.want)
		}
	}
	// Composing again is a no-op.
	once := Cmdline("a", "quiet x", "video=DP-1:e")
	if twice := Cmdline("a", once, ""); twice != once {
		t.Errorf("not idempotent: %q vs %q", once, twice)
	}
}

func TestSwapMachineArgs(t *testing.T) {
	old := "video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin firmware_class.path=/var/lib/vos/firmware"
	opts := Cmdline("b", "quiet panic=10", old)
	neu := "video=DP-2:e drm.edid_firmware=DP-2:edid/vaporos.bin firmware_class.path=/var/lib/vos/firmware"
	want := "vos.slot=b quiet panic=10 " + neu
	if got := SwapMachineArgs(opts, old, neu); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := SwapMachineArgs(opts, old, ""); got != "vos.slot=b quiet panic=10" {
		t.Fatalf("clearing: got %q", got)
	}
}

func TestSplitName(t *testing.T) {
	cases := []struct {
		name       string
		base       string
		counting   bool
		left, done int
	}{
		{"vos-20260929.123456.conf", "vos-20260929.123456", false, 0, 0},
		{"vos-20260929.123456+3.conf", "vos-20260929.123456", true, 3, 0},
		{"vos-20260929.123456+2-1.conf", "vos-20260929.123456", true, 2, 1},
		{"vos-20260929.123456+0-3.conf", "vos-20260929.123456", true, 0, 3},
		{"vos-dev-1+1-2.conf", "vos-dev-1", true, 1, 2},
		{"vos-weird+x.conf", "vos-weird+x", false, 0, 0},
		{"vos-weird+-1.conf", "vos-weird+-1", false, 0, 0},
	}
	for _, c := range cases {
		base, counting, left, done := splitName(c.name)
		if base != c.base || counting != c.counting || left != c.left || done != c.done {
			t.Errorf("splitName(%q) = %q %v %d %d", c.name, base, counting, left, done)
		}
	}
}

func TestInstallEntry(t *testing.T) {
	esp, src := newESP(t)
	mustInstall(t, esp, "20260929.123456", "b", src, "quiet panic=10", 3)

	b, err := os.ReadFile(filepath.Join(esp, "loader", "entries", "vos-20260929.123456+3.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := "title VaporOS\nversion 20260929.123456\nsort-key vapor\n" +
		"linux /vos/20260929.123456/vmlinuz\ninitrd /vos/20260929.123456/initramfs.img\n" +
		"options vos.slot=b quiet panic=10\n"
	if string(b) != want {
		t.Fatalf("entry:\n%s\nwant:\n%s", b, want)
	}
	for f, content := range map[string]string{"vmlinuz": "kernel", "initramfs.img": "initrd"} {
		got, err := os.ReadFile(filepath.Join(esp, "vos", "20260929.123456", f))
		if err != nil || string(got) != content {
			t.Fatalf("%s: %q %v", f, got, err)
		}
	}
	// No temp files left behind.
	if tmps, _ := filepath.Glob(filepath.Join(esp, "*", "*", ".*")); len(tmps) > 0 {
		t.Fatalf("temp files: %v", tmps)
	}

	e, err := EntryForSlot(esp, "b")
	if err != nil || e == nil {
		t.Fatalf("EntryForSlot: %v %v", e, err)
	}
	if e.Version != "20260929.123456" || !e.Counting || e.Left != 3 || e.Done != 0 || !e.Bootable() ||
		e.Linux != "/vos/20260929.123456/vmlinuz" || e.Base() != "vos-20260929.123456" {
		t.Fatalf("entry %+v", e)
	}
	if e, _ := EntryForSlot(esp, "a"); e != nil {
		t.Fatalf("slot a: %+v", e)
	}

	// Reinstalling the same slot and version without counting replaces the
	// counting entry instead of adding a second one.
	mustInstall(t, esp, "20260929.123456", "b", src, "quiet", 0)
	if got := entryNames(t, esp); len(got) != 1 || got[0] != "vos-20260929.123456.conf" {
		t.Fatalf("entries %v", got)
	}
}

func TestInstallEntryRejects(t *testing.T) {
	esp, src := newESP(t)
	for _, c := range []struct{ version, slot, options string }{
		{"../x", "a", ""},
		{"1+2", "a", ""},
		{"1", "c", ""},
		{"1", "a", "quiet\nlinux /evil"},
	} {
		if err := InstallEntry(esp, c.version, c.slot, src, c.options, 0); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	// One slot's entry never replaces the other's.
	mustInstall(t, esp, "1", "a", src, "", 0)
	if err := InstallEntry(esp, "1", "b", src, "", 0); err == nil {
		t.Fatal("slot b replaced slot a's entry")
	}
	if e, _ := EntryForSlot(esp, "a"); e == nil {
		t.Fatal("slot a lost its entry")
	}
}

func TestRemoveSlotEntries(t *testing.T) {
	esp, src := newESP(t)
	mustInstall(t, esp, "1", "a", src, "quiet", 0)
	mustInstall(t, esp, "2", "b", src, "quiet", 3)
	// A leftover kernel dir from an interrupted update, and a temp file.
	os.MkdirAll(filepath.Join(esp, "vos", "0"), 0o755)
	os.WriteFile(filepath.Join(esp, "loader", "entries", ".vos-3.conf.tmp-1"), nil, 0o644)

	if err := RemoveSlotEntries(esp, "b"); err != nil {
		t.Fatal(err)
	}
	if got := entryNames(t, esp); len(got) != 1 || got[0] != "vos-1.conf" {
		t.Fatalf("entries %v", got)
	}
	for v, want := range map[string]bool{"1": true, "2": false, "0": false} {
		_, err := os.Stat(filepath.Join(esp, "vos", v))
		if (err == nil) != want {
			t.Errorf("kernel dir %s exists=%v, want %v", v, err == nil, want)
		}
	}

	// A kernel shared by both slots (same version) survives until the last
	// entry that uses it is gone.
	esp, src = newESP(t)
	mustInstall(t, esp, "5", "a", src, "", 0)
	mustInstall(t, esp, "5", "b", src, "", 3)
	if err := RemoveSlotEntries(esp, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(esp, "vos", "5", "vmlinuz")); err != nil {
		t.Fatalf("shared kernel removed: %v", err)
	}
	if err := RemoveSlotEntries(esp, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(esp, "vos", "5")); !os.IsNotExist(err) {
		t.Fatalf("unreferenced kernel kept: %v", err)
	}
}

func TestMarkBadAndSetTries(t *testing.T) {
	esp, src := newESP(t)
	mustInstall(t, esp, "1", "a", src, "", 0)
	mustInstall(t, esp, "2", "b", src, "", 3)
	// systemd-boot tried slot b once.
	entries := filepath.Join(esp, "loader", "entries")
	os.Rename(filepath.Join(entries, "vos-2+3.conf"), filepath.Join(entries, "vos-2+2-1.conf"))

	if err := MarkBad(esp, "b"); err != nil {
		t.Fatal(err)
	}
	if err := MarkBad(esp, "a"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entryNames(t, esp), " "); got != "vos-1+0-1.conf vos-2+0-1.conf" {
		t.Fatalf("after MarkBad: %s", got)
	}
	// Already bad: nothing changes.
	if err := MarkBad(esp, "a"); err != nil {
		t.Fatal(err)
	}
	if err := SetTries(esp, "a", 3); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entryNames(t, esp), " "); got != "vos-1+3.conf vos-2+0-1.conf" {
		t.Fatalf("after SetTries: %s", got)
	}
	if e, _ := EntryForSlot(esp, "a"); e == nil || !e.Bootable() || e.Left != 3 {
		t.Fatalf("slot a: %+v", e)
	}
	if err := MarkBad(t.TempDir(), "a"); err == nil {
		t.Fatal("MarkBad without an entry succeeded")
	}
}

// With the same version in both slots, one slot's new name can be the
// other slot's entry: the rename must fail rather than replace it.
func TestRenamesNeverReplace(t *testing.T) {
	esp, _ := newESP(t)
	entries := filepath.Join(esp, "loader", "entries")
	write := func(name, slot string) {
		os.WriteFile(filepath.Join(entries, name), []byte(entryText("5", "vos.slot="+slot)), 0o644)
	}
	write("vos-5+0-1.conf", "a")
	write("vos-5.conf", "b")
	if err := MarkBad(esp, "b"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("MarkBad over slot a's entry: %v", err)
	}
	if e, _ := EntryForSlot(esp, "a"); e == nil || e.Name() != "vos-5+0-1.conf" {
		t.Fatalf("slot a: %+v", e)
	}
	if err := Bless(esp, "a"); err == nil {
		t.Fatal("Bless over slot b's entry succeeded")
	}
	// Freeing the name first (as Rollback does) makes both work.
	if err := SetTries(esp, "a", 3); err != nil {
		t.Fatal(err)
	}
	if err := MarkBad(esp, "b"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entryNames(t, esp), " "); got != "vos-5+0-1.conf vos-5+3.conf" {
		t.Fatalf("entries: %s", got)
	}
	if e, _ := EntryForSlot(esp, "a"); e == nil || e.Name() != "vos-5+3.conf" {
		t.Fatalf("slot a: %+v", e)
	}
}

func TestBless(t *testing.T) {
	esp, src := newESP(t)
	mustInstall(t, esp, "1", "a", src, "", 0)
	mustInstall(t, esp, "2", "b", src, "", 3)
	if err := MarkBad(esp, "a"); err != nil {
		t.Fatal(err)
	}
	if err := Bless(esp, "a"); err != nil {
		t.Fatal(err)
	}
	// Bootable entries (with tries, or blessed) are left alone.
	if err := Bless(esp, "b"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entryNames(t, esp), " "); got != "vos-1.conf vos-2+3.conf" {
		t.Fatalf("entries: %s", got)
	}
}

func TestClearLoaderOverrides(t *testing.T) {
	old := EFIVarsDir
	EFIVarsDir = t.TempDir()
	defer func() { EFIVarsDir = old }()
	names := []string{"LoaderConfigTimeout", "LoaderEntryDefault", "LoaderEntryPreferred", "LoaderEntryOneShot", "LoaderBootCountPath"}
	for _, n := range names {
		os.WriteFile(filepath.Join(EFIVarsDir, n+"-"+loaderVendor), []byte("x"), 0o644)
	}
	if err := ClearLoaderOverrides(); err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		_, err := os.Stat(filepath.Join(EFIVarsDir, n+"-"+loaderVendor))
		if kept := err == nil; kept != (i >= 3) {
			t.Errorf("%s kept=%v", n, kept)
		}
	}
	// Nothing to clear is fine.
	if err := ClearLoaderOverrides(); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteOptions(t *testing.T) {
	esp, src := newESP(t)
	mustInstall(t, esp, "1", "a", src, "quiet video=DP-1:e", 0)
	mustInstall(t, esp, "2", "b", src, "quiet video=DP-1:e", 3)
	if err := ApplyMachineCmdline(esp, "video=DP-1:e", "video=DP-2:e"); err != nil {
		t.Fatal(err)
	}
	for slot, name := range map[string]string{"a": "vos-1.conf", "b": "vos-2+3.conf"} {
		e, _ := EntryForSlot(esp, slot)
		if e == nil || e.Name() != name || e.Options != "vos.slot="+slot+" quiet video=DP-2:e" {
			t.Fatalf("slot %s: %+v", slot, e)
		}
		b, _ := os.ReadFile(e.Path)
		if strings.Count(string(b), "options ") != 1 || !strings.HasPrefix(string(b), "title VaporOS\n") {
			t.Fatalf("slot %s entry:\n%s", slot, b)
		}
	}
}

// systemd-bless-boot renames the entry between our look and our rename: the
// options must end up in the blessed entry, with no duplicate left behind.
func TestRewriteOptionsBlessRace(t *testing.T) {
	esp, src := newESP(t)
	mustInstall(t, esp, "2", "b", src, "quiet", 3)
	entries := filepath.Join(esp, "loader", "entries")
	os.Rename(filepath.Join(entries, "vos-2+3.conf"), filepath.Join(entries, "vos-2+2-1.conf"))

	blessed := false
	beforeRename = func() {
		if !blessed {
			blessed = true
			os.Rename(filepath.Join(entries, "vos-2+2-1.conf"), filepath.Join(entries, "vos-2.conf"))
		}
	}
	defer func() { beforeRename = func() {} }()

	if err := RewriteOptions(esp, func(e Entry) string { return Cmdline(e.Slot, e.Options, "video=DP-1:e") }); err != nil {
		t.Fatal(err)
	}
	if got := entryNames(t, esp); len(got) != 1 || got[0] != "vos-2.conf" {
		t.Fatalf("entries %v", got)
	}
	e, _ := EntryForSlot(esp, "b")
	if e == nil || e.Options != "vos.slot=b quiet video=DP-1:e" || e.Counting {
		t.Fatalf("entry %+v", e)
	}
}

func TestEntryForSlotPrefersBootable(t *testing.T) {
	esp, _ := newESP(t)
	entries := filepath.Join(esp, "loader", "entries")
	write := func(name, version string) {
		os.WriteFile(filepath.Join(entries, name), []byte(entryText(version, "vos.slot=a")), 0o644)
	}
	write("vos-9+0-3.conf", "9")
	write("vos-3.conf", "3")
	write("vos-2.conf", "2")
	write("other.conf", "7") // not ours
	e, err := EntryForSlot(esp, "a")
	if err != nil || e == nil || e.Version != "3" {
		t.Fatalf("got %+v %v", e, err)
	}
	all, _ := Entries(esp)
	if len(all) != 3 {
		t.Fatalf("Entries listed %d", len(all))
	}
}

// NextEntry is the entry a restart starts: bootable first, then the
// newest version, and on a tie the running slot's.
func TestNextEntry(t *testing.T) {
	e := func(name, slot string) Entry {
		base, counting, left, done := splitName(name)
		return Entry{Path: name, Version: base[len(entryPrefix):], Slot: slot, Counting: counting, Left: left, Done: done}
	}
	for _, c := range []struct {
		es   []Entry
		want string
	}{
		{nil, ""},
		{[]Entry{e("vos-9+2-1.conf", "a"), e("vos-8.conf", "b")}, "vos-9+2-1.conf"}, // first boot after an update
		{[]Entry{e("vos-9+0-3.conf", "a"), e("vos-8.conf", "b")}, "vos-8.conf"},     // its last try, not blessed
		{[]Entry{e("vos-9+0-1.conf", "a"), e("vos-8.conf", "b")}, "vos-8.conf"},     // a rollback
		{[]Entry{e("vos-9.conf", "a"), e("vos-10+3.conf", "b")}, "vos-10+3.conf"},   // a staged update
		{[]Entry{e("vos-9.conf", "b"), e("vos-9.conf", "a")}, "vos-9.conf"},         // the same version twice
	} {
		got := NextEntry(c.es, "a")
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%v: got %s", c.es, got.Path)
		case c.want != "" && (got == nil || got.Path != c.want || (c.want == "vos-9.conf" && got.Slot != "a")):
			t.Errorf("%v: got %+v, want %s", c.es, got, c.want)
		}
	}
}

func TestLoaderConfAndEnsureESP(t *testing.T) {
	esp := filepath.Join(t.TempDir(), "esp")
	os.MkdirAll(esp, 0o755)
	if err := EnsureESP(esp); err == nil {
		t.Fatal("EnsureESP accepted an ESP without loader/")
	}
	if err := WriteLoaderConf(esp); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(esp, "loader", "loader.conf"))
	if string(b) != "timeout 0\neditor no\nauto-entries no\nauto-firmware no\nconsole-mode keep\n" {
		t.Fatalf("loader.conf:\n%s", b)
	}
	if err := EnsureESP(esp); err != nil {
		t.Fatal(err)
	}
}

func TestMachineCmdline(t *testing.T) {
	old := config.StateDir
	config.StateDir = t.TempDir()
	defer func() { config.StateDir = old }()

	if got := MachineCmdline(); got != "" {
		t.Fatalf("missing file: %q", got)
	}
	if err := SetMachineCmdline("", " video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin "); err != nil {
		t.Fatal(err)
	}
	if got := MachineCmdline(); got != "video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin" {
		t.Fatalf("got %q", got)
	}
	if err := SetMachineCmdline("", "a\nb"); err == nil {
		t.Fatal("accepted a line break")
	}
	// Under a target root (the installer).
	root := t.TempDir()
	if err := SetMachineCmdline(root, "video=HDMI-A-1:e"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, config.MachineCmdlinePath()))
	if err != nil || string(b) != "video=HDMI-A-1:e\n" {
		t.Fatalf("target root: %q %v", b, err)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"20260929.123456", "20260929.123456", 0},
		{"20260929.123457", "20260929.123456", 1},
		{"20260928.235959", "20260929.000000", -1},
		{"20261001.000000", "20260930.235959", 1},
		{"1.10", "1.9", 1},
		{"1.0", "1.0.1", -1},
		{"1.01", "1.1", 0},
		{"1.a", "1.1", -1},
		{"dev-2", "dev-10", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}
