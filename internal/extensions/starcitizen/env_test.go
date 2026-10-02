package starcitizen

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// UUIDs of the fake system's filesystems.
const (
	sysUUID   = "5f0c1e8a-3b2d-4c6e-9a71-0d2f4b6c8e10"
	gameUUID  = "a1b2c3d4-e5f6-4789-8abc-def012345678"
	otherUUID = "0f1e2d3c-4b5a-4697-8877-665544332211"
)

// box is a fake PC: a system drive (vos_data, mounted at root, holding
// vapor's home) and a game drive SATA1TB at <mnt>/SATA1TB, with a mount
// table, /dev/disk/by-uuid and free space for both.
type box struct {
	t       *testing.T
	root    string
	mnt     string // the game drive's mount point
	free    map[string]uint64
	mounted bool   // whether the game drive is in the mount table
	gameFS  string // the game drive's filesystem type
	gameDev string // the device mounted as the game drive, in dev/
	calls   [][]string
}

func newBox(t *testing.T) *box {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &box{t: t, root: root, mounted: true, gameFS: "ext4", gameDev: "sdb1", free: map[string]uint64{}}
	save := struct {
		mi, uuid, mnt, home, meminfo, vos string
		asGamer                           func(context.Context, string, ...string) (string, error)
		free                              func(string) (uint64, error)
		root                              bool
	}{mountInfoPath, byUUIDDir, mntBase, config.GamerHome, meminfoPath, vosBin, asGamer, freeBytes, runAsRootOK}
	t.Cleanup(func() {
		mountInfoPath, byUUIDDir, mntBase, config.GamerHome, meminfoPath, vosBin = save.mi, save.uuid, save.mnt, save.home, save.meminfo, save.vos
		asGamer, freeBytes, runAsRootOK = save.asGamer, save.free, save.root
	})
	mntBase = filepath.Join(root, "var/mnt")
	b.mnt = filepath.Join(mntBase, "SATA1TB")
	config.GamerHome = filepath.Join(root, "var/home/vapor")
	mountInfoPath = filepath.Join(root, "mountinfo")
	byUUIDDir = filepath.Join(root, "dev/disk/by-uuid")
	meminfoPath = filepath.Join(root, "meminfo")
	vosBin = "/usr/bin/vos"
	runAsRootOK = true
	for _, d := range []string{b.mnt, config.GamerHome, byUUIDDir} {
		mkdir(t, d)
	}
	writeFile(t, filepath.Join(root, "dev/nvme0n1p4"), "")
	writeFile(t, filepath.Join(root, "dev/sdb1"), "")
	must(t, os.Symlink("../../nvme0n1p4", filepath.Join(byUUIDDir, sysUUID)))
	must(t, os.Symlink("../../sdb1", filepath.Join(byUUIDDir, gameUUID)))
	b.memory(32, 32)
	b.free[root] = 400e9
	b.free[b.mnt] = 900e9
	freeBytes = func(p string) (uint64, error) {
		if n, ok := b.free[p]; ok {
			return n, nil
		}
		return 0, fmt.Errorf("no free space for %s", p)
	}
	asGamer = b.asGamer
	b.writeMounts()
	return b
}

// writeMounts writes the mount table: the image at /, vos_data at the
// box's root (where /var is) and, when mounted, the game drive.
func (b *box) writeMounts() {
	lines := []string{
		"22 1 259:2 / / ro,relatime shared:1 - erofs /dev/nvme0n1p2 ro,user_xattr",
		fmt.Sprintf("23 22 259:4 /var %s rw,relatime shared:2 - ext4 %s/dev/nvme0n1p4 rw", b.root, b.root),
	}
	if b.mounted {
		lines = append(lines, fmt.Sprintf("40 23 8:17 / %s rw,nosuid,nodev,noatime shared:20 - %s %s/dev/%s rw", b.mnt, b.gameFS, b.root, b.gameDev))
	}
	writeFile(b.t, mountInfoPath, strings.Join(lines, "\n")+"\n")
}

