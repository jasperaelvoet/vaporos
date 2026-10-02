package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage"
)

// installMachine is a typical target: an empty SSD (sda) with an old
// partition, the ISO on a USB stick (sdb, isohybrid, mounted whole) and a
// SATA disk with a Steam library (sdc1).
func installMachine(t *testing.T) (*fakeSys, *image) {
	f := newFakeSys(t)
	f.addDisk("sda", "8:0", "ata1", 64*gib, "Test SSD")
	f.addPart("sda", "sda1", "8:1", 1, "old-root", 60*gib)
	f.addDisk("sdb", "8:16", "usb1", 16*gib, "USB Stick")
	f.addDisk("sdc", "8:32", "ata2", 1000*gib, "SATA1TB")
	f.addPart("sdc", "sdc1", "8:33", 1, "", 1000*gib)
	f.setMounts("8:16 " + f.mediumMount() + " iso9660 /dev/sdb")
	f.scan = []storage.Disk{
		{Path: "/dev/sda", Model: "Test SSD", Size: 64 * gib},
		{Path: "/dev/sdc", Model: "SATA1TB", Size: 1000 * gib},
		{Path: "/dev/sdc1", Parent: "sdc", UUID: "1de127b9-77c4", Label: "SATA 1TB", FSType: "ext4"},
	}
	return f, writeImage(t, config.LiveMedium, 5<<20+12345)
}

func stepsOf(recs []progressRec) []string {
	var out []string
	for _, r := range recs {
		if len(out) == 0 || out[len(out)-1] != r.step {
			out = append(out, r.step)
		}
	}
	return out
}

