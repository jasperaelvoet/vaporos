package starcitizen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestParseMountInfo(t *testing.T) {
	ms := parseMountInfo([]byte(`22 1 259:2 / / ro,relatime shared:1 - erofs /dev/nvme0n1p2 ro,user_xattr
25 22 259:4 /var /var rw,relatime shared:2 - ext4 /dev/nvme0n1p4 rw
41 25 0:44 / /var/mnt/Games\040Drive rw,noatime shared:20 master:3 - btrfs /dev/sdc1 rw,space_cache=v2,subvolid=5,subvol=/
not a mount line
42 25 8:17 / /var/mnt/SATA1TB rw shared:21 -
`))
	want := []mount{
		{"/", "erofs", "/dev/nvme0n1p2"},
		{"/var", "ext4", "/dev/nvme0n1p4"},
		{"/var/mnt/Games Drive", "btrfs", "/dev/sdc1"},
	}
	if len(ms) != len(want) {
		t.Fatalf("got %+v", ms)
	}
	for i := range want {
		if ms[i] != want[i] {
			t.Errorf("line %d: %+v, want %+v", i, ms[i], want[i])
		}
	}
	if s := unescapeMount(`a\040b\134c\04`); s != `a b\c\04` {
		t.Errorf("unescape: %q", s)
	}
}

func TestMountOf(t *testing.T) {
	ms := []mount{
		{"/", "erofs", "/dev/a"},
		{"/var", "ext4", "/dev/b"},
		{"/var/mnt/SATA1TB", "ext4", "/dev/old"},
		{"/var/mnt/SATA1TB", "xfs", "/dev/c"}, // mounted over the first
		{"/var/mnt/SATA", "ext4", "/dev/d"},
	}
	for p, want := range map[string]string{
		"/usr/bin":                              "/dev/a",
		"/var/home/vapor/x":                     "/dev/b",
		"/var/mnt/SATA1TB":                      "/dev/c",
		"/var/mnt/SATA1TB/VaporOS/star-citizen": "/dev/c",
		"/var/mnt/SATA1TBX/VaporOS":             "/dev/b",
		"/var/mnt/SATA/x":                       "/dev/d",
	} {
		if m, ok := mountOf(ms, p); !ok || m.Source != want {
			t.Errorf("%s: %+v, want %s", p, m, want)
		}
	}
}

func TestPlaceFor(t *testing.T) {
	b := newBox(t)
	sys := filepath.Join(config.GamerHome, ".local/share/vaporos/ext/star-citizen")
	// /var is the system drive; /state is how earlier control centers named it.
	for _, disk := range []string{"/var", "/state"} {
		p, err := placeFor(disk)
		if err != nil || !p.System || p.Prefix() != sys || p.Disk != "/var" || p.Name != "the system drive" {
			t.Errorf("%q: %+v, %v", disk, p, err)
		}
	}
	// No drive picked yet is not the system drive.
	if p, err := placeFor(""); !errors.Is(err, errNoDrive) {
		t.Errorf("no drive: %+v, %v", p, err)
	}
	p, err := placeFor(b.mnt)
	if err != nil || p.System || p.Prefix() != b.mnt+"/VaporOS/star-citizen" || p.Name != "SATA1TB" || p.Disk != b.mnt {
		t.Errorf("game drive: %+v, %v", p, err)
	}
	for _, disk := range []string{b.mnt + "/SteamLibrary", "/efi", mntBase, mntBase + "/..", mntBase + "/Games Drive", "/var/lib/vos", "/var/", "/"} {
		if p, err := placeFor(disk); !errors.Is(err, errUnknownDrive) {
			t.Errorf("%q: %+v, %v", disk, p, err)
		}
	}
	for prefix, disk := range map[string]string{sys: "/var", b.mnt + "/VaporOS/star-citizen": b.mnt} {
		if p, ok := placeOf(prefix); !ok || p.Disk != disk || p.Prefix() != prefix {
			t.Errorf("%s: %+v, %v", prefix, p, ok)
		}
	}
	for _, p := range []string{b.mnt + "/VaporOS", b.mnt + "/VaporOS/star-citizen/pfx", b.mnt + "/x/../VaporOS/star-citizen", mntBase + "/a b/VaporOS/star-citizen", mntBase + "/../VaporOS/star-citizen", "/tmp/star-citizen", ""} {
		if pl, ok := placeOf(p); ok {
			t.Errorf("%q is known: %+v", p, pl)
		}
	}
	// /mnt is a symlink to /var/mnt on VaporOS; the setting may name either.
	if normalizeDisk("/mnt/SATA1TB") != mntBase+"/SATA1TB" {
		t.Errorf("/mnt/SATA1TB → %q", normalizeDisk("/mnt/SATA1TB"))
	}
}

func TestProblem(t *testing.T) {
	b := newBox(t)
	game, _ := placeFor(b.mnt)
	sys, _ := placeFor("/var")
	for _, tc := range []struct {
		p    place
		err  error
		want string
	}{
		{game, errNotConnected, "Star Citizen's drive, SATA1TB, isn't connected. Connect it, then try again."},
		{game, errMissing, "Star Citizen's files on SATA1TB are missing. Remove Star Citizen and add it again."},
		{sys, errNotConnected, "Star Citizen's files on the system drive are missing. Remove Star Citizen and add it again."},
		{sys, errMissing, "Star Citizen's files on the system drive are missing. Remove Star Citizen and add it again."},
	} {
		if got := tc.p.problem(tc.err); got != tc.want {
			t.Errorf("%s, %v: %q", tc.p.Name, tc.err, got)
		}
	}
}

