package power

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// These probes are the reference machine's gaming-idle-shutdown checks,
// one function each, reading /proc, sysfs and the Steam libraries directly
// instead of shelling out to tr/grep/find/awk every 15 seconds.

// gameRunning reports whether any process of uid carries SteamAppId=<n>,
// n > 0, in its environment. Steam sets it for every process of a running
// game, including those inside the Steam Linux Runtime container; the
// Steam client itself runs with SteamAppId unset or 0.
func gameRunning(procDir string, uid int) bool {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !isPID(e.Name()) {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		// /proc/<pid> is owned by the process's effective uid.
		fi, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		env, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil {
			continue // exited, or not ours to read
		}
		if hasSteamAppID(env) {
			return true
		}
	}
	return false
}

func isPID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// hasSteamAppID matches the reference's grep -E '^SteamAppId=[1-9][0-9]*$'
// against a NUL-separated environ block.
func hasSteamAppID(environ []byte) bool {
	for _, kv := range bytes.Split(environ, []byte{0}) {
		v, ok := bytes.CutPrefix(kv, []byte("SteamAppId="))
		if !ok || len(v) == 0 || v[0] < '1' || v[0] > '9' {
			continue
		}
		digits := true
		for _, c := range v {
			if c < '0' || c > '9' {
				digits = false
				break
			}
		}
		if digits {
			return true
		}
	}
	return false
}

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
