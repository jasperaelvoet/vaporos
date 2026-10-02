package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// installedRig is a rig with truckersmp added, started and set up by h.
func installedRig(t *testing.T, h *recHelper) *rig {
	t.Helper()
	r := newRig(t)
	useHelper(t, "truckersmp", h)
	if code, body := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	r.pass()
	r.boot()
	if !isInstalled("truckersmp") {
		t.Fatal("not set up after the restart")
	}
	return r
}

// holdNextInstall makes the helper's next Install wait for the returned
// channel to close, and starts it from a pass, returning once it runs.
func holdNextInstall(t *testing.T, r *rig, h *recHelper, stubborn bool) chan struct{} {
	t.Helper()
	must(t, unmarkInstalled("truckersmp"))
	r.s.forget("truckersmp")
	hold := make(chan struct{})
	h.mu.Lock()
	h.hold, h.started, h.stubborn = hold, make(chan struct{}, 1), stubborn
	started := h.started
	h.mu.Unlock()
	r.s.pass(t.Context(), r.b)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the install did not start")
	}
	return hold
}

// A removal while the helper sets the extension up stops that setup and
// waits for it to end before the helper undoes it: the calls never
// overlap, Remove runs once, and no marker stays behind. Meanwhile the
// card says installing and the PC stays awake.
func TestRemoveStopsASetupUnderWay(t *testing.T) {
	h := &recHelper{}
	r := installedRig(t, h)
	holdNextInstall(t, r, h, false)
	if x := r.card("truckersmp"); x.State != StateInstalling || x.Progress != nil {
		t.Fatalf("while its helper sets it up = %+v", x)
	}
	if on, why := r.s.Busy(); !on || why != busyReason {
		t.Fatalf("busy = %v %q while a helper runs", on, why)
	}
	if code, body := r.do("DELETE", "/extensions/truckersmp", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, body)
	}
	r.s.waitInstalls()
	want := []string{"install truckersmp", "install truckersmp", "install truckersmp stopped", "remove truckersmp"}
	if got := h.Calls(); !slices.Equal(got, want) || h.most != 1 {
		t.Fatalf("calls %q (at most %d at once), want %q one at a time", got, h.most, want)
	}
	if isInstalled("truckersmp") {
		t.Fatal("a marker stayed behind")
	}
	if x := r.card("truckersmp"); x.State != StateRestartNeeded || x.Reason != "" {
		t.Fatalf("removed = %+v", x)
	}
	if on, _ := r.s.Busy(); on {
		t.Fatal("still busy")
	}
}