// swapDrive mounts another drive where SATA1TB was: the same name, another
// filesystem (otherUUID), with Star Citizen's files copied onto it.
func (b *box) swapDrive() {
	writeFile(b.t, filepath.Join(b.root, "dev/sdc1"), "")
	if _, err := os.Lstat(filepath.Join(byUUIDDir, otherUUID)); err != nil {
		must(b.t, os.Symlink("../../sdc1", filepath.Join(byUUIDDir, otherUUID)))
	}
	b.gameDev = "sdc1"
	b.writeMounts()
}

func (b *box) memory(ramGB, swapGB uint64) {
	writeFile(b.t, meminfoPath, fmt.Sprintf("MemTotal:       %d kB\nMemFree:         1234 kB\nSwapTotal:      %d kB\n",
		ramGB<<20-300<<10, swapGB<<20))
}

// asGamer runs vapor's side in this process: fetch-installer, rm and
// rmdir.
func (b *box) asGamer(ctx context.Context, name string, args ...string) (string, error) {
	b.calls = append(b.calls, append([]string{name}, args...))
	switch {
	case name == vosBin && len(args) >= 3 && args[0] == "ext" && args[1] == ID && args[2] == "fetch-installer":
		var out bytes.Buffer
		if code := fetchInstallerCmd(ctx, args[3:], &out, &out); code != 0 {
			return strings.TrimSpace(out.String()), fmt.Errorf("exit status %d", code)
		}
		return strings.TrimSpace(out.String()), nil
	case name == "rm":
		return "", os.RemoveAll(args[len(args)-1])
	case name == "rmdir":
		err := os.Remove(args[len(args)-1])
		if errors.Is(err, syscall.ENOTEMPTY) && slices.Contains(args, "--ignore-fail-on-non-empty") {
			err = nil
		}
		return "", err
	}
	return "", fmt.Errorf("unexpected command %s %v", name, args)
}

// feed serves latest.yml and the installer it names, with byte ranges.
type feed struct {
	srv       *httptest.Server
	installer []byte
	file      string
	served    []byte // what the installer URL serves; installer unless a test changes it
	ranges    []string
	noRange   bool
}

func newFeed(t *testing.T, version string) *feed {
	t.Helper()
	f := &feed{file: "RSI Launcher-Setup-" + version + ".exe", installer: bytes.Repeat([]byte("MZ installer "+version+"\n"), 50000)}
	f.served = f.installer
	sum := sha512.Sum512(f.installer)
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	yml := fmt.Sprintf("version: %s\nfiles:\n  - url: %s\n    sha512: %s\n    size: %d\n    blockMapSize: 361234\npath: %s\nsha512: %s\nreleaseDate: '2025-09-30T17:04:52.513Z'\n",
		version, f.file, b64, len(f.installer), f.file, b64)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/rel/2/latest.yml":
			fmt.Fprint(w, yml)
		case "/rel/2/" + strings.ReplaceAll(f.file, " ", "%20"):
			f.ranges = append(f.ranges, r.Header.Get("Range"))
			if f.noRange {
				r.Header.Del("Range")
			}
			http.ServeContent(w, r, f.file, time.Time{}, bytes.NewReader(f.served))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	saveURL, saveClient := feedURL, httpClient
	t.Cleanup(func() { feedURL, httpClient = saveURL, saveClient })
	feedURL = f.srv.URL + "/rel/2/latest.yml"
	httpClient = f.srv.Client()
	return f
}

// shipped is the repository's descriptor, as the image ships it.
func shipped(t *testing.T) *descriptor.Descriptor {
	t.Helper()
	d, err := descriptor.Load("../../../extensions/star-citizen/extension.json")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (b *box) ext(disk string) *extensions.Ext {
	return &extensions.Ext{ID: ID, Desc: shipped(b.t), Settings: map[string]any{"disk": disk},
		DataDir: filepath.Join(b.root, "var/lib/vos/ext/data", ID),
		HomeDir: filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, ID)}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, d string) {
	t.Helper()
	must(t, os.MkdirAll(d, 0o755))
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	must(t, os.WriteFile(p, []byte(s), 0o644))
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	must(t, err)
	return string(b)
}
