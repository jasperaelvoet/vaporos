package power

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// These probes are the reference machine's gaming-idle-shutdown checks,
// one function each, reading /proc, sysfs and the Steam libraries directly
// instead of shelling out to tr/grep/find/awk every 15 seconds. Whether a
// game runs is internal/gameproc's, which the display policy shares.

// maxWalkEntries bounds one download check. A download directory has one
// directory per app and its chunks; this is far beyond a real one, and
// keeps a pathological tree from stalling the 15 s loop.
const maxWalkEntries = 50000

// steamDownloading reports whether any file under steamapps/downloading or
// steamapps/temp of any library changed after cutoff: Steam writes there
// continuously while it downloads or installs an update.
func steamDownloading(libs []string, cutoff time.Time) bool {
	budget := maxWalkEntries
	for _, lib := range libs {
		for _, sub := range []string{"downloading", "temp"} {
			if changedSince(filepath.Join(lib, "steamapps", sub), cutoff, &budget) {
				return true
			}
			if budget <= 0 {
				return false
			}
		}
	}
	return false
}

func changedSince(dir string, cutoff time.Time, budget *int) bool {
	found := false
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, as find does
		}
		if *budget--; *budget <= 0 {
			return fs.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// ioBytes sums rbytes and wbytes over every device in a cgroup v2 io.stat.
// The cgroup exists only while the unit runs; ok is false without it.
func ioBytes(path string) (uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var sum uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, field := range strings.Fields(sc.Text()) {
			k, v, ok := strings.Cut(field, "=")
			if !ok || (k != "rbytes" && k != "wbytes") {
				continue
			}
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				sum += n
			}
		}
	}
	return sum, sc.Err() == nil
}

// gamescopeIOStat is the io.stat of the gaming user's gamescope session,
// which runs Steam and every game it starts.
func gamescopeIOStat(cgroupRoot string, uid int) string {
	return filepath.Join(cgroupRoot, "user.slice",
		fmt.Sprintf("user-%d.slice", uid), fmt.Sprintf("user@%d.service", uid),
		"app.slice", "vos-gamescope.service", "io.stat")
}

// fileExists reports whether path exists (any type).
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}