// assertSubsequence checks want appears in got in order (not necessarily
// adjacent).
func assertSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Fatalf("command %q missing or out of order; commands:\n  %s", want[i], strings.Join(got, "\n  "))
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallErase(t *testing.T) {
	f, img := installMachine(t)
	r := f.runner()
	var recs []progressRec
	opts := Options{
		Disk: "/dev/sda", Hostname: "Gamer", Password: "correct horse", Timezone: "Europe/Brussels",
		Libraries: []string{"1de127b9-77c4"},
	}
	if err := runInstall(context.Background(), f.env(r), opts, recorder(&recs)); err != nil {
		t.Fatal(err)
	}

	// Progress: every step once, in order, never backwards, ending at 100.
	want := []string{StepProbe, StepPartition, StepWrite, StepVerify, StepBootloader, StepConfigure, StepDone}
	if got := stepsOf(recs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].percent < recs[i-1].percent {
			t.Fatalf("progress went backwards: %v -> %v", recs[i-1], recs[i])
		}
	}
	if last := recs[len(recs)-1]; last.percent != 100 || last.step != StepDone {
		t.Fatalf("last progress = %+v", last)
	}

	// The commands, with the hard-won details of the bash installer.
	T := "@/run/vos/target"
	assertSubsequence(t, r.relCalls(), []string{
		"wipefs -qa @/dev/sda1", // the old partition
		"wipefs -qa @/dev/sda",
		"sgdisk --zap-all @/dev/sda",
		"sgdisk -n1:0:+512M -t1:ef00 -c1:vos_esp -n2:0:+8192M -t2:8300 -c2:vos_a -n3:0:+8192M -t3:8300 -c3:vos_b -n4:0:0 -t4:8300 -c4:vos_data @/dev/sda",
		"blockdev --rereadpt @/dev/sda",
		"udevadm settle",
		"wipefs -qa @/dev/sda3",
		"mkfs.vfat -F 32 -n VOS_ESP @/dev/sda1",
		"mkfs.ext4 -q -F -O verity -b 4096 -L vos_data @/dev/sda4",
		"mount -t vfat -o fmask=0077,dmask=0077 @/dev/sda1 " + T + "/esp",
		"bootctl install --esp-path=" + T + "/esp --graceful --no-pager",
		"mount -t erofs -o ro @/dev/sda2 " + T + "/root",
		"mount -t ext4 -o rw,noatime @/dev/sda4 " + T + "/root/state",
		"mount --bind " + T + "/root/state/var " + T + "/root/var",
		"mount -t overlay overlay -o lowerdir=" + T + "/root/etc,upperdir=" + T + "/root/state/etc/upper,workdir=" + T + "/root/state/etc/work,index=off " + T + "/root/etc",
		"sync",
		"umount " + T + "/root/etc",
		"umount " + T + "/root/var",
		"umount " + T + "/root/state",
		"umount " + T + "/root",
		"umount " + T + "/esp",
	})
	if len(r.mounted) != 0 {
		t.Errorf("still mounted: %v", r.mounted)
	}
	for _, c := range r.callList() {
		if strings.Contains(c, "sgdisk -q") || strings.Contains(c, "useradd") || strings.Contains(c, "chpasswd") {
			t.Errorf("unexpected command %q", c)
		}
	}
	if len(f.seeds) != 0 {
		t.Errorf("an image without extensions seeded: %v", f.seeds)
	}

	// Slot a holds exactly the image.
	if !slotHas(t, f.path("dev/sda2"), img.root) {
		t.Error("slot a content differs from root.erofs")
	}

	// Boot entry: slot a, image cmdline + machine cmdline, no counting.
	// Kernel and initrd came from a verified copy that is gone again.
	e := f.entry
	if e.slot != "a" || e.version != img.man.Version || e.tries != 0 || e.srcDir == "" || e.srcDir == config.LiveMedium || exists(e.srcDir) {
		t.Errorf("entry = %+v", e)
	}
	// vos.disk names this disk, for the initramfs and for updates.
	if wantOpts := "vos.slot=a quiet loglevel=3 console=ttyS0,115200 video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin vos.disk=" + testDiskGUID; e.options != wantOpts {
		t.Errorf("options = %q, want %q", e.options, wantOpts)
	}
	if !exists(filepath.Join(e.esp, "loader/loader.conf")) {
		t.Error("no loader.conf")
	}
	if f.loaderCleared != 1 {
		t.Errorf("systemd-boot's NVRAM settings cleared %d times", f.loaderCleared)
	}

	// First-boot state.
	root := filepath.Join(config.RunDir, "target/root")
	if got := readFile(t, filepath.Join(root, "etc/hostname")); got != "gamer\n" {
		t.Errorf("hostname = %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(root, "etc/localtime")); got != "../usr/share/zoneinfo/Europe/Brussels" {
		t.Errorf("localtime -> %q", got)
	}
	if f.adminPass != "correct horse" || !exists(filepath.Join(root, "var/lib/vos/auth.json")) {
		t.Errorf("admin password not written (%q)", f.adminPass)
	}
	if got := readFile(t, filepath.Join(root, "var/lib/vos/cmdline")); !strings.HasPrefix(got, "video=DP-1:e") || !strings.HasSuffix(got, " vos.disk="+testDiskGUID+"\n") {
		t.Errorf("machine cmdline = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(root, "var/roothome")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("roothome: %v %v", fi, err)
	}
	var cfg config.Config
	if err := config.ReadJSON(filepath.Join(root, "var/lib/vos/config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Schema != 1 || cfg.Update.Channel != "testing" || cfg.Update.Source != config.DefaultUpdateSrc || cfg.Display.VirtualConnector != "DP-1" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.Power.IdleShutdown {
		t.Error("idle shutdown on without a NIC that wakes the PC")
	}
	wantLib := config.Library{UUID: "1de127b9-77c4", Label: "SATA 1TB", Mountpoint: "/var/mnt/SATA_1TB", FSType: "ext4"}
	if len(cfg.Storage.Libraries) != 1 || cfg.Storage.Libraries[0] != wantLib {
		t.Errorf("libraries = %+v", cfg.Storage.Libraries)
	}

	// The lock is released.
	unlock, err := lockInstall()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestInstallNoGPUNoPassword(t *testing.T) {
	f, _ := installMachine(t)
	f.gpu.Supported = false
	f.diskGUID = "" // and no readable disk GUID: partitions go by label
	r := f.runner()
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, nil); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(config.RunDir, "target/root")
	if exists(filepath.Join(root, "var/lib/vos/cmdline")) || exists(filepath.Join(root, "var/lib/vos/auth.json")) {
		t.Error("machine cmdline or auth.json written without GPU/password")
	}
	if got := readFile(t, filepath.Join(root, "etc/hostname")); got != "vapor\n" {
		t.Errorf("hostname = %q", got)
	}
	if f.entry.options != "vos.slot=a quiet loglevel=3 console=ttyS0,115200" {
		t.Errorf("options = %q", f.entry.options)
	}
}

// Without a GPU the machine args are only the boot disk.
func TestInstallNoGPUHasDisk(t *testing.T) {
	f, _ := installMachine(t)
	f.gpu.Supported = false
	if err := runInstall(context.Background(), f.env(f.runner()), Options{Disk: "sda"}, nil); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(config.RunDir, "target/root")
	if got := readFile(t, filepath.Join(root, "var/lib/vos/cmdline")); got != "vos.disk="+testDiskGUID+"\n" {
		t.Errorf("machine cmdline = %q", got)
	}
	if f.entry.options != "vos.slot=a quiet loglevel=3 console=ttyS0,115200 vos.disk="+testDiskGUID {
		t.Errorf("options = %q", f.entry.options)
	}
}

// Idle shutdown is on only when a wired NIC can wake the PC again.
func TestInstallIdleShutdownNeedsWakeOnLAN(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nics     map[string]bool   // name -> wifi
		ethtool  map[string]string // name -> "Supports Wake-on:" modes
		wantIdle bool
	}{
		{"magic packet", map[string]bool{"enp5s0": false}, map[string]string{"enp5s0": "pumbg"}, true},
		{"no magic packet", map[string]bool{"enp5s0": false}, map[string]string{"enp5s0": "pumb"}, false},
		{"no wake-on-lan", map[string]bool{"enp5s0": false}, map[string]string{"enp5s0": "d"}, false},
		{"only wifi", map[string]bool{"wlan0": true}, map[string]string{"wlan0": "g"}, false},
		{"second nic", map[string]bool{"enp4s0": false, "enp5s0": false}, map[string]string{"enp4s0": "d", "enp5s0": "g"}, true},
		{"no nic", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := installMachine(t)
			for n, wifi := range tc.nics {
				f.addNIC(n, wifi)
			}
			f.mkdir("sys/class/net/lo") // virtual: no device
			r := f.runner()
			r.hook = func(name string, args []string) (string, error, bool) {
				if name != "ethtool" {
					return "", nil, false
				}
				modes, ok := tc.ethtool[args[0]]
				if !ok {
					return "", errors.New("ethtool: no such device"), true
				}
				return "Settings for " + args[0] + ":\n\tSupports Wake-on: " + modes + "\n\tWake-on: d\n", nil, true
			}
			if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, nil); err != nil {
				t.Fatal(err)
			}
			var cfg config.Config
			if err := config.ReadJSON(filepath.Join(config.RunDir, "target/root/var/lib/vos/config.json"), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Power.IdleShutdown != tc.wantIdle || cfg.Power.IdleMinutes != 15 || cfg.Display.VirtualConnector != "DP-1" {
				t.Errorf("config power %+v, display %+v", cfg.Power, cfg.Display)
			}
			if r.called("ethtool lo") {
				t.Error("asked ethtool about lo")
			}
		})
	}
}