func TestDriveMount(t *testing.T) {
	b := newBox(t)
	game, _ := placeFor(b.mnt)
	sys, _ := placeFor("/var")
	ms, err := readMounts()
	must(t, err)
	if m, err := driveMount(ms, game); err != nil || m.Point != b.mnt || m.FSType != "ext4" {
		t.Errorf("game drive: %+v, %v", m, err)
	}
	if m, err := driveMount(ms, sys); err != nil || m.Point != b.root {
		t.Errorf("system drive: %+v, %v", m, err)
	}
	if uuid, err := fsUUID(filepath.Join(b.root, "dev/sdb1")); err != nil || uuid != gameUUID {
		t.Errorf("game drive's UUID: %q, %v", uuid, err)
	}
	if _, err := fsUUID(filepath.Join(b.root, "dev/sdz9")); err == nil {
		t.Error("a device without a UUID has one")
	}

	// Unplugged: its folder is an empty directory on the system drive.
	b.mounted = false
	b.writeMounts()
	ms, err = readMounts()
	must(t, err)
	if m, err := driveMount(ms, game); !errors.Is(err, errNotConnected) {
		t.Errorf("an unplugged drive: %+v, %v", m, err)
	}

	// Another filesystem mounted between the drive and the prefix.
	ms = append(ms, mount{Point: b.mnt, FSType: "ext4", Source: "/dev/sdb1"}, mount{Point: b.mnt + "/VaporOS", FSType: "tmpfs", Source: "tmpfs"})
	if m, err := driveMount(ms, game); !errors.Is(err, errNotConnected) {
		t.Errorf("a mount on its VaporOS folder: %+v, %v", m, err)
	}
}

func TestOnDrive(t *testing.T) {
	b := newBox(t)
	game, _ := placeFor(b.mnt)
	sys, _ := placeFor("/var")
	ms, err := readMounts()
	must(t, err)
	if _, err := onDrive(ms, game, gameUUID); err != nil {
		t.Errorf("its drive: %v", err)
	}
	if _, err := onDrive(ms, game, strings.ToUpper(gameUUID)); err != nil {
		t.Errorf("its drive, in capitals: %v", err)
	}
	// Another drive with the same name is not the one Install recorded.
	for _, uuid := range []string{sysUUID, ""} {
		if _, err := onDrive(ms, game, uuid); !errors.Is(err, errNotConnected) {
			t.Errorf("game drive, %q: %v", uuid, err)
		}
	}
	// The system drive is always there; another filesystem on it lost the files.
	if _, err := onDrive(ms, sys, sysUUID); err != nil {
		t.Errorf("system drive: %v", err)
	}
	if _, err := onDrive(ms, sys, gameUUID); !errors.Is(err, errMissing) {
		t.Errorf("system drive, another filesystem: %v", err)
	}
	// reach also wants the marker Install wrote there.
	if _, err := reach(ms, game, gameUUID); !errors.Is(err, errMissing) {
		t.Errorf("no marker: %v", err)
	}
	writeFile(t, b.mnt+"/VaporOS/star-citizen/"+markerName, gameUUID+"\n")
	if m, err := reach(ms, game, gameUUID); err != nil || m.Point != b.mnt {
		t.Errorf("with its marker: %+v, %v", m, err)
	}
}

func TestCheckPrefix(t *testing.T) {
	b := newBox(t)
	game, _ := placeFor(b.mnt)
	prefix := game.Prefix()
	if err := checkPrefix(game); !errors.Is(err, errMissing) {
		t.Errorf("a missing prefix: %v", err)
	}
	writeFile(t, filepath.Join(prefix, markerName), gameUUID+"\n")
	if err := checkPrefix(game); err != nil {
		t.Errorf("the prefix Install made: %v", err)
	}

	writeFile(t, filepath.Join(prefix, markerName), sysUUID+"\n")
	if err := checkPrefix(game); !errors.Is(err, errNotConnected) {
		t.Errorf("a prefix copied from another drive: %v", err)
	}
	writeFile(t, filepath.Join(prefix, markerName), gameUUID+"\n")

	// The drive is gone and something made the folder on the system drive.
	b.mounted = false
	b.writeMounts()
	if err := checkPrefix(game); !errors.Is(err, errNotConnected) {
		t.Errorf("a prefix on the system drive passed: %v", err)
	}
	b.mounted = true
	b.writeMounts()

	// A symlink on the way is not the prefix (and not what the LUG allows).
	link := mntBase + "/Link"
	must(t, os.Symlink(b.mnt, link))
	if p, ok := placeOf(link + "/VaporOS/star-citizen"); !ok || checkPrefix(p) == nil {
		t.Error("a prefix through a symlink passed")
	}
	must(t, os.Rename(b.mnt+"/VaporOS", b.mnt+"/Elsewhere"))
	must(t, os.Symlink("Elsewhere", b.mnt+"/VaporOS"))
	if err := checkPrefix(game); !errors.Is(err, errMissing) {
		t.Errorf("a symlink in the prefix: %v", err)
	}

	// On the system drive, whatever is wrong, the files are missing.
	sys, _ := placeFor("/var")
	if err := checkPrefix(sys); !errors.Is(err, errMissing) {
		t.Errorf("system drive without files: %v", err)
	}
	writeFile(t, filepath.Join(sys.Prefix(), markerName), gameUUID+"\n")
	if err := checkPrefix(sys); !errors.Is(err, errMissing) {
		t.Errorf("system drive with another drive's marker: %v", err)
	}
	writeFile(t, filepath.Join(sys.Prefix(), markerName), sysUUID+"\n")
	if err := checkPrefix(sys); err != nil {
		t.Errorf("system drive: %v", err)
	}
}
