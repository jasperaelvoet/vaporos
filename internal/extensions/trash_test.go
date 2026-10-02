package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// runningTruckersMP is a rig with TruckersMP added, started and set up, a
// file in its home data area, and vapor's `rm` held until the func is
// called (then it deletes for real).
func runningTruckersMP(t *testing.T) (r *rig, home string, release func()) {
	t.Helper()
	r = newRig(t)
	useHelper(t, "truckersmp", &recHelper{})
	if code, body := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	r.pass()
	r.boot()
	home = filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "truckersmp")
	writeFile(t, filepath.Join(home, "files", "core_ets2mp.dll"), "mod")
	hold := make(chan struct{})
	r.mu.Lock()
	r.asVapor = func(ctx context.Context, name string, args ...string) (string, error) {
		if name != "rm" {
			return "", nil
		}
		select {
		case <-hold:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", os.RemoveAll(args[len(args)-1])
	}
	r.mu.Unlock()
	var once bool
	return r, home, func() {
		if !once {
			once = true
			close(hold)
		}
	}
}

// trashIn lists the trash names in dir.
func trashIn(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), trashPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// within fails unless f returns within a second.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s waited for the purge", what)
	}
}

// A purge moves the data areas aside and answers; deleting them, which may
// take minutes, holds back no change through the API, and keeps the PC
// awake until it is done.
func TestSlowPurgeHoldsNoChange(t *testing.T) {
	r, home, release := runningTruckersMP(t)
	defer release()
	within(t, "the removal", func() {
		if code, body := r.do("DELETE", "/extensions/truckersmp?purge=1", ""); code != 200 {
			t.Errorf("purge: %d %s", code, body)
		}
	})
	if exists(home) || len(trashIn(t, filepath.Dir(home))) != 1 {
		t.Fatalf("home area there %v, trash %q", exists(home), trashIn(t, filepath.Dir(home)))
	}
	if busy, _ := r.s.Busy(); !busy {
		t.Fatal("not busy while the purge deletes")
	}
	within(t, "a setting", func() {
		if code, body := r.do("PUT", "/extensions/star-citizen/settings", `{"settings":{"disk":"/var"}}`); code != 200 {
			t.Errorf("setting: %d %s", code, body)
		}
	})
	within(t, "Try again", func() { r.do("POST", "/extensions/coolercontrol/retry", "") })
	within(t, "another removal", func() { r.do("DELETE", "/extensions/truckersmp", "") })

	release()
	r.s.waitTrash()
	if got := trashIn(t, filepath.Dir(home)); len(got) != 0 {
		t.Fatalf("trash left: %q", got)
	}
	if busy, _ := r.s.Busy(); busy {
		t.Fatal("busy after the purge")
	}
}

// Added again while its purge still deletes, an extension gets new, empty
// data areas, and its helper sets it up in them.
func TestReAddAfterPurgeGetsEmptyAreas(t *testing.T) {
	r, home, release := runningTruckersMP(t)
	defer release()
	if code, _ := r.do("DELETE", "/extensions/truckersmp?purge=1", ""); code != 200 {
		t.Fatal("purge")
	}
	seen := &dataHelper{recHelper: &recHelper{}}
	useHelper(t, "truckersmp", seen)
	within(t, "adding it again", func() {
		if code, body := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
			t.Errorf("add again: %d %s", code, body)
		}
	})
	r.pass()
	if !slices.Equal(seen.Calls(), []string{"install truckersmp"}) {
		t.Fatalf("helper calls %q", seen.Calls())
	}
	if ents, err := os.ReadDir(home); err != nil || len(ents) != 0 {
		t.Fatalf("home area after adding it again: %v, %v", ents, err)
	}
	release()
	r.s.waitTrash()
	if ents, err := os.ReadDir(home); err != nil || len(ents) != 0 || len(trashIn(t, filepath.Dir(home))) != 0 {
		t.Fatalf("after the purge: home %v %v, trash %q", ents, err, trashIn(t, filepath.Dir(home)))
	}

	// A system area too.
	c := runningCoolerControl(t, &recHelper{})
	data := c.s.ext("coolercontrol", c.s.desc("coolercontrol")).DataDir
	writeFile(t, filepath.Join(data, "config", "config.toml"), "[settings]\n")
	if code, _ := c.do("DELETE", "/extensions/coolercontrol?purge=1", ""); code != 200 {
		t.Fatal("purge coolercontrol")
	}
	if code, _ := c.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatal("add coolercontrol again")
	}
	if ents, err := os.ReadDir(data); err != nil || len(ents) != 0 {
		t.Fatalf("system area after adding it again: %v, %v", ents, err)
	}
	c.s.waitTrash()
	if got := trashIn(t, config.ExtDataDir()); len(got) != 0 {
		t.Fatalf("trash left: %q", got)
	}
}