func TestInstallRepair(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("nvme0n1", "259:0", "nvme", 500*gib, "NVMe")
	img := writeImage(t, config.LiveMedium, 3<<20)
	for i, label := range partNames {
		size := []int64{espMiB * mib, 8 * gib, 8 * gib, 400 * gib}[i]
		f.addPart("nvme0n1", "nvme0n1p"+string(rune('1'+i)), "259:"+string(rune('1'+i)), i+1, label, size)
	}
	r := f.runner()
	// vos_data already has state; it appears when /var is bound.
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "mount" && args[0] == "--bind" {
			vos := filepath.Join(args[2], "lib/vos")
			os.MkdirAll(vos, 0o755)
			os.WriteFile(filepath.Join(vos, "cmdline"), []byte("video=HDMI-A-1:e keep\n"), 0o644)
			config.WriteJSONAtomic(filepath.Join(vos, "config.json"), map[string]any{
				"schema": 1, "update": map[string]any{"channel": "beta", "auto": "off"},
				"display": map[string]any{"virtual_connector": "HDMI-A-1"},
			}, 0o644)
			os.WriteFile(filepath.Join(vos, "update-state.json"),
				[]byte(`{"booted":"1","staged":{"version":"2","slot":"b"},"failed":["0"]}`), 0o640)
		}
		return "", nil, false
	}
	var recs []progressRec
	err := runInstall(context.Background(), f.env(r), Options{Disk: "/dev/nvme0n1", Mode: "repair", Timezone: "UTC"}, recorder(&recs))
	if err == nil || !strings.Contains(err.Error(), "unknown timezone") {
		// UTC is in the live zoneinfo but not in the fake image: the
		// target's own tz database is what counts.
		t.Fatalf("err = %v, want unknown timezone from the target", err)
	}
	if len(r.mounted) != 0 || exists(filepath.Join(f.entry.esp, "loader/entries/vos-"+img.man.Version+".conf")) {
		t.Fatalf("failed repair left mounts %v or its entry", r.mounted)
	}

	r = f.runner()
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "mount" && args[0] == "--bind" {
			vos := filepath.Join(args[2], "lib/vos")
			os.MkdirAll(vos, 0o755)
			os.WriteFile(filepath.Join(vos, "cmdline"), []byte("video=HDMI-A-1:e keep\n"), 0o644)
			config.WriteJSONAtomic(filepath.Join(vos, "config.json"), map[string]any{
				"schema": 1, "update": map[string]any{"channel": "beta", "auto": "off"},
				"display": map[string]any{"virtual_connector": "HDMI-A-1"},
			}, 0o644)
			os.WriteFile(filepath.Join(vos, "update-state.json"),
				[]byte(`{"booted":"1","staged":{"version":"2","slot":"b"},"failed":["0"]}`), 0o640)
		}
		return "", nil, false
	}
	recs = nil
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "/dev/nvme0n1", Mode: "repair"}, recorder(&recs)); err != nil {
		t.Fatal(err)
	}
	assertSubsequence(t, r.relCalls(), []string{
		"e2fsck -f -p @/dev/nvme0n1p4",
		"tune2fs -O verity @/dev/nvme0n1p4",
		"wipefs -qa @/dev/nvme0n1p1",
		"wipefs -qa @/dev/nvme0n1p3",
		"mkfs.vfat -F 32 -n VOS_ESP @/dev/nvme0n1p1",
		"bootctl install --esp-path=@/run/vos/target/esp --graceful --no-pager",
	})
	for _, c := range r.callList() {
		if strings.HasPrefix(c, "sgdisk") || strings.HasPrefix(c, "mkfs.ext4") || strings.HasPrefix(c, "wipefs -qa "+f.path("dev/nvme0n1p4")) {
			t.Errorf("repair ran %q", c)
		}
	}
	if !slotHas(t, f.path("dev/nvme0n1p2"), img.root) {
		t.Error("slot a not rewritten")
	}
	// The machine cmdline on vos_data wins over the detected connector; it
	// gains the boot disk.
	if f.entry.options != "vos.slot=a quiet loglevel=3 console=ttyS0,115200 video=HDMI-A-1:e keep vos.disk="+testDiskGUID {
		t.Errorf("options = %q", f.entry.options)
	}
	root := filepath.Join(config.RunDir, "target/root")
	if got := readFile(t, filepath.Join(root, "var/lib/vos/cmdline")); got != "video=HDMI-A-1:e keep vos.disk="+testDiskGUID+"\n" {
		t.Errorf("cmdline: %q", got)
	}
	var cfg config.Config
	config.ReadJSON(filepath.Join(root, "var/lib/vos/config.json"), &cfg)
	if cfg.Update.Channel != "beta" || cfg.Update.Auto != "off" || cfg.Display.VirtualConnector != "HDMI-A-1" {
		t.Errorf("repair lost settings: %+v", cfg)
	}
	var st map[string]json.RawMessage
	config.ReadJSON(filepath.Join(root, "var/lib/vos/update-state.json"), &st)
	var failed []string
	json.Unmarshal(st["failed"], &failed)
	if _, staged := st["staged"]; staged || len(failed) != 1 || failed[0] != "0" {
		t.Errorf("update-state = %v", st)
	}
	if fi, _ := os.Stat(filepath.Join(root, "var/lib/vos/update-state.json")); fi.Mode().Perm() != 0o640 {
		t.Errorf("update-state mode = %v", fi.Mode())
	}
	if f.adminPass != "" || exists(filepath.Join(root, "var/lib/vos/auth.json")) {
		t.Error("repair without a password touched auth.json")
	}
	if got := readFile(t, filepath.Join(root, "etc/hostname")); got != "vapor\n" {
		t.Errorf("repair without a hostname changed it: %q", got)
	}
}

