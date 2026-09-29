package boot

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Every VaporOS disk names its partitions vos_esp, vos_a, vos_b and
// vos_data. With two such disks attached (an old install kept for its
// games, say) /dev/disk/by-partlabel/<name> points at whichever one udev
// saw last, so an update could land on the wrong disk. The installer
// therefore records the disk's GPT disk GUID in the machine kernel args
// (vos.disk=), and the initramfs hook and the updater look for partitions
// on that disk only. Installs from before vos.disk go by the disk the
// running root came from, then by label.

// DiskArg is the kernel argument naming the boot disk by GPT disk GUID.
const DiskArg = "vos.disk"

// Where the disk lookups read; tests point them into a temp dir.
var (
	SysClassBlock = "/sys/class/block"
	SysDevBlock   = "/sys/dev/block"
	DevDir        = "/dev"
	PartLabelDir  = "/dev/disk/by-partlabel"
	MountInfoPath = "/proc/self/mountinfo"
)

// DiskGUID reads the GPT disk GUID of the whole disk at dev, lowercase as
// blkid prints PTUUID. The header is LBA 1: byte 512, or 4096 on a disk
// with 4 KiB logical sectors.
func DiskGUID(dev string) (string, error) {
	f, err := os.Open(dev)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 512)
	for _, off := range []int64{512, 4096} {
		if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s: %w", dev, err)
		}
		if guid, ok := parseGPTHeader(buf); ok {
			return guid, nil
		}
	}
	return "", fmt.Errorf("%s has no GPT partition table", dev)
}

// parseGPTHeader checks a GPT header's signature and CRC and returns its
// disk GUID.
func parseGPTHeader(h []byte) (string, bool) {
	if len(h) < 92 || string(h[:8]) != "EFI PART" {
		return "", false
	}
	size := binary.LittleEndian.Uint32(h[12:16])
	if size < 92 || int(size) > len(h) {
		return "", false
	}
	hdr := append([]byte(nil), h[:size]...)
	clear(hdr[16:20])
	if crc32.ChecksumIEEE(hdr) != binary.LittleEndian.Uint32(h[16:20]) {
		return "", false
	}
	return formatGUID(h[56:72]), true
}

// formatGUID formats a GUID as stored on disk: the first three fields are
// little-endian, the last two big-endian.
func formatGUID(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", binary.LittleEndian.Uint32(b[0:4]),
		binary.LittleEndian.Uint16(b[4:6]), binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
}

// WithDiskArg returns cmdline with its vos.disk argument set to guid; an
// empty guid drops it.
func WithDiskArg(cmdline, guid string) string {
	var out []string
	for _, a := range SplitArgs(cmdline) {
		if a != DiskArg && !strings.HasPrefix(a, DiskArg+"=") {
			out = append(out, a)
		}
	}
	if guid != "" {
		out = append(out, DiskArg+"="+strings.ToLower(guid))
	}
	return strings.Join(out, " ")
}

// DiskArgOf returns the vos.disk value in cmdline, or "".
func DiskArgOf(cmdline string) string {
	v := ""
	for _, a := range SplitArgs(cmdline) {
		if s, ok := strings.CutPrefix(a, DiskArg+"="); ok {
			v = s
		}
	}
	return v
}

// Partition returns the device node of the VaporOS partition called name
// (vos_a, vos_b, ...) on the disk this system booted from.
func Partition(name string) (string, error) {
	disk, err := bootDisk(name)
	if err != nil {
		return "", err
	}
	if disk == "" {
		return filepath.Join(PartLabelDir, name), nil
	}
	p := partitionOn(disk, name)
	if p == "" {
		return "", fmt.Errorf("/dev/%s has no %s partition", disk, name)
	}
	return filepath.Join(DevDir, p), nil
}

// bootDisk is the kernel name of the disk to find partition name on: the
// one vos.disk names, else the one the running root is on (when it has such
// a partition), else "" for "go by label".
func bootDisk(name string) (string, error) {
	root, _ := mountDisk("/")
	guid, ok := config.KernelArg(DiskArg)
	if !ok || guid == "" {
		if root != "" && partitionOn(root, name) != "" {
			return root, nil
		}
		return "", nil
	}
	guid = strings.ToLower(guid)
	entries, err := os.ReadDir(SysClassBlock)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		disk := e.Name()
		if isPartition(disk) || partitionOn(disk, name) == "" {
			continue
		}
		if g, err := DiskGUID(filepath.Join(DevDir, disk)); err == nil && g == guid {
			found = append(found, disk)
		}
	}
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("no disk with GPT disk GUID %s (%s=) has a %s partition", guid, DiskArg, name)
	case len(found) == 1 && (root == "" || root == found[0]):
		return found[0], nil
	case len(found) == 1:
		return "", fmt.Errorf("the running system is on /dev/%s, but %s=%s is /dev/%s", root, DiskArg, guid, found[0])
	}
	// A disk cloned block for block keeps its GUID: go by the running root.
	for _, d := range found {
		if d == root {
			return d, nil
		}
	}
	return "", fmt.Errorf("disks %s all have GPT disk GUID %s; disconnect the copy", strings.Join(found, ", "), guid)
}

// CheckESPDisk fails if the filesystem mounted at esp is on another disk
// than the running root. When either cannot be told (not a mount point, a
// root that is not on a disk), there is nothing to compare.
func CheckESPDisk(esp string) error {
	espDisk, ok := mountDisk(esp)
	if !ok {
		return nil
	}
	root, ok := mountDisk("/")
	if !ok || espDisk == root {
		return nil
	}
	return fmt.Errorf("the ESP at %s is on /dev/%s, but VaporOS runs from /dev/%s: another disk with VaporOS partitions is attached; disconnect it or erase it", esp, espDisk, root)
}

func isPartition(name string) bool {
	_, err := os.Stat(filepath.Join(SysClassBlock, name, "partition"))
	return err == nil
}

// partitionOn returns the kernel name of the partition on disk whose GPT
// name is name, or "". The kernel's own uevent is read, not the udev
// database.
func partitionOn(disk, name string) string {
	dir := filepath.Join(SysClassBlock, disk)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n, "partition")); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n, "uevent"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "PARTNAME="+name {
				return n
			}
		}
	}
	return ""
}

// mountDisk returns the whole disk behind the filesystem mounted exactly at
// mountpoint (the topmost of stacked mounts).
func mountDisk(mountpoint string) (string, bool) {
	f, err := os.Open(MountInfoPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	mountpoint = filepath.Clean(mountpoint)
	majmin := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) >= 5 && unescapeOctal(fl[4]) == mountpoint {
			majmin = fl[2]
		}
	}
	if majmin == "" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(filepath.Join(SysDevBlock, majmin))
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(real, "partition")); err == nil {
		return filepath.Base(filepath.Dir(real)), true
	}
	return filepath.Base(real), true
}

// unescapeOctal decodes mountinfo's \ooo escapes.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
