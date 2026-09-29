package install

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/storage"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// ProbeResult is GET /install/probe.
type ProbeResult struct {
	Disks    []ProbeDisk     `json:"disks"`
	IPs      []string        `json:"ips"`
	Timezone string          `json:"timezone"`
	GPU      display.GPUInfo `json:"gpu"`
}

// ProbeDisk is one whole disk the installer could write to.
type ProbeDisk struct {
	Path           string         `json:"path"`
	Model          string         `json:"model"`
	Size           int64          `json:"size"`
	Transport      string         `json:"transport"`
	Removable      bool           `json:"removable"`
	IsLive         bool           `json:"is_live"`     // holds the installer; the UI hides it
	HasVaporOS     bool           `json:"has_vaporos"` // repair is possible
	SteamLibraries []SteamLibrary `json:"steam_libraries"`
}

// SteamLibrary is a Steam library folder found on a filesystem. Path is
// the folder inside that filesystem ("/" when it is the root).
type SteamLibrary struct {
	UUID  string `json:"uuid"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// diskGroup is a whole disk with the filesystems on it.
type diskGroup struct {
	name  string
	disk  storage.Disk
	parts []storage.Disk
}

// groupDisks folds storage.ScanDisks' flat list into whole physical disks
// with their partitions. Parent may be a kernel name or a /dev path.
func groupDisks(list []storage.Disk) []*diskGroup {
	byName := map[string]*diskGroup{}
	var out []*diskGroup
	for _, d := range list {
		name := filepath.Base(d.Path)
		if d.Parent != "" || byName[name] != nil || !physicalDisk(name) {
			continue
		}
		g := &diskGroup{name: name, disk: d}
		byName[name] = g
		out = append(out, g)
	}
	for _, d := range list {
		if d.Parent == "" {
			continue
		}
		if g := byName[filepath.Base(d.Parent)]; g != nil {
			g.parts = append(g.parts, d)
		}
	}
	return out
}

// sysfsDisks lists physical disks from sysfs alone, for when lsblk fails:
// the installer must still offer a disk to install to.
func sysfsDisks() []*diskGroup {
	entries, err := os.ReadDir(paths.ClassBlock)
	if err != nil {
		return nil
	}
	var out []*diskGroup
	for _, e := range entries {
		if physicalDisk(e.Name()) {
			out = append(out, &diskGroup{name: e.Name(), disk: storage.Disk{Path: devName(e.Name())}})
		}
	}
	return out
}

func (s *Service) probe(ctx context.Context) ProbeResult {
	res := ProbeResult{Disks: []ProbeDisk{}, IPs: sysd.LocalIPs(), Timezone: guessTimezone(), GPU: s.env.gpu()}
	if res.IPs == nil {
		res.IPs = []string{}
	}
	scanned, err := s.env.scanDisks(ctx)
	if err != nil {
		s.env.logf("install: listing disks: %v", err)
	}
	groups := groupDisks(scanned)
	if len(groups) == 0 {
		groups = sysfsDisks()
	}
	mounts, _ := readMountinfo()
	live := liveDisks(mounts)

	// Library detection mounts filesystems; an install must never start
	// while one of them is mounted (see runJob), and none are mounted
	// once one runs.
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	mountAllowed := !s.running()

	for _, g := range groups {
		d := ProbeDisk{
			Path:           devName(g.name),
			Model:          strings.TrimSpace(g.disk.Model),
			Size:           g.disk.Size,
			Transport:      g.disk.Transport,
			Removable:      g.disk.Removable || sysRemovable(g.name),
			IsLive:         live[g.name],
			HasVaporOS:     hasVaporOS(g.name),
			SteamLibraries: []SteamLibrary{},
		}
		if d.Model == "" {
			d.Model = sysModel(g.name)
		}
		if d.Size == 0 {
			d.Size = sizeBytes(g.name)
		}
		if d.Transport == "" {
			d.Transport = sysTransport(g.name)
		}
		for _, p := range g.parts {
			if p.Label == "vos_data" {
				d.HasVaporOS = true
			}
		}
		if !d.IsLive {
			d.SteamLibraries = s.steamLibraries(ctx, g, mountAllowed)
		}
		res.Disks = append(res.Disks, d)
	}
	sort.Slice(res.Disks, func(i, j int) bool { return res.Disks[i].Path < res.Disks[j].Path })
	return res
}

// steamLibraries finds Steam libraries on a disk's filesystems, skipping
// VaporOS's own partitions. Results are cached per filesystem UUID for the
// life of the installer, since finding them may mean mounting. Caller
// holds s.scanMu.
func (s *Service) steamLibraries(ctx context.Context, g *diskGroup, mountAllowed bool) []SteamLibrary {
	labels := map[string]string{}
	for _, p := range partitions(g.name) {
		labels[p.Name] = p.Label
	}
	cands := append([]storage.Disk{}, g.parts...)
	if g.disk.FSType != "" {
		cands = append(cands, g.disk)
	}
	out := []SteamLibrary{}
	for _, c := range cands {
		if c.UUID == "" || !libraryFS[c.FSType] || strings.HasPrefix(labels[filepath.Base(c.Path)], "vos_") ||
			c.Label == "vos_data" || c.Label == "VOS_ESP" {
			continue
		}
		if libs, ok := s.libCache[c.UUID]; ok {
			out = append(out, libs...)
			continue
		}
		var dirs []string
		switch {
		case c.MountedAt != "":
			dirs = findSteamLibraries(c.MountedAt)
		case mountAllowed:
			var ok bool
			if dirs, ok = s.scanUnmounted(c); !ok {
				continue
			}
		default:
			continue
		}
		libs := []SteamLibrary{}
		for _, d := range dirs {
			libs = append(libs, SteamLibrary{UUID: c.UUID, Label: c.Label, Path: d})
		}
		s.libCache[c.UUID] = libs
		out = append(out, libs...)
	}
	return out
}

// scanUnmounted mounts a filesystem read-only in a private directory,
// looks for Steam libraries and unmounts it. Journal replay is disabled
// where the filesystem allows, so probing never writes to a disk.
func (s *Service) scanUnmounted(d storage.Disk) ([]string, bool) {
	dir := filepath.Join(config.RunDir, "probe", unsafeLabelRE.ReplaceAllString(d.UUID, "_"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	defer os.Remove(dir)
	dev := d.Path
	if !strings.HasPrefix(dev, "/dev/") {
		dev = devName(filepath.Base(dev))
	}
	fstype, opts := probeMountOptions(d.FSType)
	// Not the request's context: a mount killed half-way could stay
	// mounted without us knowing.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.env.run.Run(ctx, "mount", "-t", fstype, "-o", opts, dev, dir); err != nil {
		s.env.logf("install: probing %s: %v", dev, err)
		return nil, false
	}
	libs := findSteamLibraries(dir)
	if _, err := s.env.run.Run(context.Background(), "umount", dir); err != nil {
		s.env.run.Run(context.Background(), "umount", "-l", dir)
	}
	return libs, true
}

func probeMountOptions(fstype string) (string, string) {
	opts := "ro,nosuid,nodev,noexec"
	switch fstype {
	case "ext2", "ext3", "ext4":
		opts += ",noload"
	case "xfs":
		opts += ",norecovery"
	case "btrfs":
		opts += ",rescue=nologreplay"
	case "ntfs":
		fstype = "ntfs3"
	}
	return fstype, opts
}

// scanLimit bounds how many directories are looked at per level, so a
// data disk with a huge top level cannot stall the probe.
const scanLimit = 256

// findSteamLibraries returns the Steam library folders under root, as
// paths inside it: root itself and up to two levels down, which covers
// "/", "/SteamLibrary" and "/Program Files (x86)/Steam".
func findSteamLibraries(root string) []string {
	var out []string
	if isSteamLibrary(root) {
		return []string{"/"}
	}
	for _, d1 := range subdirs(root) {
		p1 := filepath.Join(root, d1)
		if isSteamLibrary(p1) {
			out = append(out, "/"+d1)
			continue
		}
		for _, d2 := range subdirs(p1) {
			if isSteamLibrary(filepath.Join(p1, d2)) {
				out = append(out, "/"+d1+"/"+d2)
			}
		}
	}
	sort.Strings(out)
	return out
}

// isSteamLibrary: Steam marks a library with libraryfolder.vdf; older or
// Windows libraries are recognised by app manifests in steamapps/.
func isSteamLibrary(dir string) bool {
	if exists(filepath.Join(dir, "libraryfolder.vdf")) {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(dir, "steamapps"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "appmanifest_") && strings.HasSuffix(e.Name(), ".acf") {
			return true
		}
	}
	return false
}

// subdirs lists the directories worth descending into.
func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "$") ||
			n == "lost+found" || n == "System Volume Information" || n == "steamapps" {
			continue
		}
		out = append(out, n)
		if len(out) == scanLimit {
			break
		}
	}
	return out
}

// guessTimezone reads the running system's /etc/localtime link; the live
// ISO has none, and the web UI then suggests the browser's zone.
func guessTimezone() string {
	t, err := os.Readlink(paths.Localtime)
	if err != nil {
		return "UTC"
	}
	_, tz, ok := strings.Cut(t, "zoneinfo/")
	if !ok {
		return "UTC"
	}
	for _, p := range []string{"posix/", "right/"} {
		tz = strings.TrimPrefix(tz, p)
	}
	if !validTimezoneName(tz) {
		return "UTC"
	}
	return tz
}
