package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"golang.org/x/crypto/argon2"
)

func dirs(t *testing.T) {
	t.Helper()
	oldState, oldRun := config.StateDir, config.RunDir
	d := t.TempDir()
	config.StateDir, config.RunDir = filepath.Join(d, "state"), filepath.Join(d, "run")
	t.Cleanup(func() { config.StateDir, config.RunDir = oldState, oldRun })
}

func writeAdmin(t *testing.T) {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("admin-pass"), salt, 1, 64, 1, 32)
	b64 := base64.RawStdEncoding
	h := fmt.Sprintf("$argon2id$v=19$m=64,t=1,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(key))
	if err := config.WriteJSONAtomic(config.AuthPath(), auth.File{User: "admin", Hash: h}, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewSetupCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := newSetupCode()
		if err != nil {
			t.Fatal(err)
		}
		if !codeRE.MatchString(c) {
			t.Fatalf("code %q has the wrong shape", c)
		}
		if strings.ContainsAny(c, "ILOU") {
			t.Fatalf("code %q uses an ambiguous letter", c)
		}
		// What a person types back is accepted by the api.
		if api.NormalizeCode(strings.ToLower(c)) != strings.ReplaceAll(c, "-", "") {
			t.Fatalf("code %q does not survive normalisation", c)
		}
		seen[c] = true
	}
	if len(seen) < 495 {
		t.Fatalf("only %d distinct codes in 500", len(seen))
	}
}

func TestSetupCodeLifecycle(t *testing.T) {
	dirs(t)
	live := setupCode(true)
	if !codeRE.MatchString(live) {
		t.Fatalf("live code %q", live)
	}
	st, err := os.Stat(setupCodePath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved code: %v %v", st, err)
	}
	if again := setupCode(true); again != live {
		t.Fatalf("restart changed the code: %q -> %q", live, again)
	}
	// Installed without an admin password: still needed.
	if c := setupCode(false); c != live {
		t.Fatalf("installed, no admin: %q", c)
	}
	// A damaged saved code is replaced.
	os.WriteFile(setupCodePath(), []byte("garbage"), 0o600)
	if c := setupCode(true); c == "garbage" || !codeRE.MatchString(c) {
		t.Fatalf("damaged saved code gave %q", c)
	}
	// Installed with an admin: none, and the saved one is gone.
	writeAdmin(t)
	if c := setupCode(false); c != "" {
		t.Fatalf("installed with admin: %q", c)
	}
	if _, err := os.Stat(setupCodePath()); !os.IsNotExist(err) {
		t.Fatal("saved code not removed")
	}
	// The live ISO always wants one.
	if c := setupCode(true); c == "" {
		t.Fatal("live mode without a code")
	}
	forgetSetupCode()
	forgetSetupCode() // idempotent
}