// A setup that does not stop when asked is waited for.
func TestRemoveWaitsForASetup(t *testing.T) {
	h := &recHelper{}
	r := installedRig(t, h)
	hold := holdNextInstall(t, r, h, true)
	done := make(chan int)
	go func() {
		code, _ := r.do("DELETE", "/extensions/truckersmp", "")
		done <- code
	}()
	select {
	case <-done:
		t.Fatal("the removal did not wait for the setup")
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	if code := <-done; code != 200 {
		t.Fatalf("remove: %d", code)
	}
	r.s.waitInstalls()
	if got := h.Calls(); got[len(got)-1] != "remove truckersmp" || h.most != 1 || isInstalled("truckersmp") {
		t.Fatalf("calls %q, at most %d at once, installed %v", got, h.most, isInstalled("truckersmp"))
	}
}

// An extension no longer wanted when its setup ends, without a removal
// to undo it (wanted changed elsewhere), is undone by the install itself.
func TestSetupOfAnExtensionNoLongerWanted(t *testing.T) {
	h := &recHelper{}
	r := installedRig(t, h)
	hold := holdNextInstall(t, r, h, false)
	locked(t, func() error { return store.WriteWanted(nil) })
	close(hold)
	r.s.waitInstalls()
	if got := h.Calls(); got[len(got)-1] != "remove truckersmp" || isInstalled("truckersmp") {
		t.Fatalf("calls %q, installed %v", got, isInstalled("truckersmp"))
	}
	if x := r.card("truckersmp"); x.Reason != "" {
		t.Fatalf("card = %+v", x)
	}
}

// Adds, removals, actions and passes at once never run one extension's
// helper twice at the same time, and settle: added again and passed, it
// is set up.
func TestHelperCallsRace(t *testing.T) {
	h := &recHelper{}
	r := installedRig(t, h)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(3)
		go func() { defer wg.Done(); r.do("DELETE", "/extensions/truckersmp", "") }()
		go func() { defer wg.Done(); r.do("POST", "/extensions/truckersmp", `{}`) }()
		go func() { defer wg.Done(); r.do("POST", "/extensions/truckersmp/actions/copy-profiles", `{}`) }()
	}
	passes := make(chan struct{})
	go func() {
		defer close(passes)
		for range 4 {
			r.s.pass(t.Context(), r.b)
		}
	}()
	wg.Wait()
	<-passes
	r.s.waitInstalls()
	h.mu.Lock()
	most := h.most
	h.mu.Unlock()
	if most != 1 {
		t.Fatalf("%d helper calls ran at once", most)
	}
	if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatal("adding it again")
	}
	r.pass()
	if x := r.card("truckersmp"); x.State != StateInstalled || !isInstalled("truckersmp") {
		t.Fatalf("after it settled = %+v", x)
	}
}

// Adding back an extension removed before the restart starts the units
// its removal stopped, template instances by name.
func TestReAddStartsTheUnitsRemoveStopped(t *testing.T) {
	r := newRig(t)
	useHelper(t, "coolercontrol", &recHelper{})
	if code, _ := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatal("install")
	}
	r.pass()
	r.boot()
	r.mu.Lock()
	r.running = map[bool][]string{false: {"coolercontrold.service", "cc-fans@hwmon2.service", "sshd.service"}}
	r.mu.Unlock()
	if code, _ := r.do("DELETE", "/extensions/coolercontrol", ""); code != 200 {
		t.Fatal("remove")
	}
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("add again: %d %s", code, body)
	}
	// And the firewall opens its port again.
	want := []string{"stop -- coolercontrold.service cc-fans@*.service", "start -- coolercontrold.service cc-fans@hwmon2.service", "reload vos-firewall.service"}
	if !slices.Equal(r.units, want) {
		t.Fatalf("systemctl %q, want %q", r.units, want)
	}
	// Its helper sets it up again, as the removal undid it.
	if x := r.card("coolercontrol"); x.State != StateInstalling {
		t.Fatalf("before the pass = %+v", x)
	}
	r.pass()
	if x := r.card("coolercontrol"); x.State != StateInstalled || !isInstalled("coolercontrol") {
		t.Fatalf("after the pass = %+v", x)
	}
	if code, _ := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 || len(r.units) != len(want) {
		t.Fatalf("adding it once more ran systemctl %q", r.units)
	}
}

// What runs is recorded per scope, and without a list the units that are
// not templates stand in for it.
func TestStopUnitsRecordsBothScopes(t *testing.T) {
	r := newRig(t)
	d := &descriptor.Descriptor{ID: "x", Services: []descriptor.Service{
		{Unit: "a.service", Scope: "system"}, {Unit: "b@.service", Scope: "user"}, {Unit: "c.socket", Scope: "user"}}}
	r.running = map[bool][]string{true: {"b@one.service", "c.socket"}}
	r.s.stopUnits(t.Context(), "x", d)
	r.s.startUnits(t.Context(), "x")
	want := []string{"stop -- a.service", "--user stop -- b@*.service c.socket", "--user start -- b@one.service c.socket"}
	if !slices.Equal(r.units, want) {
		t.Fatalf("systemctl %q, want %q", r.units, want)
	}
	r.units = nil
	r.s.cc.listUnits = func(context.Context, bool, ...string) ([]string, error) { return nil, errors.New("no bus") }
	r.s.stopUnits(t.Context(), "x", d)
	r.s.startUnits(t.Context(), "x")
	r.s.startUnits(t.Context(), "x") // once only
	want = []string{"stop -- a.service", "--user stop -- b@*.service c.socket", "start -- a.service", "--user start -- c.socket"}
	if !slices.Equal(r.units, want) {
		t.Fatalf("systemctl %q, want %q", r.units, want)
	}
}

