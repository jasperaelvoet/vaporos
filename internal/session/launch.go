package session

// `vos session launch <steam-url>` starts a game in the gaming user's
// Steam, the one gamescope runs. Sunshine runs it as a "detached" app
// command (as the gaming user), right after `vos session begin` returned.
// After a cold start or an HDR restart gamescope's Steam is still coming
// up then, and `steam <url>` without a running client would start a
// second Steam in Sunshine's session instead of handing the URL over. So
// this waits until that Steam takes URLs, then runs `steam <url>` in
// gamescope's environment.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// LaunchTimeout bounds the wait for Steam.
const LaunchTimeout = 90 * time.Second

// launchEnvKeys are what `steam <url>` takes from the running Steam's
// environment: how to reach gamescope's X and Wayland servers and the
// user's session.
var launchEnvKeys = []string{"DISPLAY", "WAYLAND_DISPLAY", "GAMESCOPE_WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"}

// launcher is `vos session launch`; tests point its paths elsewhere.
type launcher struct {
	uid        int
	home       string // Steam's command pipe is ~/.steam/steam.pipe
	runtimeDir string // gamescope's Wayland socket ("gamescope-0") is here
	x11Dir     string // Xwayland's sockets ("X0")
	procDir    string
	steam      string   // the Steam launcher
	env        []string // our own environment
	poll       time.Duration
	timeout    time.Duration // for Steam to come up
	handOver   time.Duration // for `steam <url>` to pass the URL on
	log        *log.Logger
}

func newLauncher(stderr io.Writer) *launcher {
	uid := os.Getuid()
	home, err := os.UserHomeDir()
	if err != nil {
		home = config.GamerHome
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = "/run/user/" + strconv.Itoa(uid)
	}
	return &launcher{
		uid: uid, home: home, runtimeDir: rt, x11Dir: "/tmp/.X11-unix", procDir: "/proc",
		steam: "steam", env: os.Environ(),
		poll: 500 * time.Millisecond, timeout: LaunchTimeout, handOver: 30 * time.Second,
		log: log.New(stderr, "vos session: ", 0),
	}
}

