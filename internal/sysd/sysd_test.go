package sysd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestRunOutputAndError(t *testing.T) {
	out, err := Run(context.Background(), "sh", "-c", "echo hello; echo oops >&2")
	if err != nil || out != "hello\noops" {
		t.Errorf("Run = %q, %v", out, err)
	}
	out, err = Run(context.Background(), "sh", "-c", "echo bad; exit 3")
	if err == nil || out != "bad" || !strings.HasPrefix(err.Error(), "sh -c echo bad; exit 3: exit status 3: bad") {
		t.Errorf("failing Run = %q, %v", out, err)
	}
}

// TestRunTimeoutWithOrphan: a command whose child outlives it keeps the
// output pipe open; Run still returns soon after its context ends.
func TestRunTimeoutWithOrphan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	// sh forks sleep (it is not the last command, so no exec); killing sh
	// leaves sleep holding the pipe for 6 s.
	_, err := Run(ctx, "sh", "-c", "sleep 6; true")
	if took := time.Since(start); took > 200*time.Millisecond+waitDelay+time.Second {
		t.Errorf("Run took %s after its context ended", took)
	}
	if err == nil {
		t.Error("a killed command succeeded")
	}
}

// TestGroupCommandKillsDescendants: on timeout the whole process group
// dies, so Run returns at once instead of after waitDelay.
func TestGroupCommandKillsDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := run(groupCommand(ctx, "sh", "-c", "sleep 20; true"))
	if took := time.Since(start); took > time.Second+200*time.Millisecond {
		t.Errorf("group kill took %s", took)
	}
	if err == nil {
		t.Error("a killed command succeeded")
	}
	// A command that finishes by itself is unaffected.
	if out, err := run(groupCommand(context.Background(), "sh", "-c", "echo ok")); err != nil || out != "ok" {
		t.Errorf("run = %q, %v", out, err)
	}
}

// fakeSystemctl puts a systemctl first on PATH that logs each call as
// "<uid> <XDG_RUNTIME_DIR> <args>" and answers is-active as the real one
// does: the state on stdout, exit 0 only for active. down.service stands
// for a user manager that is not up. It returns the logged calls.
func fakeSystemctl(t *testing.T) func() []string {
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s %s %s\n' "$(id -u)" "${XDG_RUNTIME_DIR:-unset}" "$*" >>"$0.log"
for unit; do :; done
case "$*" in
*is-active*)
	case "$unit" in
	run.service) echo active; exit 0 ;;
	restart.service) echo activating; exit 3 ;;
	down.service) echo "Failed to connect to user scope bus via local transport: No such file or directory" >&2; exit 1 ;;
	*) echo inactive; exit 3 ;;
	esac ;;
esac
echo "shown $unit"
`
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(path + ".log")
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func gamerConfig(t *testing.T, uid int, runtimeDir string) {
	oldUID, oldDir := config.GamerUID, config.GamerRuntimeDir
	t.Cleanup(func() { config.GamerUID, config.GamerRuntimeDir = oldUID, oldDir })
	config.GamerUID, config.GamerRuntimeDir = uid, runtimeDir
}

// TestUserQueriesWithoutSession: state checks of user units reach the
// gaming user's manager as that user through its runtime dir, never with
// -M (a PAM login session per call); actions still use -M.
func TestUserQueriesWithoutSession(t *testing.T) {
	calls := fakeSystemctl(t)
	t.Setenv("XDG_RUNTIME_DIR", "")
	gamerConfig(t, os.Geteuid(), "/run/test-gamer")
	ctx := context.Background()

	if !IsActive(ctx, "run.service", true) || IsActive(ctx, "restart.service", true) {
		t.Error("IsActive of user units is wrong")
	}
	if got := ActiveState(ctx, "restart.service", true); got != "activating" {
		t.Errorf("ActiveState = %q, want activating", got)
	}
	if got := ActiveState(ctx, "down.service", true); got != "" {
		t.Errorf("ActiveState with no user manager = %q, want empty", got)
	}
	if out, err := Query(ctx, true, "show", "-p", "ActiveState", "u.service"); err != nil || out != "shown u.service" {
		t.Errorf("Query = %q, %v", out, err)
	}
	if _, err := Query(ctx, true, "is-active", "down.service"); err == nil {
		t.Error("Query succeeded with no user manager")
	}
	if !IsActive(ctx, "run.service", false) {
		t.Error("IsActive of a system unit is wrong")
	}
	if err := UserSystemctl(ctx, "start", "u.service"); err != nil {
		t.Fatal(err)
	}

	uid := strconv.Itoa(os.Geteuid())
	want := []string{
		uid + " /run/test-gamer --user is-active --quiet run.service",
		uid + " /run/test-gamer --user is-active --quiet restart.service",
		uid + " /run/test-gamer --user is-active restart.service",
		uid + " /run/test-gamer --user is-active down.service",
		uid + " /run/test-gamer --user show -p ActiveState u.service",
		uid + " /run/test-gamer --user is-active down.service",
		uid + " unset is-active --quiet run.service",
		uid + " unset --user -M " + config.GamerUser + "@ start u.service",
	}
	if got := calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestUserQueryRunsAsGamer: from root (any other user), a user query
// switches to the gaming user and its primary group, with no supplementary
// groups; a system query runs as vosd itself.
func TestUserQueryRunsAsGamer(t *testing.T) {
	gamerConfig(t, os.Geteuid()+1, "/run/test-gamer")
	id := uint32(os.Geteuid() + 1)

	cmd := query(context.Background(), true, "is-active", "u.service")
	if want := []string{"systemctl", "--user", "is-active", "u.service"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("args = %q, want %q", cmd.Args, want)
	}
	if cmd.SysProcAttr == nil || !reflect.DeepEqual(cmd.SysProcAttr.Credential, &syscall.Credential{Uid: id, Gid: id}) {
		t.Errorf("SysProcAttr = %+v, want the gaming user's credential", cmd.SysProcAttr)
	}
	if n := len(cmd.Env); n == 0 || cmd.Env[n-1] != "XDG_RUNTIME_DIR=/run/test-gamer" {
		t.Errorf("env ends %q", cmd.Env[max(n-1, 0):])
	}

	cmd = query(context.Background(), false, "is-active", "u.service")
	if cmd.SysProcAttr != nil || cmd.Env != nil {
		t.Errorf("a system query changes user (%+v) or env (%d)", cmd.SysProcAttr, len(cmd.Env))
	}
}
