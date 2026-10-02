// Package gameproc finds what the gaming user runs in Steam: whether a
// game is running, which idle shutdown (internal/power) and the display
// policy (internal/display) both ask, and the Steam client that takes
// commands (docs/CONTRACTS.md "Units", Display policy). It reads /proc as
// root, where every process of the gaming user is untrusted: reads are
// bounded, never follow a symlink at their end and never block on a FIFO.
package gameproc

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// HandoffUnit is the transient user unit an extension hands a launch
// through (`systemd-run --user --unit=vos-ext-handoff`): it waits for one
// game to exit and then starts another, so a game is on its way while it
// is loaded.
const HandoffUnit = "vos-ext-handoff.service"

// Bounds on what is read of one process.
const (
	maxCmdline = 64 << 10
	maxEnviron = 8 << 20
	maxEnvVar  = 4096 // longer variables are skipped: none of ours is that long
)

// Probe looks at the processes of one user.
type Probe struct {
	ProcDir    string // /proc
	UID        int    // the gaming user
	RuntimeDir string // its /run/user/<uid>, where the user manager keeps transient units
}

// Default is the gaming user on the running system.
func Default() Probe {
	return Probe{ProcDir: "/proc", UID: config.GamerUID, RuntimeDir: config.GamerRuntimeDir}
}

// GameRunning reports whether a Steam game runs as the user: a process
// that is Steam's reaper for an app (ReaperApp), one whose environment has
// SteamAppId above 0 (Steam sets it for every process of a game, inside
// the runtime container too; the client runs without it or with 0), or
// an active HandoffUnit.
func (p Probe) GameRunning() bool {
	if p.handoffActive() {
		return true
	}
	for _, pid := range p.pids() {
		dir := p.dir(pid)
		if procUID(dir) != p.UID {
			continue
		}
		if _, ok := ReaperApp(readCmdline(dir)); ok {
			return true
		}
		if environHas(filepath.Join(dir, "environ"), steamAppIDSet) {
			return true
		}
	}
	return false
}

// handoffActive reports whether the user manager has the handoff unit
// loaded: a transient unit's file lives in its runtime directory until
// the unit is collected. The user owns that directory, so the file is
// opened through gamerfs: a symlink anywhere on the way counts as no unit.
func (p Probe) handoffActive() bool {
	if p.RuntimeDir == "" {
		return false
	}
	f, err := gamerfs.Open(p.RuntimeDir, "systemd/transient/"+HandoffUnit)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// ReaperApp reads Steam's reaper command line, the process Steam starts
// every game (and shortcut) under:
//
//	/…/ubuntu12_32/reaper SteamLaunch AppId=227300 -- /…/_v2-entry-point …
//
// argv[0]'s base name is exactly "reaper" (gamescope's own
// "gamescopereaper" is not one), argv[1] "SteamLaunch", and an AppId=<n>
// before "--" names the app: a non-zero decimal that may be larger than 32
// bits for a shortcut.
func ReaperApp(argv []string) (uint64, bool) {
	if len(argv) < 3 || filepath.Base(argv[0]) != "reaper" || argv[1] != "SteamLaunch" {
		return 0, false
	}
	for _, a := range argv[2:] {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "AppId="); ok {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil || n == 0 {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

// SteamClient returns the pid of the user's process that holds pipe
// (~/.steam/steam.pipe) open: the Steam client that takes commands, as the
// steam launcher itself checks before it hands one on. 0 when none does.
func (p Probe) SteamClient(pipe string) int {
	for _, pid := range p.pids() {
		dir := p.dir(pid)
		if procUID(dir) != p.UID {
			continue
		}
		if holds(filepath.Join(dir, "fd"), pipe) {
			return pid
		}
	}
	return 0
}

// Environ returns the variables keys of process pid's environment that
// are set.
func (p Probe) Environ(pid int, keys ...string) map[string]string {
	out := map[string]string{}
	environHas(filepath.Join(p.dir(pid), "environ"), func(kv []byte) bool {
		k, v, ok := bytes.Cut(kv, []byte("="))
		if ok && slices.Contains(keys, string(k)) {
			out[string(k)] = string(v)
		}
		return false
	})
	return out
}

func (p Probe) dir(pid int) string { return filepath.Join(p.ProcDir, strconv.Itoa(pid)) }

// pids lists the processes, lowest first.
func (p Probe) pids() []int {
	ents, err := os.ReadDir(p.ProcDir)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > 0 && e.Name()[0] != '+' {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// steamAppIDSet matches SteamAppId=<n>, n > 0.
func steamAppIDSet(kv []byte) bool {
	v, ok := bytes.CutPrefix(kv, []byte("SteamAppId="))
	if !ok || len(v) == 0 || v[0] < '1' || v[0] > '9' {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// procUID returns the real uid in <dir>/status, or -1.
func procUID(dir string) int {
	f, err := openNoFollow(filepath.Join(dir, "status"))
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				if n, err := strconv.Atoi(f[0]); err == nil {
					return n
				}
			}
			return -1
		}
	}
	return -1
}

// readCmdline returns <dir>/cmdline's arguments, at most maxCmdline bytes
// of them.
func readCmdline(dir string) []string {
	f, err := openNoFollow(filepath.Join(dir, "cmdline"))
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCmdline))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// environHas streams a NUL-separated environment (/proc/<pid>/environ)
// and reports whether match accepts one of its variables. A game can make
// its environment megabytes long, so at most maxEnviron bytes are read,
// and a variable longer than maxEnvVar is skipped whole.
func environHas(path string, match func([]byte) bool) bool {
	f, err := openNoFollow(path)
	if err != nil {
		return false
	}
	defer f.Close()
	br := bufio.NewReaderSize(io.LimitReader(f, maxEnviron), maxEnvVar)
	long := false
	for {
		kv, err := br.ReadSlice(0)
		if err == bufio.ErrBufferFull {
			long = true
			continue
		}
		if !long && len(kv) > 0 && match(bytes.TrimSuffix(kv, []byte{0})) {
			return true
		}
		long = false
		if err != nil {
			return false
		}
	}
}

// holds reports whether a descriptor in fdDir (/proc/<pid>/fd) is path.
func holds(fdDir, path string) bool {
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		if t, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && t == path {
			return true
		}
	}
	return false
}
