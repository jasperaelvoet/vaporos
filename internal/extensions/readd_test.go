package extensions

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// runningCoolerControl is a rig with CoolerControl added, started and set
// up by h, its daemon running.
func runningCoolerControl(t *testing.T, h Helper) *rig {
	t.Helper()
	r := newRig(t)
	useHelper(t, "coolercontrol", h)
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	r.pass()
	r.boot()
	r.mu.Lock()
	r.running = map[bool][]string{false: {"coolercontrold.service"}}
	r.mu.Unlock()
	return r
}

// holdListUnits makes the next removal's look at what runs wait: it
// returns once a removal is in there, and the func lets it go on.
func holdListUnits(t *testing.T, r *rig, remove string) (release func(), done <-chan int) {
	t.Helper()
	list := r.s.cc.listUnits
	entered, hold := make(chan struct{}), make(chan struct{})
	r.s.cc.listUnits = func(ctx context.Context, user bool, patterns ...string) ([]string, error) {
		close(entered)
		<-hold
		return list(ctx, user, patterns...)
	}
	codes := make(chan int, 1)
	go func() {
		code, _ := r.do("DELETE", remove, "")
		codes <- code
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the removal did not look at the units")
	}
	return func() { close(hold) }, codes
}

// addAgain adds CoolerControl back while a removal holds its helper lock,
// and returns once wanted has it.
func addAgain(t *testing.T, r *rig) <-chan int {
	t.Helper()
	codes := make(chan int, 1)
	go func() {
		code, _ := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`)
		codes <- code
	}()
	waitFor(t, func() bool { return slices.Contains(wantedNow(t), "coolercontrol") })
	return codes
}

// unitCalls is the stops and starts systemctl was asked for.
func (r *rig) unitCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, u := range r.units {
		if strings.HasPrefix(u, "stop ") || strings.HasPrefix(u, "start ") {
			out = append(out, u)
		}
	}
	return out
}

// Added back while its removal stops its units, an extension still mounted
// ends wanted with its units started again: the removal sees it wanted
// once its helper is done, and Install waits for that.
func TestReAddWhileTheRemovalStopsUnits(t *testing.T) {
	h := &recHelper{}
	r := runningCoolerControl(t, h)
	release, removed := holdListUnits(t, r, "/extensions/coolercontrol")
	added := addAgain(t, r)
	release()
	if code := <-removed; code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if code := <-added; code != 200 {
		t.Fatalf("add again: %d", code)
	}
	want := []string{"stop -- coolercontrold.service cc-fans@*.service", "start -- coolercontrold.service"}
	if got := r.unitCalls(); !slices.Equal(got, want) {
		t.Fatalf("systemctl %q, want %q", got, want)
	}
	if !slices.Contains(wantedNow(t), "coolercontrol") {
		t.Fatal("not wanted")
	}
	// The helper undid it, so the next pass sets it up again.
	r.pass()
	if x := r.card("coolercontrol"); x.State != StateInstalled || !x.Mounted || x.Reason != "" || !isInstalled("coolercontrol") {
		t.Fatalf("after the pass = %+v", x)
	}
	if got := h.Calls(); !slices.Equal(got, []string{"install coolercontrol", "remove coolercontrol", "install coolercontrol"}) {
		t.Fatalf("helper calls %q", got)
	}
}

// Added back while a removal with purge runs, an extension keeps its data
// and settings: neither the helper nor vosd deletes them.
func TestReAddDuringAPurgeKeepsTheData(t *testing.T) {
	h := &recHelper{}
	r := runningCoolerControl(t, h)
	data := filepath.Join(r.s.ext("coolercontrol", r.s.desc("coolercontrol")).DataDir, "config.toml")
	writeFile(t, data, "[settings]\n")
	release, removed := holdListUnits(t, r, "/extensions/coolercontrol?purge=1")
	added := addAgain(t, r)
	release()
	if <-removed != 200 || <-added != 200 {
		t.Fatal("remove or add again")
	}
	if !exists(data) || !exists(settingsPath("coolercontrol")) {
		t.Fatal("the purge deleted the data of an extension added back")
	}
	if got := h.Calls(); got[len(got)-1] != "remove coolercontrol" {
		t.Fatalf("helper calls %q: its Remove purged", got)
	}
}

// Purged and then added back before the restart, a mounted extension gets
// its data areas back before its units start and before its helper sets it
// up again.
func TestPurgeThenReAdd(t *testing.T) {
	h := &recHelper{}
	r := runningCoolerControl(t, h)
	dataDir := r.s.ext("coolercontrol", r.s.desc("coolercontrol")).DataDir
	if code, _ := r.do("DELETE", "/extensions/coolercontrol?purge=1", ""); code != 200 {
		t.Fatal("remove")
	}
	if exists(dataDir) || exists(settingsPath("coolercontrol")) {
		t.Fatal("the purge left its data")
	}
	var dataAtStart bool
	systemctl := r.s.cc.systemctl
	r.s.cc.systemctl = func(ctx context.Context, user bool, args ...string) error {
		if len(args) > 0 && args[0] == "start" {
			dataAtStart = exists(dataDir)
		}
		return systemctl(ctx, user, args...)
	}
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("add again: %d %s", code, body)
	}
	if !dataAtStart || !slices.Contains(r.unitCalls(), "start -- coolercontrold.service") {
		t.Fatalf("units %q started, data area there then: %v", r.unitCalls(), dataAtStart)
	}
	// Gone again before the pass: the helper's Install still finds it.
	must(t, os.RemoveAll(dataDir))
	seen := &dataHelper{recHelper: h}
	useHelper(t, "coolercontrol", seen)
	r.pass()
	if !seen.had || !isInstalled("coolercontrol") {
		t.Fatalf("data area there at Install: %v, set up: %v", seen.had, isInstalled("coolercontrol"))
	}
}

// dataHelper notes whether its extension's data area was there when its
// Install ran.
type dataHelper struct {
	*recHelper
	had bool
}

func (h *dataHelper) Install(ctx context.Context, x *Ext) error {
	h.had = exists(x.DataDir)
	return h.recHelper.Install(ctx, x)
}

// A second removal finds the units stopped; adding the extension back
// still starts what the first one stopped.
func TestRemoveTwiceThenReAdd(t *testing.T) {
	r := runningCoolerControl(t, &recHelper{})
	if code, _ := r.do("DELETE", "/extensions/coolercontrol", ""); code != 200 {
		t.Fatal("remove")
	}
	r.mu.Lock()
	r.running = nil
	r.mu.Unlock()
	if code, _ := r.do("DELETE", "/extensions/coolercontrol", ""); code != 200 {
		t.Fatal("remove again")
	}
	if code, _ := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatal("add again")
	}
	if got := r.unitCalls(); got[len(got)-1] != "start -- coolercontrold.service" {
		t.Fatalf("systemctl %q", got)
	}
}