// A repair with no hostname or timezone keeps what the user set, read from
// the /etc overlay's upper layer on vos_data; a config.json there keeps
// its idle shutdown setting whatever the NIC can do.
func TestInstallRepairKeepsIdentity(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("sda", "8:0", "ata1", 64*gib, "SSD")
	f.vosParts("sda", 8, 8192)
	writeImage(t, config.LiveMedium, 1<<20)
	f.addNIC("enp5s0", false)
	r := f.runner()
	r.hook = func(name string, args []string) (string, error, bool) {
		switch {
		case name == "ethtool":
			return "Supports Wake-on: pumbg\n", nil, true
		case name == "mount" && args[0] == "--bind":
			upper := filepath.Join(filepath.Dir(args[1]), "etc", "upper")
			os.WriteFile(filepath.Join(upper, "hostname"), []byte("den\n"), 0o644)
			os.Symlink("../usr/share/zoneinfo/Europe/Brussels", filepath.Join(upper, "localtime"))
			config.WriteJSONAtomic(filepath.Join(args[2], "lib/vos/config.json"),
				map[string]any{"schema": 1, "power": map[string]any{"idle_shutdown": false, "idle_minutes": 30}}, 0o644)
		}
		return "", nil, false
	}
	var recs []progressRec
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda", Mode: ModeRepair}, recorder(&recs)); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(config.RunDir, "target/root")
	if got := readFile(t, filepath.Join(root, "etc/hostname")); got != "den\n" {
		t.Errorf("hostname = %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(root, "etc/localtime")); got != "../usr/share/zoneinfo/Europe/Brussels" {
		t.Errorf("localtime -> %q", got)
	}
	configuring := false
	for _, rec := range recs {
		configuring = configuring || rec.message == "Configuring den"
	}
	if !configuring {
		t.Errorf("progress never named the kept hostname: %+v", recs)
	}
	var cfg config.Config
	config.ReadJSON(filepath.Join(root, "var/lib/vos/config.json"), &cfg)
	if cfg.Power.IdleShutdown || cfg.Power.IdleMinutes != 30 {
		t.Errorf("repair changed power settings: %+v", cfg.Power)
	}
}

func TestRepairE2fsck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		p, y    int // exit codes; 0 = success
		wantY   bool
		wantErr bool
	}{
		{"clean", 0, 0, false, false},
		{"fixed", 1, 0, false, false},
		{"preen gives up, -y fixes", 4, 1, true, false},
		{"unfixable", 4, 4, true, true},
		{"operational error", 8, 8, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSys(t)
			f.addDisk("sda", "8:0", "ata1", 64*gib, "SSD")
			f.vosParts("sda", 8, 8192)
			r := f.runner()
			r.hook = func(name string, args []string) (string, error, bool) {
				if name != "e2fsck" {
					return "", nil, false
				}
				code := tc.p
				if args[1] == "-y" {
					code = tc.y
				}
				if code == 0 {
					return "", nil, true
				}
				return "", exitErr{code}, true
			}
			in := &installer{env: f.env(r), opts: Options{Mode: ModeRepair}, disk: "sda"}
			in.parts, _ = vosLayout("sda")
			err := in.prepareRepair(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if r.called("e2fsck -f -y") != tc.wantY {
				t.Errorf("e2fsck -y called = %v", !tc.wantY)
			}
			if tc.wantErr && r.called("mkfs.vfat") {
				t.Error("ESP formatted after a failed fsck")
			}
		})
	}
}

