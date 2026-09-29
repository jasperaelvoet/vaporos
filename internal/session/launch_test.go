package session

import (
	"bytes"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer the logger and a child's output copier may
// write at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type launchEnv struct {
	l    *launcher
	out  string // what the fake steam recorded
	logs *syncBuffer
	pipe string
	proc string
}

// newLaunchEnv builds a gaming user's world: a home with ~/.steam, a
// runtime dir, /proc, and a steam launcher that records how it was run.
func newLaunchEnv(t *testing.T) *launchEnv {
	t.Helper()
	dir := t.TempDir()
	// Unix socket paths are short on macOS.
	rt, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	e := &launchEnv{out: filepath.Join(dir, "steam.out"), logs: &syncBuffer{}, proc: filepath.Join(dir, "proc")}
	home := filepath.Join(dir, "home")
	e.pipe = filepath.Join(home, ".steam", "steam.pipe")
	write(t, e.pipe, "")
	steam := filepath.Join(dir, "steam")
	write(t, steam, "#!/bin/sh\necho \"$@\" > "+e.out+".args\nenv > "+e.out+"\n")
	os.Chmod(steam, 0o755)
	e.l = &launcher{
		uid: 1000, home: home, runtimeDir: rt, x11Dir: filepath.Join(dir, "x11"), procDir: e.proc,
		steam: steam, env: []string{"PATH=" + os.Getenv("PATH"), "DISPLAY=:9", "SUNSHINE_APP=x"},
		poll: 5 * time.Millisecond, timeout: 2 * time.Second, handOver: 5 * time.Second,
		log: log.New(e.logs, "vos session: ", 0),
	}
	return e
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gamescope starts gamescope: its Wayland socket appears.
func (e *launchEnv) gamescope(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(e.l.runtimeDir, "gamescope-0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

// process adds a process of uid to /proc; holding makes it hold the pipe.
func (e *launchEnv) process(t *testing.T, pid, uid int, comm, environ string, holding bool) {
	d := filepath.Join(e.proc, strconv.Itoa(pid))
	write(t, filepath.Join(d, "status"), "Name:\t"+comm+"\nUid:\t"+strconv.Itoa(uid)+"\t0\t0\t0\n")
	write(t, filepath.Join(d, "comm"), comm+"\n")
	write(t, filepath.Join(d, "environ"), environ)
	os.MkdirAll(filepath.Join(d, "fd"), 0o755)
	os.Symlink("/dev/null", filepath.Join(d, "fd", "0"))
	if holding {
		os.Symlink(e.pipe, filepath.Join(d, "fd", "57"))
	}
}

func (e *launchEnv) ran(t *testing.T) (args string, env []string) {
	t.Helper()
	a, err := os.ReadFile(e.out + ".args")
	if err != nil {
		return "", nil
	}
	b, _ := os.ReadFile(e.out)
	return strings.TrimSpace(string(a)), strings.Split(string(b), "\n")
}

const steamEnviron = "HOME=/var/home/vapor\x00DISPLAY=:1\x00GAMESCOPE_WAYLAND_DISPLAY=gamescope-0\x00XDG_RUNTIME_DIR=/run/user/1000\x00"

func TestLaunchHandsURLToSteamInGamescope(t *testing.T) {
	e := newLaunchEnv(t)
	e.gamescope(t)
	e.process(t, 10, 0, "systemd", "", false)
	e.process(t, 40, 1000, "steam", steamEnviron, false) // the launcher script
	e.process(t, 42, 1000, "steam", steamEnviron+"LONG="+strings.Repeat("x", 10000)+"\x00", true)
	e.process(t, 50, 1001, "steam", steamEnviron, true) // someone else's
	if !e.l.run(context.Background(), "steam://rungameid/730") {
		t.Fatalf("not launched: %s", e.logs.String())
	}
	args, env := e.ran(t)
	if args != "steam://rungameid/730" {
		t.Errorf("args = %q", args)
	}
	for _, want := range []string{"DISPLAY=:1", "GAMESCOPE_WAYLAND_DISPLAY=gamescope-0", "XDG_RUNTIME_DIR=/run/user/1000", "SUNSHINE_APP=x"} {
		if !contains(env, want) {
			t.Errorf("env lacks %s: %v", want, env)
		}
	}
	if contains(env, "DISPLAY=:9") || !strings.Contains(e.logs.String(), "pid 42") {
		t.Errorf("env %v, log %s", env, e.logs.String())
	}
}

func TestLaunchWaitsForSteam(t *testing.T) {
	e := newLaunchEnv(t)
	done := make(chan bool, 1)
	go func() { done <- e.l.run(context.Background(), "steam://rungameid/440") }()
	time.Sleep(50 * time.Millisecond)
	e.process(t, 42, 1000, "steam", steamEnviron, true) // Steam is up, gamescope not yet
	time.Sleep(50 * time.Millisecond)
	if args, _ := e.ran(t); args != "" {
		t.Fatal("launched before gamescope was up")
	}
	e.gamescope(t)
	if !<-done {
		t.Fatalf("not launched: %s", e.logs.String())
	}
	if args, _ := e.ran(t); args != "steam://rungameid/440" {
		t.Errorf("args = %q", args)
	}
}

func TestLaunchGivesUp(t *testing.T) {
	e := newLaunchEnv(t)
	e.l.timeout = 100 * time.Millisecond
	e.gamescope(t)
	e.process(t, 42, 1000, "bash", steamEnviron, false)
	start := time.Now()
	if e.l.run(context.Background(), "steam://rungameid/440") {
		t.Error("launched without a Steam")
	}
	if time.Since(start) > time.Second || !strings.Contains(e.logs.String(), "did not come up") {
		t.Errorf("took %s: %s", time.Since(start), e.logs.String())
	}
	if args, _ := e.ran(t); args != "" {
		t.Error("steam ran")
	}
}

// TestLaunchFallback: a Steam client runs in gamescope, but was never
// seen holding its pipe: at the deadline the URL goes to it anyway.
func TestLaunchFallback(t *testing.T) {
	e := newLaunchEnv(t)
	e.l.timeout = 100 * time.Millisecond
	e.gamescope(t)
	e.process(t, 42, 1000, "steam", steamEnviron, false)
	if !e.l.run(context.Background(), "steam://rungameid/440") {
		t.Fatalf("no fallback: %s", e.logs.String())
	}
	if _, env := e.ran(t); !contains(env, "DISPLAY=:1") {
		t.Errorf("env = %v", env)
	}
}

func TestLaunchRefuses(t *testing.T) {
	e := newLaunchEnv(t)
	e.gamescope(t)
	e.process(t, 42, 1000, "steam", steamEnviron, true)
	for _, u := range []string{"", "steam://", "http://example.com", "steam://run/1 --evil", "steam://x\n", "file:///etc/passwd"} {
		if e.l.run(context.Background(), u) {
			t.Errorf("launched %q", u)
		}
	}
	e.l.uid = 0
	if e.l.run(context.Background(), "steam://rungameid/1") {
		t.Error("launched as root")
	}
	if args, _ := e.ran(t); args != "" {
		t.Errorf("steam ran with %q", args)
	}
}

func TestReadEnviron(t *testing.T) {
	p := filepath.Join(t.TempDir(), "environ")
	write(t, p, "A=1\x00B=x=y\x00BIG="+strings.Repeat("z", 3*maxEnvVar)+"\x00C=3")
	env := readEnviron(p)
	if env["A"] != "1" || env["B"] != "x=y" || env["C"] != "3" || env["BIG"] != "" || len(env) != 3 {
		t.Errorf("env = %v", env)
	}
	// Not through a symlink.
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(p, link)
	if len(readEnviron(link)) != 0 {
		t.Error("followed a symlink")
	}
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }
