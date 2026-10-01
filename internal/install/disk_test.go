package install

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSlotSize(t *testing.T) {
	for _, tc := range []struct {
		image int64
		want  int64
	}{
		{0, 8192},
		{1 * gib, 8192},
		{2*gib + 600*mib, 8192},   // 7944 MiB -> clamped up
		{3 * gib, 9216},           // exactly 3x
		{3*gib + 1, 9217},         // rounds up to whole MiB
		{5*gib + 1000*mib, 16384}, // 18360 MiB -> clamped down
		{40 * gib, 16384},
	} {
		if got := slotSizeMiB(tc.image); got != tc.want {
			t.Errorf("slotSizeMiB(%d) = %d, want %d", tc.image, got, tc.want)
		}
	}
}

func TestMinDiskBytes(t *testing.T) {
	// 512 MiB + 2 x slot + 8 GiB (docs/CONTRACTS.md "Disk layout").
	if got, want := minDiskBytes(8192), (512+2*8192+8192)*mib; got != want {
		t.Errorf("minDiskBytes(8192) = %d, want %d", got, want)
	}
	if got, want := minDiskBytes(16384), int64(40*gib+512*mib); got != want {
		t.Errorf("minDiskBytes(16384) = %d, want %d", got, want)
	}
}

func TestSgdiskArgs(t *testing.T) {
	got := strings.Join(sgdiskArgs("/dev/nvme0n1", 9216), " ")
	want := "-n1:0:+512M -t1:ef00 -c1:vos_esp " +
		"-n2:0:+9216M -t2:8300 -c2:vos_a " +
		"-n3:0:+9216M -t3:8300 -c3:vos_b " +
		"-n4:0:0 -t4:8300 -c4:vos_data /dev/nvme0n1"
	if got != want {
		t.Errorf("sgdiskArgs =\n  %s\nwant\n  %s", got, want)
	}
}