// destructive reports whether any command could have changed a disk.
func destructive(r *fakeRunner) []string {
	var out []string
	for _, c := range r.callList() {
		for _, p := range []string{"wipefs", "sgdisk", "mkfs", "bootctl", "e2fsck", "mount"} {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
			}
		}
	}
	return out
}

func TestInstallRefusesBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeSys, img *image)
		opts  Options
		want  string
	}{
		{"bad signature", func(f *fakeSys, img *image) {
			os.WriteFile(filepath.Join(img.dir, "manifest.json.sig"), []byte("forged"), 0o644)
		}, Options{Disk: "sda"}, "not signed by a trusted key"},
		{"no image", func(f *fakeSys, img *image) {
			os.Remove(filepath.Join(img.dir, "manifest.json"))
		}, Options{Disk: "sda"}, "no VaporOS image found"},
		{"truncated root", func(f *fakeSys, img *image) {
			os.WriteFile(filepath.Join(img.dir, "root.erofs"), img.root[:100], 0o644)
		}, Options{Disk: "sda"}, "the manifest says"},
		{"newer updater", func(f *fakeSys, img *image) {
			img.man.MinUpdater = 99
			img.sign(t)
		}, Options{Disk: "sda"}, "needs updater version 99"},
		{"unsafe version", func(f *fakeSys, img *image) {
			img.man.Version = "../../EFI"
			img.sign(t)
		}, Options{Disk: "sda"}, "invalid version"},
		{"artifact path", func(f *fakeSys, img *image) {
			a := img.man.Artifacts["kernel"]
			a.Name = "../vmlinuz"
			img.man.Artifacts["kernel"] = a
			img.sign(t)
		}, Options{Disk: "sda"}, "invalid name"},
		{"untrusted key", func(f *fakeSys, img *image) {
			os.Remove(filepath.Join(config.KeysDir, "test.pub"))
		}, Options{Disk: "sda"}, "not signed by a trusted key"},
		{"missing directory", nil, Options{Disk: "sda", Source: "/nonexistent/vos"}, "no VaporOS image found at /nonexistent/vos"},
		{"live disk", nil, Options{Disk: "sdb"}, "holds the VaporOS installer"},
		{"live disk by label", func(f *fakeSys, img *image) {
			f.setMounts()
			f.symlink("../../sdb", "dev/disk/by-label/VOS_LIVE")
		}, Options{Disk: "sdb"}, "holds the VaporOS installer"},
		{"mounted", func(f *fakeSys, img *image) {
			f.setMounts("8:16 "+f.mediumMount()+" iso9660 /dev/sdb", "8:1 /mnt/old ext4 /dev/sda1")
		}, Options{Disk: "sda"}, "in use (mounted at /mnt/old)"},
		{"holder", func(f *fakeSys, img *image) {
			f.write(filepath.Join(f.disks["sda"], "sda1/holders/dm-0"), "")
		}, Options{Disk: "sda"}, "in use by dm-0"},
		{"read-only", func(f *fakeSys, img *image) {
			f.write(filepath.Join(f.disks["sda"], "ro"), "1")
		}, Options{Disk: "sda"}, "read-only"},
		{"too small", func(f *fakeSys, img *image) {
			f.write(filepath.Join(f.disks["sda"], "size"), "41943040") // 20 GiB
		}, Options{Disk: "sda"}, "too small (20.0 GiB): VaporOS needs at least 24.5 GiB"},
		{"partition", nil, Options{Disk: "/dev/sda1"}, "is a partition"},
		{"repair without VaporOS", nil, Options{Disk: "sda", Mode: "repair"}, "no VaporOS installation to repair"},
		{"unknown timezone", nil, Options{Disk: "sda", Timezone: "Mars/Olympus"}, "unknown timezone"},
		{"library on target", func(f *fakeSys, img *image) {
			f.scan = append(f.scan, storage.Disk{Path: "/dev/sda1", Parent: "sda", UUID: "aaaa-bbbb", FSType: "ext4"})
		}, Options{Disk: "sda", Libraries: []string{"aaaa-bbbb"}}, "on the disk VaporOS is being installed to"},
		{"unknown library", nil, Options{Disk: "sda", Libraries: []string{"ffff"}}, "no filesystem with UUID ffff"},
		{"no UEFI", func(f *fakeSys, img *image) {
			os.RemoveAll(paths.EFI)
		}, Options{Disk: "sda"}, "UEFI"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, img := installMachine(t)
			if tc.setup != nil {
				tc.setup(f, img)
			}
			r := f.runner()
			err := runInstall(context.Background(), f.env(r), tc.opts, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if d := destructive(r); len(d) > 0 {
				t.Fatalf("ran %v before refusing", d)
			}
		})
	}
}