// The helper undoes only what it may have set up: an extension this boot
// never mounted and that was never set up gets no Remove, while purge
// deletes its data all the same. One set up in an earlier boot does.
func TestRemoveNeverMounted(t *testing.T) {
	r := newRig(t)
	h := &recHelper{}
	useHelper(t, "star-citizen", h)
	if code, _ := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var/mnt/games"}}`); code != 200 {
		t.Fatal("install")
	}
	data := filepath.Join(config.ExtDataDir(), "star-citizen")
	must(t, os.MkdirAll(data, 0o755))
	if code, body := r.do("DELETE", "/extensions/star-citizen?purge=1", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, body)
	}
	if len(h.Calls()) != 0 || len(r.units) != 0 {
		t.Fatalf("helper %q, systemctl %q for one never mounted", h.Calls(), r.units)
	}
	if exists(data) || exists(settingsPath("star-citizen")) {
		t.Fatal("purge left its data or settings")
	}
	if x := r.card("star-citizen"); x.State != StateNotInstalled || x.Reason != "" {
		t.Fatalf("card = %+v", x)
	}

	if code, _ := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var"}}`); code != 200 {
		t.Fatal("install again")
	}
	must(t, markInstalled("star-citizen"))
	if code, _ := r.do("DELETE", "/extensions/star-citizen", ""); code != 200 {
		t.Fatal("remove again")
	}
	if !slices.Equal(h.Calls(), []string{"remove star-citizen"}) || isInstalled("star-citizen") {
		t.Fatalf("calls %q, installed %v", h.Calls(), isInstalled("star-citizen"))
	}

	// A helper that fails says so on the card in plain words, and the
	// removal stands.
	h.removeErr = errors.New("EACCES")
	if code, _ := r.do("POST", "/extensions/star-citizen", `{}`); code != 200 { // its drive is kept
		t.Fatal("install a third time")
	}
	must(t, markInstalled("star-citizen"))
	if code, _ := r.do("DELETE", "/extensions/star-citizen", ""); code != 200 {
		t.Fatal("remove a third time")
	}
	if w := wantedNow(t); slices.Contains(w, "star-citizen") {
		t.Fatal("a failed undo kept it wanted")
	}
	r.s.mu.Lock()
	note := r.s.cc.notes["star-citizen"]
	r.s.mu.Unlock()
	if note != "Removing Star Citizen didn't finish. Try removing it again." {
		t.Fatalf("note %q", note)
	}
}