func TestParseMountinfo(t *testing.T) {
	in := `22 1 0:21 / / rw,relatime shared:1 - overlay overlay rw,lowerdir=/x
36 22 8:17 / /run/vos/medium ro,relatime shared:2 master:1 - iso9660 /dev/sdb1 ro,nojoliet
37 22 0:45 /@home /mnt/my\040games rw shared:3 - btrfs /dev/sdc1 rw,ssd
garbage line
`
	got, err := parseMountinfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []mountEntry{
		{"0:21", "/", "overlay", "overlay"},
		{"8:17", "/run/vos/medium", "iso9660", "/dev/sdb1"},
		{"0:45", "/mnt/my games", "btrfs", "/dev/sdc1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if m := mountContaining(got, "/run/vos/medium/vos"); m == nil || m.MajMin != "8:17" {
		t.Errorf("mountContaining = %+v", m)
	}
	if m := mountContaining(got, "/run/vos/mediumX"); m == nil || m.Mountpoint != "/" {
		t.Errorf("mountContaining matched a sibling: %+v", m)
	}
	if got := unescapeMount(`a\134b\011c\`); got != "a\\b\tc\\" {
		t.Errorf("unescapeMount = %q", got)
	}
}

func keys(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestLiveDisks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeSys) []string // returns mountinfo entries
		want  string
	}{
		{"optical drive", func(f *fakeSys) []string {
			f.addVirtual("sr0", "11:0")
			return []string{"11:0 " + f.mediumMount() + " iso9660 /dev/sr0"}
		}, "sr0"},
		{"usb partition", func(f *fakeSys) []string {
			f.addPart("sdb", "sdb1", "8:17", 1, "", 2*gib)
			return []string{"8:17 " + f.mediumMount() + " iso9660 /dev/sdb1"}
		}, "sdb"},
		{"isohybrid whole disk", func(f *fakeSys) []string {
			return []string{"8:16 " + f.mediumMount() + " iso9660 /dev/sdb"}
		}, "sdb"},
		{"loop-mounted iso file", func(f *fakeSys) []string {
			f.addPart("sdc", "sdc2", "8:34", 2, "", 100*gib)
			dir := f.addVirtual("loop0", "7:0")
			f.write(filepath.Join(dir, "loop/backing_file"), "/mnt/isos/vaporos.iso\n")
			return []string{"8:34 /mnt/isos ext4 /dev/sdc2", "7:0 " + f.mediumMount() + " iso9660 /dev/loop0"}
		}, "sdc"},
		{"iso file on a ventoy stick", func(f *fakeSys) []string {
			f.addPart("sdc", "sdc1", "8:33", 1, "", 30*gib)
			dir := f.addVirtual("loop1", "7:1")
			f.write(filepath.Join(dir, "loop/backing_file"), "/run/vos/host/isos/vaporos.iso\n")
			return []string{"8:33 /run/vos/host exfat /dev/sdc1", "7:1 " + f.mediumMount() + " iso9660 /dev/loop1"}
		}, "sdc"},
		{"device mapper", func(f *fakeSys) []string {
			f.addPart("sdc", "sdc1", "8:33", 1, "", 100*gib)
			dir := f.addVirtual("dm-0", "254:0")
			f.mkdir(filepath.Join(dir, "slaves/sdc1"))
			return []string{"254:0 " + f.mediumMount() + " iso9660 /dev/mapper/ventoy"}
		}, "sdc"},
		{"label only", func(f *fakeSys) []string {
			f.addPart("sdb", "sdb1", "8:17", 1, "", 2*gib)
			f.symlink("../../sdb1", "dev/disk/by-label/VOS_LIVE")
			return nil
		}, "sdb"},
		{"mount and label disagree", func(f *fakeSys) []string {
			f.symlink("../../sdc", "dev/disk/by-label/VOS_LIVE")
			return []string{"8:16 " + f.mediumMount() + " iso9660 /dev/sdb"}
		}, "sdb,sdc"},
		{"not live", func(f *fakeSys) []string { return nil }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSys(t)
			f.addDisk("sda", "8:0", "ata1", 500*gib, "SSD")
			f.addDisk("sdb", "8:16", "usb1", 16*gib, "Stick")
			f.addDisk("sdc", "8:32", "ata2", 1000*gib, "HDD")
			f.setMounts(tc.setup(f)...)
			mounts, err := readMountinfo()
			if err != nil {
				t.Fatal(err)
			}
			if got := keys(liveDisks(mounts)); got != tc.want {
				t.Errorf("liveDisks = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBusyDisks(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("nvme0n1", "259:0", "nvme", 500*gib, "NVMe")
	f.addPart("nvme0n1", "nvme0n1p2", "259:2", 2, "", 400*gib)
	f.addDisk("sdc", "8:32", "ata2", 1000*gib, "HDD")
	f.addPart("sdc", "sdc1", "8:33", 1, "", 1000*gib)
	f.addDisk("sdd", "8:48", "ata3", 1000*gib, "HDD")
	f.addPart("sdd", "sdd2", "8:50", 2, "", 8*gib)
	f.addDisk("sde", "8:64", "ata4", 1000*gib, "idle")
	// btrfs reports an anonymous device number; its source still counts.
	f.setMounts("259:2 /home ext4 /dev/nvme0n1p2", "0:45 /mnt/games btrfs /dev/sdc1")
	f.write("proc/swaps", "Filename\tType\tSize\tUsed\tPriority\n/dev/sdd2 partition 8388604 0 -2\n/dev/zram0 partition 1 0 100\n")
	mounts, _ := readMountinfo()
	busy := busyDisks(mounts)
	for disk, want := range map[string]string{"nvme0n1": "mounted at /home", "sdc": "mounted at /mnt/games", "sdd": "swap"} {
		if busy[disk] != want {
			t.Errorf("busy[%s] = %q, want %q", disk, busy[disk], want)
		}
	}
	if _, ok := busy["sde"]; ok {
		t.Error("idle disk reported busy")
	}
}

func TestPartitionsFromSysfs(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("nvme0n1", "259:0", "nvme", 500*gib, "NVMe")
	f.vosParts("nvme0n1", 259, 8192)
	// Kernel partition names are not always <disk><n>.
	ps := partitions("nvme0n1")
	if len(ps) != 4 {
		t.Fatalf("partitions = %+v", ps)
	}
	if p := partByNumber("nvme0n1", 3); p.Name != "nvme0n13" || p.Label != "vos_b" {
		t.Errorf("partByNumber(3) = %+v", p)
	}
	if p := partByNumber("nvme0n1", 7); p.Name != "" {
		t.Errorf("partByNumber(7) = %+v", p)
	}
	layout, err := vosLayout("nvme0n1")
	if err != nil || layout.a != "nvme0n12" || layout.data != "nvme0n14" {
		t.Errorf("vosLayout = %+v, %v", layout, err)
	}
	if !hasVaporOS("nvme0n1") {
		t.Error("hasVaporOS = false")
	}
	if got := sizeBytes("nvme0n12"); got != 8*gib {
		t.Errorf("sizeBytes = %d", got)
	}
	f.addDisk("sda", "8:0", "ata1", 64*gib, "SSD")
	if hasVaporOS("sda") {
		t.Error("hasVaporOS(empty disk) = true")
	}
	if _, err := vosLayout("sda"); err == nil {
		t.Error("vosLayout(empty disk) succeeded")
	}
}

func TestResolveDisk(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("sda", "8:0", "ata1", 64*gib, "SSD")
	f.addPart("sda", "sda1", "8:1", 1, "", 1*gib)
	f.addVirtual("sr0", "11:0")
	f.addVirtual("loop0", "7:0")
	f.symlink("../../sda", "dev/disk/by-id/ata-Test_SSD")
	for _, tc := range []struct{ spec, want, err string }{
		{"/dev/sda", "sda", ""},
		{"sda", "sda", ""},
		{" /dev/disk/by-id/ata-Test_SSD ", "sda", ""},
		{"/dev/sda1", "", "is a partition"},
		{"sr0", "", "not a disk VaporOS can be installed on"},
		{"/dev/loop0", "", "not a disk VaporOS can be installed on"},
		{"/dev/sdz", "", "no such disk"},
		{"/etc/passwd", "", "not a device under /dev"},
		{"/dev/../etc/passwd", "", "no such disk"},
		{"", "", "no disk given"},
	} {
		got, err := resolveDisk(tc.spec)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("resolveDisk(%q) = %q, %v; want error %q", tc.spec, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("resolveDisk(%q) = %q, %v; want %q", tc.spec, got, err, tc.want)
		}
	}
}

func TestSysfsDescription(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("sdb", "8:16", "usb1/1-1/1-1:1.0/host6/target6:0:0/6:0:0:0", 16*gib, "Stick")
	f.addDisk("nvme0n1", "259:0", "0000:01:00.0/nvme/nvme0", 500*gib, "Samsung 990")
	f.addDisk("vda", "253:0", "virtio2", 32*gib, "")
	f.write(filepath.Join(f.disks["sdb"], "removable"), "1")
	for name, want := range map[string]string{"sdb": "usb", "nvme0n1": "nvme", "vda": "virtio"} {
		if got := sysTransport(name); got != want {
			t.Errorf("sysTransport(%s) = %q, want %q", name, got, want)
		}
	}
	if !sysRemovable("sdb") || sysRemovable("nvme0n1") {
		t.Error("sysRemovable wrong")
	}
	if got := sysModel("nvme0n1"); got != "Samsung 990" {
		t.Errorf("sysModel = %q", got)
	}
	if got := humanBytes(500 * gib); got != "500 GiB" {
		t.Errorf("humanBytes = %q", got)
	}
	if got := humanBytes(1536 * mib); got != "1.5 GiB" {
		t.Errorf("humanBytes = %q", got)
	}
	os.Remove(f.path("dev/vda"))
	if err := checkTarget("vda"); err == nil || !strings.Contains(err.Error(), "no device node") {
		t.Errorf("checkTarget without a node = %v", err)
	}
}
