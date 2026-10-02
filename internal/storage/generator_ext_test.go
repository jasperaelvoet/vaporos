package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

const coolerDescriptor = `{"schema":1,"id":"cooler","name":"Cooler","summary":"Fans.","category":"system",
 "upstream":{"name":"Cooler","url":"https://example.com/cooler","license":"MIT"},
 "permissions":["service","user-service"],
 "services":[{"unit":"coolerd.service","scope":"system"},{"unit":"cooler-poll.timer","scope":"system"},
             {"unit":"cooler@fan0.service","scope":"system"},{"unit":"cooler-tray.service","scope":"user"}]}`

// extFixture is a booted system with the cooler extension's units and
// descriptor in place; report writes /run/vos/extensions.json.
func extFixture(t *testing.T) (dir string, report func(rep *store.BootReport)) {
	t.Helper()
	isolate(t)
	oldDesc, oldUnits, oldCount := config.ExtDescriptorsDir, systemUnitDir, config.BootCountVar
	t.Cleanup(func() { config.ExtDescriptorsDir, systemUnitDir, config.BootCountVar = oldDesc, oldUnits, oldCount })
	config.ExtDescriptorsDir, systemUnitDir = t.TempDir(), t.TempDir()
	config.BootCountVar = filepath.Join(t.TempDir(), "LoaderBootCountPath")
	if err := os.WriteFile(filepath.Join(config.ExtDescriptorsDir, "cooler.json"), []byte(coolerDescriptor), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"coolerd.service", "cooler-poll.timer", "cooler@.service", "cooler-tray.service"} {
		if err := os.WriteFile(filepath.Join(systemUnitDir, u), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return t.TempDir(), func(rep *store.BootReport) {
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(config.ExtBootPath(), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mounted(ids ...string) []store.Mounted {
	var out []store.Mounted
	for _, id := range ids {
		out = append(out, store.Mounted{ID: id, SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64)})
	}
	return out
}

func TestGeneratorExtensions(t *testing.T) {
	dir, report := extFixture(t)
	report(&store.BootReport{Mode: store.ModeEnabled, Set: "2", Mounted: mounted("cooler", "ghost")})
	errs := generate(dir)
	// ghost has no descriptor: logged, and the rest still generated.
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "extension ghost") {
		t.Fatalf("errors %v", errs)
	}
	for link, want := range map[string]string{
		"multi-user.target.wants/coolerd.service":     "coolerd.service",
		"timers.target.wants/cooler-poll.timer":       "cooler-poll.timer",
		"multi-user.target.wants/cooler@fan0.service": "cooler@.service",
	} {
		if got, err := os.Readlink(filepath.Join(dir, link)); err != nil || got != filepath.Join(systemUnitDir, want) {
			t.Errorf("%s -> %q, %v", link, got, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "multi-user.target.wants/cooler-tray.service")); !os.IsNotExist(err) {
		t.Error("a user unit was wanted by the system manager")
	}
	if _, err := os.Stat(filepath.Join(dir, "vos-health.service.d")); !os.IsNotExist(err) {
		t.Error("trial drop-in on a boot that is not on trial")
	}
	// daemon-reload runs it again into the same directory.
	if errs := generate(dir); len(errs) != 1 {
		t.Fatalf("rerun: %v", errs)
	}
}

// Only what this boot mounted counts, never what is enabled or wanted.
func TestGeneratorExtensionsSkipped(t *testing.T) {
	dir, report := extFixture(t)
	report(&store.BootReport{Mode: store.ModePending, Set: "3", TriesLeft: 1,
		Skipped: []store.Skipped{{ID: "cooler", Reason: store.SkipFSVerity}}})
	if errs := generate(dir); len(errs) != 0 {
		t.Fatalf("errors %v", errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "multi-user.target.wants")); !os.IsNotExist(err) {
		t.Error("a skipped extension's service is wanted")
	}
}

func TestGeneratorTrialDropin(t *testing.T) {
	cases := map[string]struct {
		rep     *store.BootReport // nil: a report that cannot be read
		counted bool              // systemd-boot counts this boot
		want    bool
	}{
		"pending":  {rep: &store.BootReport{Mode: store.ModePending}, want: true},
		"os-trial": {rep: &store.BootReport{Mode: store.ModeOSTrial}, counted: true, want: true},
		"enabled":  {rep: &store.BootReport{Mode: store.ModeEnabled}},
		"off":      {rep: &store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoReport}},
		// An OS trial with vos.ext=0 or skip-once mounts nothing (mode off),
		// and is a trial all the same.
		"counted, cmdline":           {rep: &store.BootReport{Mode: store.ModeOff, Reason: store.ReasonCmdline}, counted: true, want: true},
		"counted, skip-once":         {rep: &store.BootReport{Mode: store.ModeOff, Reason: store.ReasonSkipOnce}, counted: true, want: true},
		"counted, unreadable report": {counted: true, want: true},
		"unreadable report":          {},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, report := extFixture(t)
			if c.rep != nil {
				c.rep.Mounted = mounted("cooler")
				report(c.rep)
			} else {
				os.WriteFile(config.ExtBootPath(), []byte("{broken"), 0o644)
			}
			if c.counted {
				os.WriteFile(config.BootCountVar, []byte("x"), 0o644)
			}
			errs := generate(dir)
			if c.rep == nil && len(errs) != 1 || c.rep != nil && len(errs) != 0 {
				t.Fatalf("errors %v", errs)
			}
			b, err := os.ReadFile(filepath.Join(dir, "vos-health.service.d", "50-vos-trial.conf"))
			if !c.want {
				if !os.IsNotExist(err) {
					t.Fatalf("drop-in: %q %v", b, err)
				}
				return
			}
			if err != nil || !strings.Contains(string(b), "[Unit]\nJobTimeoutSec=10min\nJobTimeoutAction=reboot-force\n") {
				t.Fatalf("drop-in %q, %v", b, err)
			}
		})
	}
}

func TestGeneratorExtensionProblems(t *testing.T) {
	dir, report := extFixture(t)
	os.Remove(filepath.Join(systemUnitDir, "cooler-poll.timer"))
	report(&store.BootReport{Mode: store.ModeEnabled, Mounted: mounted("cooler")})
	errs := generate(dir)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "cooler-poll.timer") {
		t.Fatalf("errors %v", errs)
	}
	if _, err := os.Lstat(filepath.Join(dir, "timers.target.wants/cooler-poll.timer")); !os.IsNotExist(err) {
		t.Error("wanted a unit that does not exist")
	}
	if _, err := os.Readlink(filepath.Join(dir, "multi-user.target.wants/coolerd.service")); err != nil {
		t.Errorf("the other units: %v", err)
	}

	// A descriptor shipped under another id's name is refused.
	os.WriteFile(filepath.Join(config.ExtDescriptorsDir, "fans.json"), []byte(coolerDescriptor), 0o644)
	report(&store.BootReport{Mode: store.ModeEnabled, Mounted: mounted("fans")})
	if errs := generate(t.TempDir()); len(errs) != 1 || !strings.Contains(errs[0].Error(), `for "cooler"`) {
		t.Fatalf("errors %v", errs)
	}

	// A broken report costs the extension units, not the library mounts.
	os.WriteFile(config.ExtBootPath(), []byte("{broken"), 0o644)
	cfg := config.Defaults()
	cfg.SSH.Enabled = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	if code := CLIGenerator([]string{dir}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Readlink(filepath.Join(dir, "multi-user.target.wants", "sshd.service")); err != nil {
		t.Errorf("sshd: %v", err)
	}
}