func TestReadyLine(t *testing.T) {
	cases := []struct{ mode, ver, ip, code, want string }{
		{"installer", "20260929.1", "192.168.1.50", "ABCD-EFGH", "VOS-READY mode=installer version=20260929.1 ip=192.168.1.50 code=ABCD-EFGH"},
		{"os", "20260929.1", "", "", "VOS-READY mode=os version=20260929.1 ip=- code=-"},
		{"os", "dev build", "fd00::1", "", "VOS-READY mode=os version=dev_build ip=fd00::1 code=-"},
	}
	for _, c := range cases {
		if got := readyLine(c.mode, c.ver, c.ip, c.code); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the announcer goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAnnouncer(t *testing.T) {
	serial := filepath.Join(t.TempDir(), "ttyS0")
	if err := os.WriteFile(serial, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	ip, code := "", "ABCD-EFGH"
	get := func(p *string) func() string {
		return func() string { mu.Lock(); defer mu.Unlock(); return *p }
	}
	set := func(p *string, v string) { mu.Lock(); *p = v; mu.Unlock() }

	out := &syncBuffer{}
	a := newAnnouncer(true, "20260929.1", get(&code))
	a.firstIP, a.serial, a.stdout, a.interval = get(&ip), serial, out, 10*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)

	lines := func() []string { return strings.Split(strings.TrimSpace(out.String()), "\n") }
	waitFor(t, "first line", func() bool { return strings.Contains(out.String(), "ip=-") })
	time.Sleep(50 * time.Millisecond) // several polls, no change
	if n := len(lines()); n != 1 {
		t.Fatalf("repeated an unchanged line: %q", lines())
	}
	set(&ip, "192.168.1.50")
	waitFor(t, "IP change", func() bool { return len(lines()) == 2 })
	if got := lines()[1]; got != "VOS-READY mode=installer version=20260929.1 ip=192.168.1.50 code=ABCD-EFGH" {
		t.Fatalf("line %q", got)
	}
	set(&code, "")
	waitFor(t, "code change", func() bool { return strings.HasSuffix(out.String(), "code=-\n") })

	b, _ := os.ReadFile(serial)
	if !strings.Contains(string(b), "\nVOS-READY mode=installer version=20260929.1 ip=- code=ABCD-EFGH\n") ||
		!strings.Contains(string(b), "ip=192.168.1.50 code=ABCD-EFGH\n") {
		t.Fatalf("serial got %q", b)
	}

	// A missing serial port is fine.
	a2 := newAnnouncer(false, "v", func() string { return "" })
	a2.serial, a2.stdout = filepath.Join(t.TempDir(), "nope"), &syncBuffer{}
	a2.emit("VOS-READY x")
}

func TestAnnouncerPoke(t *testing.T) {
	var mu sync.Mutex
	code := "ABCD-EFGH"
	out := &syncBuffer{}
	a := newAnnouncer(false, "v1", func() string { mu.Lock(); defer mu.Unlock(); return code })
	a.firstIP, a.serial, a.stdout, a.interval = func() string { return "10.0.0.2" }, "", out, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)
	waitFor(t, "first line", func() bool { return out.String() != "" })
	mu.Lock()
	code = ""
	mu.Unlock()
	a.poke()
	a.poke() // never blocks, even unread
	waitFor(t, "poked line", func() bool {
		return strings.HasSuffix(out.String(), "VOS-READY mode=os version=v1 ip=10.0.0.2 code=-\n")
	})
}

func TestSuperviseRestartsPanics(t *testing.T) {
	old := firstRestartDelay
	firstRestartDelay = time.Millisecond
	defer func() { firstRestartDelay = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	done := make(chan struct{})
	go func() {
		supervise(ctx, runner{"flaky", func(ctx context.Context) {
			calls++
			if calls < 3 {
				panic("boom")
			}
		}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not return after a clean exit")
	}
	if calls != 3 {
		t.Fatalf("ran %d times, want 3 (two panics, then a clean return)", calls)
	}

	// Cancellation stops the restart loop.
	cancel()
	n := 0
	supervise(ctx, runner{"always", func(context.Context) { n++; panic("again") }})
	if n != 1 {
		t.Fatalf("restarted after cancel: %d runs", n)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	waitTimeout(&wg, 20*time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatal("waitTimeout did not give up")
	}
}

func TestSdNotify(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := sdNotify("READY=1"); err != nil {
		t.Fatalf("without NOTIFY_SOCKET: %v", err)
	}
	// Short path: macOS limits unix socket paths to 104 bytes.
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "n")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Skipf("unixgram unavailable: %v", err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", sock)
	if err := sdNotify("READY=1"); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("got %q, %v", buf[:n], err)
	}
}

func TestConfigWatch(t *testing.T) {
	dirs(t)
	now := time.Unix(2e9, 0)
	c := newConfigWatch()
	c.now = func() time.Time { return now }
	tick := func() { now = now.Add(1100 * time.Millisecond) }

	if c.allowPublic() {
		t.Fatal("no config.json: public allowed")
	}
	cfg := config.Defaults()
	cfg.Web.AllowPublic = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if c.allowPublic() {
		t.Fatal("re-read inside the one-second window")
	}
	tick()
	if !c.allowPublic() {
		t.Fatal("allow_public=true not seen")
	}
	os.WriteFile(config.ConfigPath(), []byte("{broken json"), 0o644)
	tick()
	if c.allowPublic() {
		t.Fatal("broken config.json opened the UI")
	}
	os.Remove(config.ConfigPath())
	tick()
	if c.allowPublic() {
		t.Fatal("deleted config.json still public")
	}
}
