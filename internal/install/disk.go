package install

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// sysPaths are the kernel and system interfaces the installer reads. They
// are variables so tests can point them at a fake tree.
type sysPaths struct {
	ClassBlock string // /sys/class/block: every block device by kernel name
	DevBlock   string // /sys/dev/block: the same, by major:minor
	Dev        string // device nodes
	Mountinfo  string
	Swaps      string
	EFI        string // exists only when booted through UEFI
	Zoneinfo   string // the running system's tz database
	Localtime  string // the running system's /etc/localtime
	Serial     string // harness console (docs/CONTRACTS.md "Serial lines")
}

var paths = sysPaths{
	ClassBlock: "/sys/class/block",
	DevBlock:   "/sys/dev/block",
	Dev:        "/dev",
	Mountinfo:  "/proc/self/mountinfo",
	Swaps:      "/proc/swaps",
	EFI:        "/sys/firmware/efi",
	Zoneinfo:   "/usr/share/zoneinfo",
	Localtime:  "/etc/localtime",
	Serial:     "/dev/ttyS0",
}

// isBlockDevice reports whether path is a block device node. A variable so
// tests can use plain files as partitions.
var isBlockDevice = func(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0
}

const (
	mib = int64(1) << 20
	gib = int64(1) << 30

	espMiB     = 512
	minSlotMiB = 8 << 10
	maxSlotMiB = 16 << 10
	minDataMiB = 8 << 10

	// liveLabel is the ISO's volume label; the initramfs finds the live
	// medium by it (vos.label=VOS_LIVE).
	liveLabel = "VOS_LIVE"
)

// Partition names, in GPT order (docs/CONTRACTS.md "Disk layout").
var partNames = [4]string{"vos_esp", "vos_a", "vos_b", "vos_data"}

// slotSizeMiB is the size of each A/B slot: three times today's image, so
// images can grow for a long time without repartitioning, within 8-16 GiB.
func slotSizeMiB(imageBytes int64) int64 {
	s := (3*imageBytes + mib - 1) / mib
	return min(max(s, minSlotMiB), maxSlotMiB)
}

// minDiskBytes is the smallest disk that holds the ESP, both slots and an
// 8 GiB data partition.
func minDiskBytes(slotMiB int64) int64 {
	return (espMiB + 2*slotMiB + minDataMiB) * mib
}

// sgdiskArgs creates the four VaporOS partitions on disk in one sgdisk run.
// Slots are 8300 (plain Linux data) on purpose: the "root" or "home" type
// GUIDs would invite systemd-gpt-auto-generator to mount them.
func sgdiskArgs(disk string, slotMiB int64) []string {
	return []string{
		fmt.Sprintf("-n1:0:+%dM", espMiB), "-t1:ef00", "-c1:" + partNames[0],
		fmt.Sprintf("-n2:0:+%dM", slotMiB), "-t2:8300", "-c2:" + partNames[1],
		fmt.Sprintf("-n3:0:+%dM", slotMiB), "-t3:8300", "-c3:" + partNames[2],
		"-n4:0:0", "-t4:8300", "-c4:" + partNames[3],
		disk,
	}
}

// devPath is where the installer opens a device node.
func devPath(name string) string { return filepath.Join(paths.Dev, name) }

// devName is how a device is shown to people and returned by the API.
func devName(name string) string { return "/dev/" + name }