// An action runs as vapor through `vos ext action` unless its descriptor
// says root, which runs in vosd. The helper's words are the answer when
// it fails.
func TestActionsRunAs(t *testing.T) {
	r := newRig(t)
	h := &recHelper{}
	useHelper(t, "coolercontrol", h)
	useHelper(t, "truckersmp", h)
	for _, id := range []string{"coolercontrol", "truckersmp"} {
		if code, _ := r.do("POST", "/extensions/"+id, `{"password":"`+rigPassword+`"}`); code != 200 {
			t.Fatal("install " + id)
		}
		r.pass()
		r.boot()
	}
	r.gamer = nil
	if code, body := r.do("POST", "/extensions/coolercontrol/actions/restore-fans", `{}`); code != 200 {
		t.Fatalf("root action: %d %s", code, body)
	}
	if len(r.gamer) != 0 || !slices.Contains(h.Calls(), "action coolercontrol restore-fans ") {
		t.Fatalf("root action: as vapor %q, calls %q", r.gamer, h.Calls())
	}
	if code, body := r.do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{"game":"ats"}}`); code != 200 {
		t.Fatalf("vapor action: %d %s", code, body)
	}
	if want := []string{`/usr/bin/vos ext action truckersmp copy-profiles --args {"game":"ats"}`}; !slices.Equal(r.gamer, want) ||
		!slices.Contains(h.Calls(), `action truckersmp copy-profiles {"game":"ats"}`) {
		t.Fatalf("vapor action: as vapor %q, calls %q", r.gamer, h.Calls())
	}
	// The helper does not know "curves": `vos ext action` exits 3.
	if code, body := r.do("POST", "/extensions/coolercontrol/actions/curves", `{}`); code != 404 || !strings.Contains(body, `CoolerControl has no action \"curves\"`) {
		t.Fatalf("unknown to the helper: %d %s", code, body)
	}
	// A failure says which action, in its label; why goes to the log.
	h.actionErr = errors.New("open /var/home/vapor/x: permission denied")
	if code, body := r.do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{"game":"ats"}}`); code != 500 || body != `{"error":"Copy profiles didn't finish. Try again."}` {
		t.Fatalf("failed: %d %s", code, body)
	}
	if code, body := r.do("POST", "/extensions/coolercontrol/actions/restore-fans", `{}`); code != 500 || body != `{"error":"Restore fans didn't finish. Try again."}` {
		t.Fatalf("root action failed: %d %s", code, body)
	}
	if code, body := r.do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{"x":"`+strings.Repeat("a", maxActionArgs)+`"}}`); code != 400 || !strings.Contains(body, "args is too large") {
		t.Fatalf("large args: %d %.80s", code, body)
	}
	if on, _ := r.s.Busy(); on {
		t.Fatal("busy after the actions")
	}
}

func TestParseAction(t *testing.T) {
	for _, c := range []struct {
		args     []string
		id, name string
		raw      string
		ok       bool
	}{
		{[]string{"truckersmp", "copy-profiles"}, "truckersmp", "copy-profiles", "", true},
		{[]string{"truckersmp", "copy-profiles", "--args", `{"a":1}`}, "truckersmp", "copy-profiles", `{"a":1}`, true},
		{[]string{"--args={}", "truckersmp", "copy-profiles"}, "truckersmp", "copy-profiles", `{}`, true},
		{[]string{"truckersmp"}, "", "", "", false},
		{[]string{"Bad_ID", "x"}, "", "", "", false},
		{[]string{"truckersmp", "x", "--args", "[1]"}, "", "", "", false},
		{[]string{"truckersmp", "x", "--args"}, "", "", "", false},
		{[]string{"truckersmp", "x", "--now"}, "", "", "", false},
	} {
		id, name, raw, err := parseAction(c.args)
		if (err == nil) != c.ok || id != c.id || name != c.name || string(raw) != c.raw {
			t.Errorf("parseAction(%q) = %q %q %q %v", c.args, id, name, raw, err)
		}
	}
}

// `vos ext action` refuses an extension that is not running, and a root
// action when it is not root.
func TestActionCLIRefuses(t *testing.T) {
	r := newRig(t)
	var out strings.Builder
	if code := actionCmd([]string{"truckersmp", "copy-profiles"}, &out); code != 1 || !strings.Contains(out.String(), "TruckersMP is not running") {
		t.Fatalf("not mounted: %d %s", code, out.String())
	}
	out.Reset()
	if code := actionCmd([]string{"truckersmp", "fly"}, &out); code != actionExitNoAction {
		t.Fatalf("no such action: %d %s", code, out.String())
	}
	if os.Geteuid() != 0 {
		out.Reset()
		if code := actionCmd([]string{"coolercontrol", "restore-fans"}, &out); code != 1 || !strings.Contains(out.String(), "Restore fans runs as root") {
			t.Fatalf("root action as a user: %d %s", code, out.String())
		}
	}
	_ = r
}

