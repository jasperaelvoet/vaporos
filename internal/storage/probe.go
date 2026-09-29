package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// probeOptions are the read-only mount options used to look inside an
// unmounted filesystem. They go further than "ro": ext4 and xfs would
// otherwise replay their journal (a write), and btrfs its log tree.
var probeOptions = map[string]string{
	"ext4":  "ro,noload",
	"ext3":  "ro,noload",
	"ext2":  "ro", // no journal to skip
	"xfs":   "ro,norecovery",
	"btrfs": "ro,rescue=nologreplay",
	"f2fs":  "ro,norecovery",
	"ntfs":  "ro",
	"ntfs3": "ro",
}

// libraryFS are the kernel filesystems (mountType names) a Steam library
// can live on. Proton and native games need Unix permissions and symlinks,
// which rules out FAT and exFAT. NTFS works through the in-kernel ntfs3
// driver, mounted with uid/gid so everything on it is the gaming user's.
var libraryFS = map[string]bool{
	"ext4": true, "ext3": true, "ext2": true, "btrfs": true, "xfs": true, "f2fs": true, "ntfs3": true,
}

// LibraryFS reports whether a filesystem, as lsblk or config.json names it,
// can hold a game library. It is the one rule for the Storage page, the
// installer and the generator, so nothing is adopted that is not mounted.
func LibraryFS(fstype string) bool { return libraryFS[mountType(fstype)] }

// mountType maps lsblk's filesystem name to the kernel driver to mount it
// with. lsblk says "ntfs", but the "ntfs" name in current kernels is a
// read-only compatibility alias; ntfs3 is the read-write driver.
func mountType(fstype string) string {
	if fstype == "ntfs" {
		return "ntfs3"
	}
	return fstype
}

var uuidRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]{0,63}$`)

// validUUID accepts filesystem UUIDs as blkid reports them: RFC 4122 for
// ext4/btrfs/xfs, 16 hex digits for NTFS, XXXX-XXXX for FAT.
func validUUID(u string) bool { return uuidRe.MatchString(u) }

// Seams for tests: probing mounts filesystems, which needs root and Linux.
var (
	runCmd       = sysd.Run
	probeLibrary = probeUnmounted
	probeTTL     = 2 * time.Minute
)

type probeResult struct {
	libDir string
	free   int64
	ok     bool
	at     time.Time
}

// probeCache remembers results per filesystem so that opening the Storage
// page twice does not mount every disk twice. probeMu also serializes the
// probes themselves: two at once would mount on the same directory.
var (
	probeMu    sync.Mutex
	probeCache = map[string]probeResult{}
)

// detectLibrary fills SteamLibrary, LibraryDir and (for unmounted disks)
// Free. System filesystems are never looked into: reading /efi alone would
// trigger its automount.
func detectLibrary(ctx context.Context, d *Disk) {
	if d.IsSystem || d.FSType == "" || d.FSType == "swap" {
		return
	}
	if d.MountedAt != "" {
		d.LibraryDir, d.SteamLibrary = steam.LibraryIn(d.MountedAt)
		return
	}
	if _, ok := probeOptions[d.FSType]; !ok || !validUUID(d.UUID) {
		return
	}
	probeMu.Lock()
	defer probeMu.Unlock()
	key := d.UUID + "\x00" + d.Path
	r, ok := probeCache[key]
	if !ok || time.Since(r.at) > probeTTL {
		libDir, free, err := probeLibrary(ctx, *d)
		if err != nil {
			log.Printf("storage: probe %s (%s): %v", d.Path, d.FSType, err)
		}
		r = probeResult{libDir: libDir, free: free, ok: err == nil && libDir != "", at: time.Now()}
		probeCache[key] = r
	}
	d.SteamLibrary, d.LibraryDir = r.ok, r.libDir
	if d.Free == 0 {
		d.Free = r.free
	}
}

// probeUnmounted mounts d read-only under /run/vos/probe/<uuid>, looks for
// a Steam library and the free space, and unmounts it again.
func probeUnmounted(ctx context.Context, d Disk) (libDir string, free int64, err error) {
	dir := filepath.Join(config.RunDir, "probe", d.UUID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	defer os.Remove(dir)
	// A probe interrupted by a crash can leave its mount behind; never
	// stack a second one on top of it.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		unmountProbe(ctx, dir)
	}

	opts := probeOptions[d.FSType] + ",nosuid,nodev,noexec"
	mctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := runCmd(mctx, "mount", "-t", mountType(d.FSType), "-o", opts, d.Path, dir); err != nil {
		if d.FSType != "btrfs" {
			return "", 0, err
		}
		// rescue=nologreplay needs a 5.9+ kernel; plain ro is still ro.
		if _, err := runCmd(mctx, "mount", "-t", "btrfs", "-o", "ro,nosuid,nodev,noexec", d.Path, dir); err != nil {
			return "", 0, err
		}
	}
	defer unmountProbe(ctx, dir)

	libDir, _ = steam.LibraryIn(dir)
	return libDir, freeBytes(dir), nil
}

func unmountProbe(ctx context.Context, dir string) {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if _, err := runCmd(uctx, "umount", dir); err != nil {
		// Something (a file manager, a stuck process) holds it: detach it
		// now and let the kernel finish when the last user goes away.
		if _, err2 := runCmd(uctx, "umount", "-l", dir); err2 != nil {
			log.Printf("storage: cannot unmount probe %s: %v", dir, fmt.Errorf("%w; lazy: %v", err, err2))
		}
	}
}

// freeBytes is the space available to unprivileged users on the
// filesystem holding dir, 0 when unknown.
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
