package starcitizen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
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

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	uuidRe = regexp.MustCompile(`^[0-9A-Fa-f][0-9A-Fa-f-]{3,63}$`)
	safeRe = regexp.MustCompile(`^/[A-Za-z0-9/._-]+$`)
)

// errNotConnected is why a drive cannot be used: it is not mounted where
// VaporOS mounts it, or another filesystem is.
var errNotConnected = errors.New("not connected")

// place is where Star Citizen's prefix goes on one drive: Rel beneath
// Base, a directory root may write through gamerfs (a game drive's mount
// point, or vapor's home on the system drive).
type place struct {
	Disk   string // the disk setting it came from, normalized ("" for the system drive)
	Base   string
	Rel    string
	Name   string // the drive in the person's words
	System bool
}

func (p place) Prefix() string { return filepath.Join(p.Base, p.Rel) }

// systemPrefix is the prefix on the system drive: the home data area.
func systemPrefix() string { return filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, ID) }

// normalizeDisk maps the other spellings of a drive to one: /state (where
// the system drive is mounted, which the control center offers as its
// folder) and /var to "", /mnt/<name> to /var/mnt/<name>.
func normalizeDisk(disk string) string {
	switch {
	case disk == "/state", disk == "/var":
		return ""
	case strings.HasPrefix(disk, "/mnt/"):
		return mntBase + strings.TrimPrefix(disk, "/mnt")
	}
	return disk
}

// placeFor reads the disk setting: "" (or /var) is the system drive,
// /var/mnt/<name> a game drive VaporOS mounts.
func placeFor(disk string) (place, error) {
	disk = normalizeDisk(disk)
	if disk == "" {
		return place{Base: config.GamerHome, Rel: path.Join(config.ExtGamerDataSubdir, ID), Name: "the system drive", System: true}, nil
	}
	name, ok := strings.CutPrefix(disk, mntBase+"/")
	if !ok || !nameRe.MatchString(name) || name == "." || name == ".." {
		return place{}, errors.New("VaporOS doesn't know the drive picked for it. Pick a game drive, or none for the system drive.")
	}
	return place{Disk: disk, Base: disk, Rel: "VaporOS/" + ID, Name: name}, nil
}

// knownPrefix reports whether prefix is one placeFor gives: the hook and
// the vapor-side command work in no other directory.
func knownPrefix(prefix string) bool {
	if prefix == systemPrefix() {
		return safeRe.MatchString(prefix)
	}
	rest, ok := strings.CutPrefix(prefix, mntBase+"/")
	if !ok {
		return false
	}
	name, tail, _ := strings.Cut(rest, "/")
	return tail == "VaporOS/"+ID && nameRe.MatchString(name) && name != "." && name != ".." &&
		safeRe.MatchString(prefix)
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

// driveMount is the mount Star Citizen's place is on. A game drive must be
// mounted exactly at its folder: an empty mount point holds the system
// drive, where 100 GB must never land by accident.
func driveMount(ms []mount, p place) (mount, error) {
	if p.System {
		if m, ok := mountOf(ms, p.Prefix()); ok {
			return m, nil
		}
		return mount{}, errNotConnected
	}
	if m, ok := mountOf(ms, p.Base); ok && m.Point == p.Base {
		return m, nil
	}
	return mount{}, errNotConnected
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

// driveUUID is the UUID of the filesystem the mount table puts p on.
func driveUUID(ms []mount, p string) (string, error) {
	m, ok := mountOf(ms, p)
	if !ok {
		return "", errNotConnected
	}
	return fsUUID(m.Source)
}

// checkPrefix makes sure prefix is where Install put it: the real
// directory, without a symlink on the way, on the filesystem whose UUID its
// marker holds. It runs as vapor, in vapor's own tree.
func checkPrefix(prefix string) error {
	if r, err := filepath.EvalSymlinks(prefix); err != nil || r != prefix {
		return errNotConnected
	}
	f, err := os.Open(filepath.Join(prefix, markerName))
	if err != nil {
		return errNotConnected
	}
	b, err := io.ReadAll(io.LimitReader(f, maxMarker))
	f.Close()
	if err != nil {
		return errNotConnected
	}
	want := strings.TrimSpace(string(b))
	ms, err := readMounts()
	if err != nil {
		return err
	}
	got, err := driveUUID(ms, prefix)
	if err != nil || !strings.EqualFold(got, want) || want == "" {
		return errNotConnected
	}
	return nil
}