// A descriptor that cannot be read refuses the changes it would check,
// rather than reading as one that needs no password and conflicts with
// nothing.
func TestUnreadableDescriptorRefuses(t *testing.T) {
	r := newRig(t)
	if code, _ := r.do("POST", "/extensions/star-citizen", `{"options":{"disk":"/var"}}`); code != 200 {
		t.Fatal("install")
	}
	broken := func(id string) {
		writeFile(t, filepath.Join(config.ExtDescriptorsDir, id+".json"), "{")
		r.s.cc.descMu.Lock()
		r.s.cc.descs = nil
		r.s.cc.descMu.Unlock()
	}
	broken("coolercontrol")
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/extensions/coolercontrol", `{}`},
		{"PUT", "/extensions/coolercontrol/settings", `{"settings":{}}`},
	} {
		if code, body := r.do(c.method, c.path, c.body); code != 409 || !strings.Contains(body, unreadableText) {
			t.Errorf("%s %s = %d %s", c.method, c.path, code, body)
		}
	}
	broken("star-citizen") // wanted: no add can check its conflicts, nor can it be removed
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/extensions/lact", `{"password":"` + rigPassword + `"}`},
		{"DELETE", "/extensions/star-citizen", ``},
	} {
		if code, body := r.do(c.method, c.path, c.body); code != 409 || !strings.Contains(body, unreadableText) {
			t.Errorf("%s %s = %d %s", c.method, c.path, code, body)
		}
	}
	if w := wantedNow(t); !slices.Equal(w, []string{"star-citizen"}) {
		t.Fatalf("wanted = %v", w)
	}
}

// slowHelper's Action and Remove each say they started and wait for
// release, or for their context.
type slowHelper struct {
	NopHelper
	started chan string
	release chan struct{}
}

func (h *slowHelper) wait(ctx context.Context, call string) error {
	h.started <- call
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *slowHelper) Action(ctx context.Context, x *Ext, name string, _ json.RawMessage) error {
	return h.wait(ctx, "action "+name)
}

func (h *slowHelper) Remove(ctx context.Context, x *Ext, _ bool) error {
	return h.wait(ctx, "remove")
}

// The PC stays awake while an action runs and while a helper undoes its
// extension; an action that runs out of time says so in its label alone.
func TestHelperCallsKeepThePCAwake(t *testing.T) {
	h := &slowHelper{started: make(chan string, 1), release: make(chan struct{})}
	r := runningCoolerControl(t, h)
	busyWhile := func(call string, do func() int) {
		t.Helper()
		done := make(chan int, 1)
		go func() { done <- do() }()
		if got := <-h.started; got != call {
			t.Fatalf("started %q, want %q", got, call)
		}
		if on, why := r.s.Busy(); !on || why != busyReason {
			t.Fatalf("busy = %v %q during %s", on, why, call)
		}
		h.release <- struct{}{}
		if code := <-done; code != 200 {
			t.Fatalf("%s: %d", call, code)
		}
		if on, _ := r.s.Busy(); on {
			t.Fatalf("still busy after %s", call)
		}
	}
	busyWhile("action restore-fans", func() int {
		code, _ := r.do("POST", "/extensions/coolercontrol/actions/restore-fans", `{}`)
		return code
	})

	timeout := helperTimeout
	t.Cleanup(func() { helperTimeout = timeout })
	helperTimeout = 50 * time.Millisecond
	go func() { <-h.started }()
	if code, body := r.do("POST", "/extensions/coolercontrol/actions/restore-fans", `{"args":{"fan":"secret"}}`); code != 500 ||
		body != `{"error":"Restore fans didn't finish in time. Try again."}` {
		t.Fatalf("timed out: %d %s", code, body)
	}
	helperTimeout = timeout

	busyWhile("remove", func() int {
		code, _ := r.do("DELETE", "/extensions/coolercontrol", "")
		return code
	})
}