func classDir(name string) string { return filepath.Join(paths.ClassBlock, name) }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// readSys returns a sysfs attribute, trimmed; "" when missing.
func readSys(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// sizeBytes is a block device's size. sysfs counts 512-byte sectors
// whatever the device's logical block size.
func sizeBytes(name string) int64 {
	n, _ := strconv.ParseInt(readSys(filepath.Join(classDir(name), "size")), 10, 64)
	return n * 512
}

func isPartition(name string) bool { return exists(filepath.Join(classDir(name), "partition")) }

// notDisks are kernel name prefixes of block devices that are never an
// install target, even when they look like whole disks.
var notDisks = []string{"loop", "ram", "zram", "sr", "dm-", "md", "nbd", "fd", "mtdblock"}

// physicalDisk reports whether name is a whole disk backed by hardware
// (lsblk TYPE=disk), not a partition, optical drive or virtual device.
func physicalDisk(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\x00") || !exists(classDir(name)) || isPartition(name) {
		return false
	}
	for _, p := range notDisks {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return exists(filepath.Join(classDir(name), "device"))
}

type partInfo struct {
	Name   string // kernel name, e.g. "nvme0n1p2"
	Number int
	Label  string // GPT partition name (PARTNAME)
}

// partitions lists a disk's partitions straight from sysfs, sorted by
// number. lsblk's PARTLABEL/PARTN come from the udev database, which lags
// right after partitioning; the kernel's own view does not.
func partitions(disk string) []partInfo {
	entries, err := os.ReadDir(classDir(disk))
	if err != nil {
		return nil
	}
	var out []partInfo
	for _, e := range entries {
		dir := filepath.Join(classDir(disk), e.Name())
		n, err := strconv.Atoi(readSys(filepath.Join(dir, "partition")))
		if err != nil {
			continue
		}
		p := partInfo{Name: e.Name(), Number: n}
		for _, line := range strings.Split(readSys(filepath.Join(dir, "uevent")), "\n") {
			if v, ok := strings.CutPrefix(line, "PARTNAME="); ok {
				p.Label = v
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// partByNumber is bash vos's part_by_number: the kernel name of partition n
// of disk, or "".
func partByNumber(disk string, n int) partInfo {
	for _, p := range partitions(disk) {
		if p.Number == n {
			return p
		}
	}
	return partInfo{}
}

// partSet holds the kernel names of the four VaporOS partitions.
type partSet struct{ esp, a, b, data string }

func (p partSet) list() []string { return []string{p.esp, p.a, p.b, p.data} }

// vosLayout finds an existing VaporOS installation's partitions by name.
func vosLayout(disk string) (partSet, error) {
	byLabel := map[string]string{}
	for _, p := range partitions(disk) {
		if _, dup := byLabel[p.Label]; !dup {
			byLabel[p.Label] = p.Name
		}
	}
	var ps partSet
	for i, dst := range []*string{&ps.esp, &ps.a, &ps.b, &ps.data} {
		name := byLabel[partNames[i]]
		if name == "" {
			return ps, fmt.Errorf("%s has no VaporOS installation to repair (no %s partition)", devName(disk), partNames[i])
		}
		*dst = name
	}
	return ps, nil
}

// hasVaporOS reports whether disk carries a VaporOS data partition.
func hasVaporOS(disk string) bool {
	for _, p := range partitions(disk) {
		if p.Label == "vos_data" {
			return true
		}
	}
	return false
}

// resolveDisk turns "sda", "/dev/sda" or a /dev/disk/by-* link into the
// kernel name of a whole physical disk.
func resolveDisk(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	var p string
	switch {
	case spec == "":
		return "", errors.New("no disk given")
	case !strings.Contains(spec, "/"):
		p = devPath(spec)
	case strings.HasPrefix(spec, "/dev/"):
		p = filepath.Join(paths.Dev, strings.TrimPrefix(spec, "/dev/"))
	default:
		return "", fmt.Errorf("%q is not a device under /dev", spec)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%s: no such disk", spec)
	}
	name := filepath.Base(real)
	if devDir, err := filepath.EvalSymlinks(paths.Dev); err != nil || filepath.Dir(real) != devDir {
		return "", fmt.Errorf("%s is not a disk", spec)
	}
	if isPartition(name) {
		return "", fmt.Errorf("%s is a partition; choose the whole disk", devName(name))
	}
	if !physicalDisk(name) {
		return "", fmt.Errorf("%s is not a disk VaporOS can be installed on", devName(name))
	}
	return name, nil
}

// checkTarget refuses disks that must not be overwritten: read-only or
// empty ones, the disk holding the live medium, and anything in use.
func checkTarget(disk string) error {
	if !physicalDisk(disk) {
		return fmt.Errorf("%s is not a disk VaporOS can be installed on", devName(disk))
	}
	if readSys(filepath.Join(classDir(disk), "ro")) == "1" {
		return fmt.Errorf("%s is read-only", devName(disk))
	}
	if sizeBytes(disk) == 0 {
		return fmt.Errorf("%s has no medium", devName(disk))
	}
	if !isBlockDevice(devPath(disk)) {
		return fmt.Errorf("%s has no device node", devName(disk))
	}
	mounts, _ := readMountinfo()
	if liveDisks(mounts)[disk] {
		return fmt.Errorf("%s holds the VaporOS installer itself; choose another disk", devName(disk))
	}
	if why, busy := busyDisks(mounts)[disk]; busy {
		return fmt.Errorf("%s is in use (%s)", devName(disk), why)
	}
	names := []string{disk}
	for _, p := range partitions(disk) {
		names = append(names, p.Name)
	}
	for _, n := range names {
		if h := holders(n); len(h) > 0 {
			return fmt.Errorf("%s is in use by %s (LVM, RAID or encryption)", devName(n), strings.Join(h, ", "))
		}
	}
	return nil
}

func holders(name string) []string {
	entries, err := os.ReadDir(filepath.Join(classDir(name), "holders"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// mountEntry is one line of /proc/self/mountinfo.
type mountEntry struct {
	MajMin     string
	Mountpoint string
	FSType     string
	Source     string
}

func readMountinfo() ([]mountEntry, error) {
	f, err := os.Open(paths.Mountinfo)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseMountinfo(f)
}

// parseMountinfo parses proc(5) mountinfo: "id parent maj:min root
// mountpoint options [optional...] - fstype source superoptions".
func parseMountinfo(r io.Reader) ([]mountEntry, error) {
	var out []mountEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if len(f) < 5 || sep < 0 || sep+2 >= len(f) {
			continue
		}
		out = append(out, mountEntry{
			MajMin:     f[2],
			Mountpoint: unescapeMount(f[4]),
			FSType:     f[sep+1],
			Source:     unescapeMount(f[sep+2]),
		})
	}
	return out, sc.Err()
}

// unescapeMount undoes the kernel's octal escapes (\040 for a space, ...).
func unescapeMount(s string) string {
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

// mountContaining returns the mount that holds path: the deepest mount
// point above it, and the topmost of several stacked on the same point.
func mountContaining(mounts []mountEntry, path string) *mountEntry {
	path = filepath.Clean(path)
	var best *mountEntry
	for i := range mounts {
		mp := mounts[i].Mountpoint
		if mp != "/" && path != mp && !strings.HasPrefix(path, mp+"/") {
			continue
		}
		if best == nil || len(mp) >= len(best.Mountpoint) {
			best = &mounts[i]
		}
	}
	return best
}

// mountDisks returns the whole disks behind a mount. Both the device
// number and the source path are followed: btrfs reports an anonymous
// device number, and some sources are not /dev paths at all.
func mountDisks(m mountEntry, mounts []mountEntry) []string {
	var out []string
	if real, err := filepath.EvalSymlinks(filepath.Join(paths.DevBlock, m.MajMin)); err == nil {
		out = append(out, disksOf(filepath.Base(real), mounts, 0)...)
	}
	if rest, ok := strings.CutPrefix(m.Source, "/dev/"); ok {
		if real, err := filepath.EvalSymlinks(filepath.Join(paths.Dev, rest)); err == nil {
			out = append(out, disksOf(filepath.Base(real), mounts, 0)...)
		}
	}
	return out
}

// disksOf resolves a block device to the whole disks it lives on: a
// partition to its disk, a device-mapper or md device to its slaves, and a
// loop device to the disk holding its backing file.
func disksOf(name string, mounts []mountEntry, depth int) []string {
	real, err := filepath.EvalSymlinks(classDir(name))
	if err != nil || depth > 8 {
		return nil
	}
	if exists(filepath.Join(real, "partition")) {
		return disksOf(filepath.Base(filepath.Dir(real)), mounts, depth+1)
	}
	if slaves, err := os.ReadDir(filepath.Join(real, "slaves")); err == nil && len(slaves) > 0 {
		var out []string
		for _, s := range slaves {
			out = append(out, disksOf(s.Name(), mounts, depth+1)...)
		}
		return out
	}
	if strings.HasPrefix(name, "loop") {
		if backing := readSys(filepath.Join(real, "loop", "backing_file")); backing != "" {
			if m := mountContaining(mounts, backing); m != nil {
				if real, err := filepath.EvalSymlinks(filepath.Join(paths.DevBlock, m.MajMin)); err == nil {
					return disksOf(filepath.Base(real), mounts, depth+1)
				}
			}
		}
	}
	return []string{name}
}

// liveDisks returns the disks holding the live medium, found two ways so
// that neither can be fooled on its own: the mount behind
// config.LiveMedium (a CD, a USB partition, or the whole USB disk when an
// isohybrid image is mounted from sector 0) and the VOS_LIVE label.
func liveDisks(mounts []mountEntry) map[string]bool {
	set := map[string]bool{}
	if m := mountContaining(mounts, config.LiveMedium); m != nil && m.Mountpoint != "/" {
		for _, d := range mountDisks(*m, mounts) {
			set[d] = true
		}
	}
	if real, err := filepath.EvalSymlinks(filepath.Join(paths.Dev, "disk", "by-label", liveLabel)); err == nil {
		for _, d := range disksOf(filepath.Base(real), mounts, 0) {
			set[d] = true
		}
	}
	return set
}

// busyDisks maps each disk that backs a mounted filesystem or active swap
// to the reason.
func busyDisks(mounts []mountEntry) map[string]string {
	busy := map[string]string{}
	for _, m := range mounts {
		for _, d := range mountDisks(m, mounts) {
			if _, seen := busy[d]; !seen {
				busy[d] = "mounted at " + m.Mountpoint
			}
		}
	}
	if b, err := os.ReadFile(paths.Swaps); err == nil {
		for i, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) == 0 || !strings.HasPrefix(f[0], "/dev/") {
				continue
			}
			real, err := filepath.EvalSymlinks(filepath.Join(paths.Dev, strings.TrimPrefix(f[0], "/dev/")))
			if err != nil {
				continue
			}
			for _, d := range disksOf(filepath.Base(real), mounts, 0) {
				if _, seen := busy[d]; !seen {
					busy[d] = "swap"
				}
			}
		}
	}
	return busy
}

// sysModel, sysRemovable and sysTransport describe a disk from sysfs when
// lsblk (storage.ScanDisks) did not.
func sysModel(name string) string {
	return strings.TrimSpace(readSys(filepath.Join(classDir(name), "device", "model")))
}

func sysRemovable(name string) bool {
	return readSys(filepath.Join(classDir(name), "removable")) == "1"
}

func sysTransport(name string) string {
	real, _ := filepath.EvalSymlinks(classDir(name))
	switch {
	case strings.Contains(real, "/usb"):
		return "usb"
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "mmcblk"):
		return "mmc"
	case strings.Contains(real, "/virtio"):
		return "virtio"
	case strings.Contains(real, "/ata"):
		return "sata"
	}
	return ""
}

// humanBytes formats a size for messages ("476 GiB", "1.9 GiB", "512 MiB").
func humanBytes(n int64) string {
	switch {
	case n >= 100*gib:
		return fmt.Sprintf("%d GiB", n/gib)
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	default:
		return fmt.Sprintf("%d MiB", n/mib)
	}
}
