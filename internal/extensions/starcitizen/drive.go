package starcitizen

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// Where the system finds drives; variables for tests.
var (
	mountInfoPath = "/proc/self/mountinfo"
	byUUIDDir     = "/dev/disk/by-uuid"
	mntBase       = "/var/mnt" // adopted game drives, /var/mnt/<name>
)

const (
	maxMountInfo = 4 << 20
	maxMarker    = 256
)

// systemDisk is the disk setting's value for the system drive.
const systemDisk = "/var"

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	uuidRe = regexp.MustCompile(`^[0-9A-Fa-f][0-9A-Fa-f-]{3,63}$`)
	safeRe = regexp.MustCompile(`^/[A-Za-z0-9/._-]+$`)
)

// place is where Star Citizen's prefix goes on one drive: Rel beneath
// Base, a directory root may write through gamerfs (a game drive's mount
// point, or vapor's home on the system drive).
type place struct {
	Disk   string // the disk setting it came from, normalized
	Base   string
	Rel    string
	Name   string // the drive in the person's words
	System bool
}

func (p place) Prefix() string { return filepath.Join(p.Base, p.Rel) }

// systemPrefix is the prefix on the system drive: the home data area.
func systemPrefix() string { return filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, ID) }

// normalizeDisk maps the other spellings of a drive to one: /state (the
// system drive's folder, as earlier control centers offered it) to /var,
// /mnt/<name> to /var/mnt/<name>.
func normalizeDisk(disk string) string {
	switch {
	case disk == "/state":
		return systemDisk
	case strings.HasPrefix(disk, "/mnt/"):
		return mntBase + strings.TrimPrefix(disk, "/mnt")
	}
	return disk
}

// placeFor reads the disk setting: "" is no drive picked yet, /var the
// system drive, /var/mnt/<name> a game drive VaporOS mounts.
func placeFor(disk string) (place, error) {
	switch disk = normalizeDisk(disk); disk {
	case "":
		return place{}, refuse(codeNoDrive, "no drive picked")
	case systemDisk:
		return place{Disk: systemDisk, Base: config.GamerHome, Rel: path.Join(config.ExtGamerDataSubdir, ID), Name: "the system drive", System: true}, nil
	}
	name, ok := strings.CutPrefix(disk, mntBase+"/")
	if !ok || !nameRe.MatchString(name) || name == "." || name == ".." {
		return place{}, refuse(codeUnknownDrive, "the disk setting %q names no drive", disk)
	}
	return place{Disk: disk, Base: disk, Rel: "VaporOS/" + ID, Name: name}, nil
}

// placeOf is the place whose prefix is prefix, false for any other
// directory: the hook and the vapor-side command work in no other.
func placeOf(prefix string) (place, bool) {
	if !safeRe.MatchString(prefix) {
		return place{}, false
	}
	if prefix == systemPrefix() {
		p, err := placeFor(systemDisk)
		return p, err == nil
	}
	rest, ok := strings.CutPrefix(prefix, mntBase+"/")
	if !ok {
		return place{}, false
	}
	name, tail, _ := strings.Cut(rest, "/")
	if tail != "VaporOS/"+ID {
		return place{}, false
	}
	p, err := placeFor(mntBase + "/" + name)
	return p, err == nil && p.Prefix() == prefix
}

// mount is one line of the mount table.
type mount struct {
	Point  string
	FSType string
	Source string
}

func readMounts() ([]mount, error) {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMountInfo))
	if err != nil {
		return nil, err
	}
	return parseMountInfo(b), nil
}

// parseMountInfo reads /proc/self/mountinfo:
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
//
// Lines it cannot read are skipped.
func parseMountInfo(b []byte) []mount {
	var out []mount
	for line := range bytes.Lines(b) {
		f := strings.Fields(string(line))
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(f) {
			continue
		}
		out = append(out, mount{Point: unescapeMount(f[4]), FSType: f[sep+1], Source: unescapeMount(f[sep+2])})
	}
	return out
}

// unescapeMount undoes the kernel's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mountOf returns the mount that holds p: the longest mount point above
// it, the last one mounted there.
func mountOf(ms []mount, p string) (mount, bool) {
	var best mount
	found := false
	for _, m := range ms {
		if !within(p, m.Point) {
			continue
		}
		if !found || len(m.Point) >= len(best.Point) {
			best, found = m, true
		}
	}
	return best, found
}

func within(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}

// locate is the one judgement of whether Star Citizen's files at p can
// be used, for the card's status, fetch-installer and the launch hook
// alike: the mount of p's drive, or a refusal with codeNotConnected or
// codeFilesMissing. uuid is the filesystem Install recorded (state.json):
// the drive must be mounted at its folder and hold that filesystem
// (onDrive) before its marker, read through gamerfs, is looked at, so
// another drive with the same name is one that isn't connected, never one
// whose files are gone. Install, which writes the marker, asks only for
// the drive (onDrive, or driveMount for a prefix it has not recorded).
func locate(ms []mount, p place, uuid string) (mount, error) {
	m, err := onDrive(ms, p, uuid)
	if err != nil {
		return mount{}, err
	}
	b, err := gamerfs.ReadFile(p.Base, path.Join(p.Rel, markerName), maxMarker)
	if err != nil {
		return mount{}, extensions.Refuse(codeFilesMissing, err)
	}
	if got := strings.TrimSpace(string(b)); !strings.EqualFold(got, uuid) {
		return mount{}, refuse(codeFilesMissing, "the marker in %s holds %q, not %s", p.Prefix(), got, uuid)
	}
	return m, nil
}

// driveMount is the mount Star Citizen's prefix is on. A game drive must
// be mounted exactly at its folder, with nothing mounted between it and
// the prefix: an empty mount point holds the system drive, where 100 GB
// must never land by accident.
func driveMount(ms []mount, p place) (mount, error) {
	m, ok := mountOf(ms, p.Prefix())
	switch {
	case !ok:
		return mount{}, notThere(p, "nothing is mounted above %s", p.Prefix())
	case !p.System && m.Point != p.Base:
		return mount{}, notThere(p, "%s is on %s, not on a drive mounted at %s", p.Prefix(), m.Point, p.Base)
	}
	return m, nil
}

// onDrive is the mount of p's drive when it holds the filesystem uuid
// names.
func onDrive(ms []mount, p place, uuid string) (mount, error) {
	m, err := driveMount(ms, p)
	if err != nil {
		return mount{}, err
	}
	got, err := fsUUID(m.Source)
	if err != nil {
		return mount{}, notThere(p, "%v", err)
	}
	if uuid == "" || !strings.EqualFold(got, uuid) {
		return mount{}, notThere(p, "%s holds filesystem %s, not %q", m.Point, got, uuid)
	}
	return m, nil
}

// notThere is the refusal for p's drive missing or holding another
// filesystem: the game drive isn't connected, or the system drive lost
// the files.
func notThere(p place, format string, args ...any) error {
	if p.System {
		return refuse(codeFilesMissing, format, args...)
	}
	return refuse(codeNotConnected, format, args...)
}

// fsUUID is the UUID of the filesystem on source: the name of the
// /dev/disk/by-uuid link that leads to the same device.
func fsUUID(source string) (string, error) {
	dev := resolve(source)
	ents, err := os.ReadDir(byUUIDDir)
	if err != nil {
		return "", err
	}
	for _, e := range ents {
		if uuidRe.MatchString(e.Name()) && resolve(filepath.Join(byUUIDDir, e.Name())) == dev {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("no filesystem UUID for %s", source)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