// driveHelper refuses to remove Star Citizen while its drive is away, in
// words of its own.
type driveHelper struct{ *recHelper }

func (driveHelper) MessageText(code string) (string, bool) {
	if code == "drive-away" {
		return "Star Citizen's drive, Games, isn't connected, so its files stay on it.", true
	}
	return "", false
}

// When its helper's Remove fails (its drive is unplugged), a purge keeps
// the system data area and the settings, which say where its files are,
// so a removal once the drive is back still finds them; the card says
// what the helper said. The home area goes all the same.
func TestFailedRemoveKeepsWhatFindsTheFiles(t *testing.T) {
	r := newRig(t)
	h := driveHelper{&recHelper{}}
	useHelper(t, "star-citizen", h)
	if code, body := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var/mnt/games"}}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	r.pass()
	r.boot()
	state := filepath.Join(config.ExtDataDir(), "star-citizen", "state.json")
	writeFile(t, state, `{"prefix":"/var/mnt/games/VaporOS/star-citizen"}`)
	home := filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "star-citizen")
	must(t, os.MkdirAll(home, 0o755))

	h.removeErr = Refuse("drive-away", errors.New("no filesystem at /var/mnt/games"))
	if code, body := r.do("DELETE", "/extensions/star-citizen?purge=1", ""); code != 200 {
		t.Fatalf("purge: %d %s", code, body)
	}
	r.s.waitTrash()
	if !exists(state) || !exists(settingsPath("star-citizen")) {
		t.Fatalf("state there %v, settings there %v", exists(state), exists(settingsPath("star-citizen")))
	}
	if exists(home) {
		t.Fatal("the home area stayed")
	}
	if x := r.card("star-citizen"); x.State != StateNeedsAttention || x.Reason != "Star Citizen's drive, Games, isn't connected, so its files stay on it." {
		t.Fatalf("card = %s %q", x.State, x.Reason)
	}

	// The drive is back: removing it again deletes what is left.
	h.removeErr = nil
	if code, _ := r.do("DELETE", "/extensions/star-citizen?purge=1", ""); code != 200 {
		t.Fatal("purge again")
	}
	r.s.waitTrash()
	if exists(state) || exists(settingsPath("star-citizen")) || len(trashIn(t, config.ExtDataDir())) != 0 {
		t.Fatal("the second purge left its data")
	}
	if got := h.Calls(); !slices.Equal(got[len(got)-2:], []string{"remove star-citizen purge", "remove star-citizen purge"}) {
		t.Fatalf("helper calls %q", got)
	}
	if x := r.card("star-citizen"); x.State != StateRestartNeeded || x.Reason != "" {
		t.Fatalf("card after = %s %q", x.State, x.Reason)
	}
}

// What a purge left when vosd stopped goes at the next start: the system
// areas as root, the home areas as vapor; the areas themselves stay.
func TestLeftoverTrashGoesAtStart(t *testing.T) {
	r := newRig(t)
	sys := filepath.Join(config.ExtDataDir(), ".trash-coolercontrol-1")
	writeFile(t, filepath.Join(sys, "config.toml"), "x")
	keep := filepath.Join(config.ExtDataDir(), "coolercontrol", "config.toml")
	writeFile(t, keep, "x")
	homeDir := filepath.Join(config.GamerHome, config.ExtGamerDataSubdir)
	writeFile(t, filepath.Join(homeDir, ".trash-truckersmp-2", "f"), "x")
	writeFile(t, filepath.Join(homeDir, "truckersmp", "f"), "x")
	r.s.emptyLeftoverTrash()
	r.s.waitTrash()
	if exists(sys) || !exists(keep) {
		t.Fatalf("system trash there %v, area there %v", exists(sys), exists(keep))
	}
	if want := []string{"rm -rf -- " + filepath.Join(homeDir, ".trash-truckersmp-2")}; !slices.Equal(r.gamer, want) {
		t.Fatalf("as vapor %q, want %q", r.gamer, want)
	}
}
