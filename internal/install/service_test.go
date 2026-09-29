package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

type published struct {
	mu     sync.Mutex
	events []progressEvent
}

func (p *published) publish(topic string, data any) {
	if topic != "install.progress" {
		return
	}
	p.mu.Lock()
	p.events = append(p.events, data.(progressEvent))
	p.mu.Unlock()
}

func (p *published) list() []progressEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]progressEvent{}, p.events...)
}

func serviceMachine(t *testing.T) (*fakeSys, *Service, *published) {
	f, _ := installMachine(t)
	s := NewService(config.Defaults())
	s.env = f.env(f.runner())
	// Never the real reboot: TestRoutes goes through the live handler.
	s.reboot = func(context.Context) error { return nil }
	pub := &published{}
	s.publish = pub.publish
	return f, s, pub
}

func do(t *testing.T, h http.HandlerFunc, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/install", strings.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func status(t *testing.T, s *Service) Status {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/install/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)
	var st Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func waitFor(t *testing.T, s *Service, ok func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := status(t, s); ok(st) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("status never reached the expected state: %+v", status(t, s))
	return Status{}
}

func waitState(t *testing.T, s *Service, state string) Status {
	t.Helper()
	return waitFor(t, s, func(st Status) bool { return st.State == state })
}

func TestServiceJob(t *testing.T) {
	f, s, pub := serviceMachine(t)
	release := make(chan struct{})
	var got Options
	s.install = func(ctx context.Context, opts Options, p Progress) error {
		got = opts
		p(StepProbe, 2, "Ready")
		p(StepWrite, 40, "Writing slot a")
		<-release
		p(StepDone, 100, "VaporOS 1 is installed on /dev/sda")
		return nil
	}
	rebooted := make(chan struct{}, 1)
	s.reboot = func(context.Context) error { rebooted <- struct{}{}; return nil }

	if st := status(t, s); st.State != StateIdle {
		t.Fatalf("initial state %+v", st)
	}
	code, out := do(t, s.handleInstall, "POST", `{"disk":"/dev/sda","hostname":"Den","password":"hunter2hunter2","timezone":"Europe/Brussels","libraries":["x-1"]}`)
	if code != http.StatusAccepted || len(out["job"].(string)) != 16 {
		t.Fatalf("POST /install = %d %v", code, out)
	}
	// One job at a time; no reboot in the middle of it.
	if code, _ := do(t, s.handleInstall, "POST", `{"disk":"/dev/sda"}`); code != http.StatusConflict {
		t.Errorf("second POST = %d", code)
	}
	if code, _ := do(t, s.handleReboot, "POST", ``); code != http.StatusConflict {
		t.Errorf("reboot while running = %d", code)
	}
	st := waitFor(t, s, func(st Status) bool { return st.State == StateRunning && st.Percent == 40 })
	if st.Step != StepWrite || st.Message != "Writing slot a" {
		t.Errorf("status = %+v", st)
	}

	close(release)
	st = waitState(t, s, StateDone)
	if st.Percent != 100 || st.Step != StepDone || st.Error != "" {
		t.Errorf("done status = %+v", st)
	}
	if got.Hostname != "den" || got.Mode != ModeErase || got.Password != "hunter2hunter2" {
		t.Errorf("job options = %+v", got)
	}

	evs := pub.list()
	if len(evs) < 4 || evs[0].State != StateRunning || evs[len(evs)-1].State != StateDone || evs[len(evs)-1].Percent != 100 {
		t.Errorf("events = %+v", evs)
	}
	for _, ev := range evs {
		if strings.Contains(ev.Message, "hunter2") {
			t.Error("password leaked into an event")
		}
	}
	if serial := readFile(t, f.path("dev/ttyS0")); serial != "VOS-INSTALL state=done message=VaporOS 1 is installed on /dev/sda\n" {
		t.Errorf("serial = %q", serial)
	}

	s.rebootDelay = 0
	if code, _ := do(t, s.handleReboot, "POST", ``); code != http.StatusOK {
		t.Errorf("reboot = %d", code)
	}
	select {
	case <-rebooted:
	case <-time.After(5 * time.Second):
		t.Error("reboot not called")
	}
}

func TestServiceJobFails(t *testing.T) {
	f, s, pub := serviceMachine(t)
	s.install = func(ctx context.Context, opts Options, p Progress) error {
		p(StepPartition, 5, "Creating partitions")
		return errors.New("partition: sgdisk failed\nexit status 4")
	}
	if code, _ := do(t, s.handleInstall, "POST", `{"disk":"sda"}`); code != http.StatusAccepted {
		t.Fatal(code)
	}
	st := waitState(t, s, StateFailed)
	if st.Error != "partition: sgdisk failed\nexit status 4" || st.Step != StepPartition || st.Percent != 5 {
		t.Errorf("status = %+v", st)
	}
	if last := pub.list()[len(pub.list())-1]; last.State != StateFailed {
		t.Errorf("last event = %+v", last)
	}
	if serial := readFile(t, f.path("dev/ttyS0")); serial != "VOS-INSTALL state=failed message=Installation failed: partition: sgdisk failed exit status 4\n" {
		t.Errorf("serial = %q", serial)
	}
	// A failed install can be retried.
	s.install = func(ctx context.Context, opts Options, p Progress) error { return nil }
	if code, _ := do(t, s.handleInstall, "POST", `{"disk":"sda"}`); code != http.StatusAccepted {
		t.Errorf("retry = %d", code)
	}
	waitState(t, s, StateDone)
}

func TestServiceValidation(t *testing.T) {
	_, s, _ := serviceMachine(t)
	s.install = func(ctx context.Context, opts Options, p Progress) error {
		t.Error("install started for an invalid request")
		return nil
	}
	for body, want := range map[string]string{
		`not json`:                            "bad request body",
		`{}`:                                  "no disk",
		`{"disk":"sdb"}`:                      "holds the VaporOS installer",
		`{"disk":"/dev/sda1"}`:                "is a partition",
		`{"disk":"sda","hostname":"a b"}`:     "invalid hostname",
		`{"disk":"sda","password":"short"}`:   "at least 8",
		`{"disk":"sda","mode":"repair"}`:      "no VaporOS installation",
		`{"disk":"sda","source":"oci://x/y"}`: "not supported",
		`{"disk":"sda","timezone":"../../x"}`: "invalid timezone",
	} {
		code, out := do(t, s.handleInstall, "POST", body)
		if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), want) {
			t.Errorf("%s -> %d %v, want 400 %q", body, code, out, want)
		}
	}
	if st := status(t, s); st.State != StateIdle {
		t.Errorf("state = %+v", st)
	}
}

