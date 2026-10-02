package extensions

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

func TestFetchAndLaunchAreRegistered(t *testing.T) {
	for _, name := range []string{"fetch", "launch"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("vos ext %s is not registered", name)
		}
	}
}

func TestParseLaunch(t *testing.T) {
	for _, c := range []struct {
		args     []string
		app      uint32
		shortcut string
		key      string
		argv     []string
	}{
		{[]string{"--app", "227300", "/games/ets2", "-nointro", "-64bit"}, 227300, "", "", []string{"/games/ets2", "-nointro", "-64bit"}},
		{[]string{"--app=4294967295", "--", "-odd"}, 4294967295, "", "", []string{"-odd"}},
		{[]string{"--shortcut", "star-citizen/launcher", "/x/reaper", "SteamLaunch", "AppId=1"}, 0, "star-citizen", "launcher", []string{"/x/reaper", "SteamLaunch", "AppId=1"}},
		{[]string{"--shortcut=truckersmp/ets2-mp", "run"}, 0, "truckersmp", "ets2-mp", []string{"run"}},
		{[]string{"/bin/game", "--app", "1"}, 0, "", "", []string{"/bin/game", "--app", "1"}},
		{[]string{"--", "--app", "1"}, 0, "", "", []string{"--app", "1"}},
		{[]string{"PROTON_LOG=1", "/bin/game"}, 0, "", "", []string{"PROTON_LOG=1", "/bin/game"}},
	} {
		l, err := parseLaunch(c.args)
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if l.app != c.app || l.shortcut != c.shortcut || l.key != c.key || !slices.Equal(l.argv, c.argv) {
			t.Errorf("%q: got %+v", c.args, l)
		}
	}
	for _, args := range [][]string{
		nil,
		{"--"},
		{"--app"},
		{"--app", "1"},
		{"--app", "x", "/g"},
		{"--app", "0", "/g"},
		{"--app", "-1", "/g"},
		{"--app", "+1", "/g"},
		{"--app", "4294967296", "/g"},
		{"--app=", "/g"},
		{"--app", "1", "--app", "2", "/g"},
		{"--app", "1", "--shortcut", "a/b", "/g"},
		{"--shortcut", "proton", "/g"},
		{"--shortcut", "Proton/x", "/g"},
		{"--shortcut", "proton/", "/g"},
		{"--shortcut", "proton/a/b", "/g"},
	} {
		if l, err := parseLaunch(args); err == nil {
			t.Errorf("%q: parsed as %+v", args, l)
		}
	}
}

type execCall struct {
	path string
	argv []string
	env  []string
}

func fakeExec(t *testing.T) *[]execCall {
	var calls []execCall
	e := execve
	t.Cleanup(func() { execve = e })
	execve = func(path string, argv, env []string) error {
		calls = append(calls, execCall{path, argv, env})
		return errors.New("exec faked")
	}
	return &calls
}

func runLaunch(args ...string) (int, string) {
	var stderr bytes.Buffer
	rc := launchCmd(args, &stderr)
	return rc, stderr.String()
}

func TestLaunchPassesThrough(t *testing.T) {
	e := newEnv(t)
	calls := fakeExec(t)
	t.Setenv("SteamAppId", "227300")

	// No boot report at all: --app still runs the game.
	rc, _ := runLaunch("--app", "227300", "/games/ets2", "-nointro")
	if rc != 1 || len(*calls) != 1 { // 1: the fake exec "failed"
		t.Fatalf("exit %d, calls %+v", rc, *calls)
	}
	c := (*calls)[0]
	if c.path != "/games/ets2" || !slices.Equal(c.argv, []string{"/games/ets2", "-nointro"}) || !slices.Contains(c.env, "SteamAppId=227300") {
		t.Fatalf("exec %+v", c)
	}

	// A name is looked up in PATH; argv[0] stays as given.
	sh, err := exec.LookPath("sh")
	must(t, err)
	runLaunch("sh", "-c", "true")
	if c := (*calls)[1]; c.path != sh || !filepath.IsAbs(c.path) || c.argv[0] != "sh" {
		t.Fatalf("exec %+v", c)
	}

	// A shortcut runs only while its extension is mounted.
	sc := newImage(t, "star-citizen", "", 100, false)
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "1", Mounted: mountedAs(sc)})
	runLaunch("--shortcut", "star-citizen/launcher", "/x/reaper")
	if len(*calls) != 3 || (*calls)[2].path != "/x/reaper" {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestLaunchRefuses(t *testing.T) {
	newEnv(t)
	calls := fakeExec(t)
	for name, report := range map[string]string{
		"no report":   "",
		"not mounted": `{"mode":"enabled","set":"1","mounted":[],"skipped":[{"id":"star-citizen","reason":"unproven"}]}`,
		"unreadable":  `{"mode":`,
	} {
		os.Remove(config.ExtBootPath())
		if report != "" {
			writeFile(t, config.ExtBootPath(), report)
		}
		rc, msg := runLaunch("--shortcut", "star-citizen/launcher", "/x/reaper")
		if rc != 1 || !strings.Contains(msg, "star-citizen extension is not installed") {
			t.Errorf("%s: exit %d: %s", name, rc, msg)
		}
	}
	if rc, _ := runLaunch("--app", "1", "/no/such/dir/game"); rc != 1 {
		t.Errorf("a missing absolute command: exit %d", rc)
	}
	calls0 := len(*calls)
	if rc, msg := runLaunch("no-such-command-vaporos"); rc != 1 || !strings.Contains(msg, "no-such-command-vaporos") {
		t.Errorf("unknown command: exit %d: %s", rc, msg)
	}
	if rc, _ := runLaunch("--app"); rc != 2 {
		t.Errorf("usage: exit %d", rc)
	}
	if len(*calls) != calls0 || calls0 != 1 {
		t.Fatalf("exec calls %+v", *calls)
	}
}