func TestInstallFailureCleansUp(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fail      func(f *fakeSys, r *fakeRunner, e *env)
		step      string
		entryGone bool
	}{
		{"bootctl", func(f *fakeSys, r *fakeRunner, e *env) {
			r.hook = func(name string, args []string) (string, error, bool) {
				if name == "bootctl" {
					return "", errors.New("bootctl: no ESP"), true
				}
				return "", nil, false
			}
		}, StepBootloader, true},
		{"admin password", func(f *fakeSys, r *fakeRunner, e *env) {
			e.setAdminPassword = func(root, pw string) error { return errors.New("disk full") }
		}, StepConfigure, true},
		{"corrupt medium", func(f *fakeSys, r *fakeRunner, e *env) {
			b := readFile(t, filepath.Join(config.LiveMedium, "root.erofs"))
			os.WriteFile(filepath.Join(config.LiveMedium, "root.erofs"), []byte("X"+b[1:]), 0o644)
		}, StepWrite, true},
		{"bad read-back", func(f *fakeSys, r *fakeRunner, e *env) {
			// The disk changes between the write and the read-back.
			f.onStep = func(step string) {
				if step == StepVerify {
					fh, _ := os.OpenFile(f.path("dev/sda2"), os.O_WRONLY, 0)
					fh.WriteAt([]byte("bitrot"), 4096)
					fh.Close()
				}
			}
		}, StepVerify, true},
		{"stuck mount", func(f *fakeSys, r *fakeRunner, e *env) {
			// A failing unmount falls back to a lazy one.
			r.hook = func(name string, args []string) (string, error, bool) {
				if name == "umount" && args[0] != "-l" && strings.HasSuffix(args[0], "/etc") {
					return "", errors.New("target is busy"), true
				}
				return "", nil, false // "umount -l X" unmounts X
			}
			e.setAdminPassword = func(root, pw string) error { return errors.New("disk full") }
		}, StepConfigure, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := installMachine(t)
			r := f.runner()
			e := f.env(r)
			tc.fail(f, r, e)
			var recs []progressRec
			err := runInstall(context.Background(), e, Options{Disk: "sda", Password: "12345678"}, f.recorder(&recs))
			if err == nil || !strings.HasPrefix(err.Error(), tc.step+": ") {
				t.Fatalf("err = %v, want a %s failure", err, tc.step)
			}
			if len(r.mounted) != 0 {
				t.Errorf("still mounted after failure: %v", r.mounted)
			}
			entries, _ := filepath.Glob(filepath.Join(config.RunDir, "target/esp/loader/entries/vos-*.conf"))
			if tc.entryGone && len(entries) > 0 {
				t.Errorf("boot entry left behind: %v", entries)
			}
			for _, rec := range recs {
				if rec.step == StepDone {
					t.Error("reported done")
				}
			}
			// A new install can start: the lock was released.
			unlock, err := lockInstall()
			if err != nil {
				t.Fatal(err)
			}
			unlock()
		})
	}
}