func TestServiceRealInstall(t *testing.T) {
	// The default install func runs the real installer with the service's env.
	f, s, pub := serviceMachine(t)
	if code, out := do(t, s.handleInstall, "POST", `{"disk":"sda","password":"12345678"}`); code != http.StatusAccepted {
		t.Fatal(code, out)
	}
	st := waitState(t, s, StateDone)
	if !strings.HasPrefix(st.Message, "VaporOS 20260929.123456 is installed on /dev/sda") {
		t.Errorf("status = %+v", st)
	}
	var steps []string
	for _, ev := range pub.list() {
		if len(steps) == 0 || steps[len(steps)-1] != ev.Step {
			steps = append(steps, ev.Step)
		}
	}
	if got := strings.Join(steps, ","); got != "probe,partition,write,verify,bootloader,configure,done" {
		t.Errorf("event steps = %s", got)
	}
	if f.adminPass != "12345678" {
		t.Error("password not set")
	}
}

func TestServiceProbeHandler(t *testing.T) {
	_, s, _ := serviceMachine(t)
	req := httptest.NewRequest("GET", "/api/v1/install/probe", nil)
	w := httptest.NewRecorder()
	s.handleProbe(w, req)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, k := range []string{"disks", "ips", "timezone", "gpu"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("probe has no %q", k)
		}
	}
	var disks []map[string]json.RawMessage
	json.Unmarshal(raw["disks"], &disks)
	for _, k := range []string{"path", "model", "size", "transport", "removable", "is_live", "has_vaporos", "steam_libraries"} {
		if _, ok := disks[0][k]; !ok {
			t.Errorf("disk has no %q", k)
		}
	}
}

func TestRoutes(t *testing.T) {
	_, s, _ := serviceMachine(t)
	srv := api.New(api.Options{Installer: true})
	s.Routes(srv)
	h := srv.Handler()
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/v1/install/probe"}, {"POST", "/api/v1/install"},
		{"GET", "/api/v1/install/status"}, {"POST", "/api/v1/install/reboot"},
	} {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewReader(nil))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s not registered (%d)", rt.method, rt.path, w.Code)
		}
	}
}
