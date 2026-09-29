package boot

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// A GUID as GPT stores it, and how blkid prints it.
var (
	guidBytes = [16]byte{0x78, 0x56, 0x34, 0x12, 0x34, 0x12, 0x78, 0x56, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}
	guidText  = "12345678-1234-5678-1234-56789abcdef0"
)

func gptHeader(guid [16]byte) []byte {
	h := make([]byte, 512)
	copy(h, "EFI PART")
	binary.LittleEndian.PutUint32(h[8:], 0x00010000)
	binary.LittleEndian.PutUint32(h[12:], 92)
	copy(h[56:72], guid[:])
	binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
	return h
}

// writeDisk writes a disk image whose GPT header sits at LBA 1 of sector.
func writeDisk(t *testing.T, path string, guid [16]byte, sector int) {
	t.Helper()
	b := make([]byte, 2*sector)
	copy(b[sector:], gptHeader(guid))
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiskGUID(t *testing.T) {
	dir := t.TempDir()
	for _, sector := range []int{512, 4096} {
		p := filepath.Join(dir, fmt.Sprint("disk", sector))
		writeDisk(t, p, guidBytes, sector)
		if got, err := DiskGUID(p); err != nil || got != guidText {
			t.Fatalf("%d-byte sectors: %q %v", sector, got, err)
		}
	}
	// A corrupt header (bad CRC) or none at all is no GUID.
	p := filepath.Join(dir, "corrupt")
	h := gptHeader(guidBytes)
	h[60] ^= 1
	os.WriteFile(p, append(make([]byte, 512), h...), 0o644)
	if _, err := DiskGUID(p); err == nil {
		t.Fatal("accepted a header with a bad CRC")
	}
	os.WriteFile(p, make([]byte, 8192), 0o644)
	if _, err := DiskGUID(p); err == nil {
		t.Fatal("found a GUID on a blank disk")
	}
}

func TestDiskArg(t *testing.T) {
	if got := WithDiskArg("video=DP-1:e vos.disk=OLD", "ABC"); got != "video=DP-1:e vos.disk=abc" {
		t.Fatalf("got %q", got)
	}
	if got := WithDiskArg("vos.disk=x video=DP-1:e", ""); got != "video=DP-1:e" {
		t.Fatalf("got %q", got)
	}
	if got := DiskArgOf("quiet vos.disk=abc video=x"); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

// fakeDisks is sysfs, /dev and mountinfo with VaporOS disks.
type fakeDisks struct {
	t    *testing.T
	root string
}

func newFakeDisks(t *testing.T) *fakeDisks {
	t.Helper()
	root := t.TempDir()
	vars := []*string{&SysClassBlock, &SysDevBlock, &DevDir, &PartLabelDir, &MountInfoPath, &config.ProcCmdline}
	saved := make([]string, len(vars))
	for i, v := range vars {
		saved[i] = *v
	}
	t.Cleanup(func() {
		for i, v := range vars {
			*v = saved[i]
		}
	})
	SysClassBlock = filepath.Join(root, "sys/class/block")
	SysDevBlock = filepath.Join(root, "sys/dev/block")
	DevDir = filepath.Join(root, "dev")
	PartLabelDir = filepath.Join(root, "dev/disk/by-partlabel")
	MountInfoPath = filepath.Join(root, "mountinfo")
	config.ProcCmdline = filepath.Join(root, "cmdline")
	for _, d := range []string{SysClassBlock, SysDevBlock, PartLabelDir} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(config.ProcCmdline, []byte("vos.slot=a quiet\n"), 0o644)
	return &fakeDisks{t: t, root: root}
}

// add adds a disk (major number major) with the four VaporOS partitions.
func (f *fakeDisks) add(disk string, major int, guid [16]byte) {
	devdir := filepath.Join(f.root, "sys/devices", disk)
	os.MkdirAll(devdir, 0o755)
	os.Symlink(devdir, filepath.Join(SysClassBlock, disk))
	os.Symlink(devdir, filepath.Join(SysDevBlock, fmt.Sprintf("%d:0", major)))
	writeDisk(f.t, filepath.Join(DevDir, disk), guid, 512)
	for i, label := range []string{"vos_esp", "vos_a", "vos_b", "vos_data"} {
		part := fmt.Sprintf("%s%d", disk, i+1)
		pdir := filepath.Join(devdir, part)
		os.MkdirAll(pdir, 0o755)
		os.WriteFile(filepath.Join(pdir, "partition"), []byte(fmt.Sprint(i+1)), 0o644)
		os.WriteFile(filepath.Join(pdir, "uevent"), []byte("DEVTYPE=partition\nPARTNAME="+label+"\n"), 0o644)
		os.Symlink(pdir, filepath.Join(SysClassBlock, part))
		os.Symlink(pdir, filepath.Join(SysDevBlock, fmt.Sprintf("%d:%d", major, i+1)))
		os.WriteFile(filepath.Join(DevDir, part), nil, 0o644)
		// udev's by-partlabel links: the last disk added wins, as it may.
		os.Remove(filepath.Join(PartLabelDir, label))
		os.Symlink(filepath.Join(DevDir, part), filepath.Join(PartLabelDir, label))
	}
}

func (f *fakeDisks) cmdline(s string) { os.WriteFile(config.ProcCmdline, []byte(s+"\n"), 0o644) }

// mounts writes mountinfo; each entry is "majmin mountpoint".
func (f *fakeDisks) mounts(entries ...string) {
	var b strings.Builder
	for i, e := range entries {
		mm, mp, _ := strings.Cut(e, " ")
		fmt.Fprintf(&b, "%d 1 %s / %s rw - fs /dev/x rw\n", 20+i, mm, mp)
	}
	os.WriteFile(MountInfoPath, []byte(b.String()), 0o644)
}

func TestPartitionOnBootDisk(t *testing.T) {
	f := newFakeDisks(t)
	other := guidBytes
	other[0] = 0x99
	f.add("sda", 8, guidBytes)
	f.add("sdb", 9, other) // the by-partlabel links point here

	// No vos.disk, no root to go by: the label, as before.
	if got, err := Partition("vos_b"); err != nil || got != filepath.Join(PartLabelDir, "vos_b") {
		t.Fatalf("by label: %q %v", got, err)
	}
	// The disk the running root is on.
	f.mounts("8:2 /")
	if got, err := Partition("vos_b"); err != nil || got != filepath.Join(DevDir, "sda3") {
		t.Fatalf("root disk: %q %v", got, err)
	}
	// vos.disk names the disk, whatever the labels say.
	f.mounts()
	f.cmdline("vos.slot=a vos.disk=" + strings.ToUpper(guidText))
	if got, err := Partition("vos_b"); err != nil || got != filepath.Join(DevDir, "sda3") {
		t.Fatalf("vos.disk: %q %v", got, err)
	}
	// ... and never falls back to another disk.
	f.cmdline("vos.slot=a vos.disk=00000000-0000-0000-0000-000000000000")
	if got, err := Partition("vos_b"); err == nil {
		t.Fatalf("unknown vos.disk resolved to %q", got)
	}
	// A root on another disk than vos.disk names is refused.
	f.cmdline("vos.slot=a vos.disk=" + guidText)
	f.mounts("9:2 /")
	if got, err := Partition("vos_b"); err == nil {
		t.Fatalf("root on sdb, vos.disk sda: resolved to %q", got)
	}
	// A block-for-block clone shares the GUID: the running root decides.
	f.add("sdc", 10, guidBytes)
	f.mounts("10:2 /")
	if got, err := Partition("vos_b"); err != nil || got != filepath.Join(DevDir, "sdc3") {
		t.Fatalf("clone: %q %v", got, err)
	}
	f.mounts()
	if got, err := Partition("vos_b"); err == nil {
		t.Fatalf("two disks with one GUID resolved to %q", got)
	}
}

func TestCheckESPDisk(t *testing.T) {
	f := newFakeDisks(t)
	f.add("sda", 8, guidBytes)
	f.add("sdb", 9, guidBytes)
	if err := CheckESPDisk("/efi"); err != nil {
		t.Fatalf("nothing mounted: %v", err)
	}
	f.mounts("8:2 /", "0:40 /efi", "8:1 /efi") // the automount, then the ESP on it
	if err := CheckESPDisk("/efi"); err != nil {
		t.Fatalf("same disk: %v", err)
	}
	f.mounts("8:2 /", "0:40 /efi", "9:1 /efi")
	if err := CheckESPDisk("/efi"); err == nil || !strings.Contains(err.Error(), "another disk") {
		t.Fatalf("ESP of another disk: %v", err)
	}
	// A live system's root is no disk: nothing to compare.
	f.mounts("0:21 /", "9:1 /efi")
	if err := CheckESPDisk("/efi"); err != nil {
		t.Fatalf("live root: %v", err)
	}
}
