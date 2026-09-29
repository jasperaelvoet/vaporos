package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// call runs one handler directly: the api package's access control is not
// what these tests are about.
func call(t *testing.T, h http.HandlerFunc, method, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/update", strings.NewReader(body))
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response %q: %v", w.Body.String(), err)
	}
	return w.Code, out
}

func waitIdle(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if busy, _ := s.Busy(); !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the update never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceStageAndActivate(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 200<<10, nil)
	s := NewService(e.cfg(e.srcDir(img)))
	rebooted := make(chan struct{}, 1)
	s.reboot = func(context.Context) error { rebooted <- struct{}{}; return nil }
	oldDelay := rebootDelay
	rebootDelay = 0
	defer func() { rebootDelay = oldDelay }()

	if code, out := call(t, s.handleActivate, "POST", ""); code != http.StatusConflict {
		t.Fatalf("activate with nothing staged: %d %v", code, out)
	}
	if code, out := call(t, s.handleCheck, "POST", ""); code != 200 || out["available"].(map[string]any)["version"] != newVersion {
		t.Fatalf("check: %d %v", code, out)
	}
	if code, out := call(t, s.handleGet, "GET", ""); code != 200 || out["next_boot"] != nil {
		t.Fatalf("next_boot before the stage: %d %v", code, out["next_boot"])
	}

	evs, cancel := events.Default.Subscribe()
	defer cancel()
	if code, out := call(t, s.handleStage, "POST", ""); code != 200 {
		t.Fatalf("stage: %d %v", code, out)
	}
	waitIdle(t, s)
	e.checkStaged(img)
	sawDone := false
	for len(evs) > 0 {
		ev := <-evs
		if ev.Topic == "update.progress" && strings.Contains(string(ev.Data), `"phase":"done"`) {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("no update.progress done event")
	}

	code, out := call(t, s.handleGet, "GET", "")
	if code != 200 || out["staged"].(map[string]any)["version"] != newVersion || out["booted"] != bootedVersion ||
		out["booted_slot"] != "a" || out["other_slot"].(map[string]any)["version"] != newVersion ||
		out["config"].(map[string]any)["channel"] != "main" || out["busy"] != false {
		t.Fatalf("get: %d %v", code, out)
	}
	if nb, _ := out["next_boot"].(map[string]any); nb["slot"] != "b" || nb["version"] != newVersion {
		t.Fatalf("next_boot after the stage: %v", out["next_boot"])
	}

	if code, out := call(t, s.handleActivate, "POST", ""); code != 200 {
		t.Fatalf("activate: %d %v", code, out)
	}
	select {
	case <-rebooted:
	case <-time.After(5 * time.Second):
		t.Fatal("activate did not reboot")
	}
}

func TestServiceStageErrors(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	s := NewService(e.cfg(e.srcDir(img)))
	if code, _ := call(t, s.handleStage, "POST", `{"version":"../x"}`); code != http.StatusBadRequest {
		t.Fatalf("bad version: %d", code)
	}
	if code, _ := call(t, s.handleStage, "POST", `{nope`); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
	// One at a time.
	if !s.reserve() {
		t.Fatal("reserve")
	}
	if code, _ := call(t, s.handleStage, "POST", `{}`); code != http.StatusConflict {
		t.Fatalf("while running: %d", code)
	}
	if busy, why := s.Busy(); !busy || why == "" {
		t.Fatalf("Busy() = %v %q", busy, why)
	}
	s.running = false

	// A `vos update` in another process holds the lock.
	l, err := lockFile(updateLockPath(), false)
	if err != nil {
		t.Fatal(err)
	}
	if busy, _ := s.Busy(); !busy {
		t.Fatal("Busy() missed the lock")
	}
	if code, _ := call(t, s.handleStage, "POST", `{}`); code != http.StatusConflict {
		t.Fatalf("while locked: %d", code)
	}
	l.Unlock()

	// A failing stage reports an error event and last_error.
	evs, cancel := events.Default.Subscribe()
	defer cancel()
	bad := e.makeImage(newVersion, 200, slotSize+10, nil)
	s = NewService(e.cfg(e.srcDir(bad)))
	if code, _ := call(t, s.handleStage, "POST", ""); code != 200 {
		t.Fatalf("stage: %d", code)
	}
	waitIdle(t, s)
	sawError := false
	for len(evs) > 0 {
		ev := <-evs
		if ev.Topic == "update.progress" && strings.Contains(string(ev.Data), `"phase":"error"`) {
			sawError = true
		}
	}
	if !sawError || !strings.Contains(e.state().LastError, "does not fit") {
		t.Fatalf("error event %v, state %+v", sawError, e.state())
	}
}

// lastProgress is the update.progress a page that opens now gets replayed.
func lastProgress(t *testing.T) Progress {
	t.Helper()
	for _, ev := range events.Default.Last() {
		if ev.Topic == "update.progress" {
			var p Progress
			if err := json.Unmarshal(ev.Data, &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	t.Fatal("no update.progress to replay")
	return Progress{}
}

func TestBenignStageEndsWithIdle(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	s := NewService(e.cfg(e.srcDir(img)))
	if err := s.stageNow(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if p := lastProgress(t); p.Phase != "done" {
		t.Fatalf("after staging: %+v", p)
	}
	// The 6-hourly check finds the staged version again.
	if err := s.stageNow(context.Background(), Options{}); !errors.Is(err, ErrAlreadyStaged) {
		t.Fatalf("second stage: %v", err)
	}
	if p := lastProgress(t); p.Phase != "idle" || p.Version != newVersion || p.Error != "" {
		t.Fatalf("already staged: %+v", p)
	}

	same := e.makeImage(bootedVersion, bootedRollback, 1000, nil)
	s = NewService(e.cfg(e.srcDir(same)))
	if err := s.stageNow(context.Background(), Options{}); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("up to date: %v", err)
	}
	if p := lastProgress(t); p.Phase != "idle" || p.Version != bootedVersion {
		t.Fatalf("up to date: %+v", p)
	}
	if st := e.state(); st.LastError != "" {
		t.Fatalf("last_error %q", st.LastError)
	}
}

func TestServiceSettings(t *testing.T) {
	e := setup(t)
	s := NewService(e.cfg(config.DefaultUpdateSrc))
	e.setState(&State{Available: &Available{Version: newVersion}})
	for _, body := range []string{`{"auto":"always"}`, `{"channel":"a/b"}`, `nope`} {
		if code, _ := call(t, s.handleSettings, "PUT", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, out := call(t, s.handleSettings, "PUT", `{"channel":"dev-tooling","auto":"off"}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	saved, err := config.Load()
	if err != nil || saved.Update.Channel != "dev-tooling" || saved.Update.Auto != "off" {
		t.Fatalf("saved %+v %v", saved.Update, err)
	}
	if st := e.state(); st.Available != nil {
		t.Fatal("available from the old channel kept")
	}
	// Only auto: the channel stays.
	if code, _ := call(t, s.handleSettings, "PUT", `{"auto":"stage"}`); code != 200 {
		t.Fatal(code)
	}
	if saved, _ := config.Load(); saved.Update.Channel != "dev-tooling" || saved.Update.Auto != "stage" {
		t.Fatalf("saved %+v", saved.Update)
	}
}

func TestServiceRollback(t *testing.T) {
	e := setup(t)
	s := NewService(e.cfg(config.DefaultUpdateSrc))
	if code, out := call(t, s.handleRollback, "POST", ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if a := e.entry("a"); a.Bootable() {
		t.Fatal("slot a still preferred")
	}
	if _, out := call(t, s.handleGet, "GET", ""); out["next_boot"] == nil ||
		out["next_boot"].(map[string]any)["slot"] != "b" || out["next_boot"].(map[string]any)["version"] != oldIdleVersion {
		t.Fatalf("next_boot after the rollback: %v", out["next_boot"])
	}
	e.setState(&State{Failed: []string{bootedVersion}})
	e.bootSlot("b")
	if code, _ := call(t, s.handleRollback, "POST", ""); code != http.StatusConflict {
		t.Fatalf("rollback to a failed version: %d", code)
	}
}

func TestRunAutoStages(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	s := NewService(e.cfg(e.srcDir(img)))
	oldFirst, oldJitter := firstCheckDelay, checkJitter
	firstCheckDelay, checkJitter = 10*time.Millisecond, 0
	defer func() { firstCheckDelay, checkJitter = oldFirst, oldJitter }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for e.state().Staged == nil {
		if time.Now().After(deadline) {
			t.Fatalf("nothing staged: %+v", e.state())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	e.checkStaged(img)

	// auto = off: Run only does the bookkeeping.
	e = setup(t)
	c := e.cfg(e.srcDir(img))
	c.Update.Auto = "off"
	s = NewService(c)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if st := e.state(); st.Staged != nil || st.Checked != "" {
		t.Fatalf("auto=off staged or checked: %+v", st)
	}
}