// validSteamURL accepts steam:// URLs only, without spaces or control
// characters (they end up as one argument of the Steam launcher).
func validSteamURL(u string) bool {
	if !strings.HasPrefix(u, "steam://") || len(u) > 1024 || len(u) == len("steam://") {
		return false
	}
	for _, r := range u {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// run waits for Steam and hands it the URL. It reports whether it ran
// `steam <url>`.
func (l *launcher) run(ctx context.Context, url string) bool {
	if !validSteamURL(url) {
		l.log.Printf("launch: %q is not a steam:// URL", url)
		return false
	}
	if l.uid == 0 {
		l.log.Print("launch: runs as the gaming user, not as root")
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	var fallback *steamProc
	for {
		sock := l.gamescopeSocket()
		var s *steamProc
		if sock != "" {
			s, fallback = l.findSteam()
		}
		if s != nil {
			l.log.Printf("launch: Steam (pid %d) is up in gamescope; opening %s", s.pid, url)
			return l.exec(url, s, sock)
		}
		select {
		case <-ctx.Done():
			if fallback != nil && sock != "" {
				// A Steam client runs in gamescope but was never seen
				// holding its command pipe: try anyway.
				l.log.Printf("launch: Steam (pid %d) never showed its command pipe; opening %s anyway", fallback.pid, url)
				return l.exec(url, fallback, sock)
			}
			l.log.Printf("launch: Steam did not come up in gamescope within %s; not opening %s", l.timeout, url)
			return false
		case <-time.After(l.poll):
		}
	}
}

// exec runs `steam <url>` in s's environment and waits (a while) for it to
// hand the URL over and exit; a launcher that takes longer is left running.
func (l *launcher) exec(url string, s *steamProc, sock string) bool {
	cmd := exec.Command(l.steam, url)
	cmd.Env = l.steamEnv(s, sock)
	cmd.Stdout, cmd.Stderr = l.log.Writer(), l.log.Writer()
	if err := cmd.Start(); err != nil {
		l.log.Printf("launch: %v", err)
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			l.log.Printf("launch: steam %s: %v", url, err)
		}
	case <-time.After(l.handOver):
		l.log.Printf("launch: steam %s still runs after %s; leaving it", url, l.handOver)
	}
	return true
}

// steamEnv is our environment with the running Steam's display, Wayland
// and session variables (the gamescope session's), filled in from what
// gamescope serves where Steam's own environment lacks them.
func (l *launcher) steamEnv(s *steamProc, sock string) []string {
	vars := map[string]string{}
	for _, k := range launchEnvKeys {
		if v, ok := s.env[k]; ok {
			vars[k] = v
		}
	}
	if vars["DISPLAY"] == "" {
		vars["DISPLAY"] = ":" + strings.TrimPrefix(lowestSocket(l.x11Dir, "X", "X0"), "X")
	}
	if vars["GAMESCOPE_WAYLAND_DISPLAY"] == "" {
		vars["GAMESCOPE_WAYLAND_DISPLAY"] = sock
	}
	if vars["XDG_RUNTIME_DIR"] == "" {
		vars["XDG_RUNTIME_DIR"] = l.runtimeDir
	}
	env := make([]string, 0, len(l.env)+len(vars))
	for _, kv := range l.env {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(launchEnvKeys, k) {
			env = append(env, kv)
		}
	}
	for _, k := range launchEnvKeys {
		if v, ok := vars[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// gamescopeSocket returns gamescope's Wayland socket name ("gamescope-0"),
// or "" while gamescope is not up.
func (l *launcher) gamescopeSocket() string {
	return lowestSocket(l.runtimeDir, "gamescope-", "")
}

// steamProc is a Steam process of ours and its environment.
type steamProc struct {
	pid int
	env map[string]string
}

// findSteam looks for the Steam client that takes steam:// URLs: the one
// holding ~/.steam/steam.pipe open, as the Steam launcher itself checks
// before it forwards a URL. The pid in ~/.steam/steam.pid is tried first.
// fallback is a process named steam running in gamescope, if any.
func (l *launcher) findSteam() (s, fallback *steamProc) {
	pipe := filepath.Join(l.home, ".steam", "steam.pipe")
	targets := []string{pipe}
	if p, err := filepath.EvalSymlinks(pipe); err == nil && p != pipe {
		targets = append(targets, p)
	}
	pids := l.pids()
	if b, err := readSmall(filepath.Join(l.home, ".steam", "steam.pid"), 64); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			pids = append([]int{pid}, pids...)
		}
	}
	seen := map[int]bool{}
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		dir := filepath.Join(l.procDir, strconv.Itoa(pid))
		if procUID(filepath.Join(dir, "status")) != l.uid {
			continue
		}
		if holdsFile(filepath.Join(dir, "fd"), targets) {
			return &steamProc{pid: pid, env: readEnviron(filepath.Join(dir, "environ"))}, nil
		}
		if fallback == nil {
			if comm, err := readSmall(filepath.Join(dir, "comm"), 64); err == nil && strings.TrimSpace(string(comm)) == "steam" {
				if env := readEnviron(filepath.Join(dir, "environ")); env["GAMESCOPE_WAYLAND_DISPLAY"] != "" {
					fallback = &steamProc{pid: pid, env: env}
				}
			}
		}
	}
	return nil, fallback
}

// pids lists the processes in procDir, lowest first.
func (l *launcher) pids() []int {
	ents, err := os.ReadDir(l.procDir)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > 0 {
			pids = append(pids, n)
		}
	}
	sort.Ints(pids)
	return pids
}

// holdsFile reports whether any descriptor in fdDir (/proc/<pid>/fd) is
// one of the paths.
func holdsFile(fdDir string, paths []string) bool {
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		if t, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && slices.Contains(paths, t) {
			return true
		}
	}
	return false
}

// procUID returns the real uid from /proc/<pid>/status, or -1.
func procUID(path string) int {
	b, err := readSmall(path, 64<<10)
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				if n, err := strconv.Atoi(f[0]); err == nil {
					return n
				}
			}
		}
	}
	return -1
}

// maxEnvVar bounds one variable read from a process's environment; the
// variables launch wants are short.
const maxEnvVar = 4096

// readEnviron reads the variables of a NUL-separated environment
// (/proc/<pid>/environ), at most 1 MiB of it, skipping overlong ones.
func readEnviron(path string) map[string]string {
	env := map[string]string{}
	f, err := openNoFollow(path)
	if err != nil {
		return env
	}
	defer f.Close()
	br := bufio.NewReaderSize(io.LimitReader(f, 1<<20), maxEnvVar)
	long := false
	for {
		kv, err := br.ReadSlice(0)
		if err == bufio.ErrBufferFull {
			long = true
			continue
		}
		if !long {
			if k, v, ok := strings.Cut(string(bytes.TrimSuffix(kv, []byte{0})), "="); ok && k != "" {
				env[k] = v
			}
		}
		long = false
		if err != nil {
			return env
		}
	}
}

// readSmall reads a regular file of at most max bytes without following
// a symlink at its end or blocking on a FIFO.
func readSmall(path string, max int64) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// lowestSocket returns the socket in dir named prefix<N> with the lowest
// N, or fallback.
func lowestSocket(dir, prefix, fallback string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fallback
	}
	best, bestN := fallback, -1
	for _, e := range ents {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || e.Type()&os.ModeSocket == 0 {
			continue
		}
		if n, err := strconv.Atoi(rest); err == nil && n >= 0 && (bestN < 0 || n < bestN) {
			best, bestN = e.Name(), n
		}
	}
	return best
}