func TestInstallCancelled(t *testing.T) {
	f, _ := installMachine(t)
	r := f.runner()
	ctx, cancel := context.WithCancel(context.Background())
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "mkfs.ext4" {
			cancel()
		}
		return "", nil, false
	}
	err := runInstall(ctx, f.env(r), Options{Disk: "sda"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(r.mounted) != 0 {
		t.Errorf("mounted: %v", r.mounted)
	}
}

func TestInstallExclusive(t *testing.T) {
	f, _ := installMachine(t)
	unlock, err := lockInstall()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	r := f.runner()
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, nil); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallRefusesOutsideLive(t *testing.T) {
	f, _ := installMachine(t)
	allowAnywhere = false
	old := config.ProcCmdline
	defer func() { config.ProcCmdline = old }()
	config.ProcCmdline = f.path("proc/cmdline")
	f.write("proc/cmdline", "vos.slot=a quiet\n")
	r := f.runner()
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, nil); err == nil || !strings.Contains(err.Error(), "live ISO") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallCleansStaleMounts(t *testing.T) {
	f, _ := installMachine(t)
	T := filepath.Join(config.RunDir, "target")
	f.setMounts("8:16 "+f.mediumMount()+" iso9660 /dev/sdb", "0:50 "+T+"/root erofs /dev/sdx2", "0:51 "+T+"/root/etc overlay overlay")
	r := f.runner()
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "umount" {
			return "", nil, true
		}
		return "", nil, false
	}
	in, _ := newInstaller(f.env(r), Options{Disk: "sda"}, nil)
	in.cleanStaleMounts(context.Background())
	got := strings.Join(r.callList(), "|")
	if got != "umount "+T+"/root/etc|umount "+T+"/root" {
		t.Fatalf("calls = %s", got)
	}
}
