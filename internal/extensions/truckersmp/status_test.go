package truckersmp

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

func TestLogVersion(t *testing.T) {
	for _, c := range []struct{ log, want string }{
		{"************ : log created on : Friday October 02 2026 @ 18:40:28\r\n" +
			"00:00:00.000 : Euro Truck Simulator 2 init ver.1.61.1.1s (rev. 4a4ce8f7a3d9) win_x64 [Sep 15 2026 18:23:05]\r\n" +
			"00:00:00.010 : [sys] Steam API initialized\r\n", "1.61.1.1s"},
		{"00:00:00.000 : American Truck Simulator init ver.1.53.3.14s (rev. 1) linux_x64\n" +
			"00:12:00.000 : American Truck Simulator init ver.1.61.3.1s (rev. 2) linux_x64\n", "1.61.3.1s"},
		{"00:00:00.000 : init ver.\n", ""},
		{"nothing here\n", ""},
	} {
		if got := logVersion(strings.NewReader(c.log)); got != c.want {
			t.Errorf("%q: %q", c.log, got)
		}
	}
}

// The version comes from the Proton prefix's log, wherever the library
// is, else from the Linux build's.
func TestInstalledVersion(t *testing.T) {
	b := newBox(t)
	ets2, ats := games[0], games[1]
	if v := installedVersion(b.disk, ets2); v != "" {
		t.Errorf("no log: %q", v)
	}
	write(t, filepath.Join(config.GamerHome, nativeDocsRel(ets2), "game.log.txt"), "00:00:00.000 : init ver.1.60.2.3s (rev. x)\n")
	if v := installedVersion(b.disk, ets2); v != "1.60.2.3s" {
		t.Errorf("the Linux build's log: %q", v)
	}
	b.log(b.disk, ets2, "1.61.1.1s")
	if v := installedVersion(b.disk, ets2); v != "1.61.1.1s" {
		t.Errorf("the prefix's log on a disk: %q", v)
	}
	b.log(b.steam, ats, "1.61.3.1s")
	if v := installedVersion(b.steam, ats); v != "1.61.3.1s" {
		t.Errorf("the prefix's log in the home: %q", v)
	}
}

func TestVersionLine(t *testing.T) {
	ets2 := games[0]
	for _, c := range []struct {
		version, supported, held string
		tone, text               string
	}{
		{"1.61.1.1s", "1.61.1.1s", "", "", "ETS2 1.61.1.1s works with TruckersMP."},
		{"1.62.0.5s", "1.61.1.1s", "", "warning", "ETS2 1.62.0.5s is newer than TruckersMP supports. Switch to the supported version."},
		{"1.60.3.1s", "1.61.1.1s", "", "warning", "ETS2 1.60.3.1s is older than TruckersMP supports. Update it in Steam."},
		{"1.61.1.1s", "", "", "", "ETS2 runs version 1.61.1.1s."},
		{"", "1.61.1.1s", "", "", ""},
		{"1.61.1.1s", "1.61.1.1s", "temporary_1_61", "", "ETS2 stays on version 1.61 for TruckersMP."},
		{"1.62.0.5s", "1.61.1.1s", "temporary_1_61", "", "ETS2 switches to version 1.61 when Steam restarts."},
		{"1.61.1.1s", "1.62.0.1s", "temporary_1_61", "warning", "TruckersMP now supports ETS2 1.62. Switch to the latest version."},
	} {
		tone, text := versionLine(gameState{g: ets2, lib: "/x", version: c.version}, c.supported, c.held)
		if tone != c.tone || text != c.text {
			t.Errorf("%+v: %q %q", c, tone, text)
		}
	}
}

// statusHelper is a helper on a box whose extension is installed, with
// the version API's answer and the sync both fakes.
type statusHelper struct {
	*Helper
	mu      sync.Mutex
	syncs   int
	release chan struct{} // closes to end a sync
	answer  *versionInfo
}

func newStatusHelper(t *testing.T) *statusHelper {
	s := &statusHelper{Helper: newHelper(), release: make(chan struct{})}
	s.answer = &versionInfo{Name: "0.7.7.9", SupportedETS2: "1.61.1.1s", SupportedATS: "1.61.3.1s"}
	s.active = func() bool { return true }
	s.fetchAPI = func(context.Context) (*versionInfo, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.answer, nil
	}
	s.runSync = func(ctx context.Context, progress func(done, total int64)) error {
		s.mu.Lock()
		s.syncs++
		s.mu.Unlock()
		progress(0, 0)
		progress(250, 1000)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	t.Cleanup(func() { s.stopSync(time.Second) })
	return s
}

func (s *statusHelper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncs
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *statusHelper) waitIdle(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool {
		s.Helper.mu.Lock()
		defer s.Helper.mu.Unlock()
		return s.job == nil
	})
}

func (s *statusHelper) waitAPI(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool {
		s.Helper.mu.Lock()
		defer s.Helper.mu.Unlock()
		return s.Helper.api != nil && !s.asking
	})
}

func texts(lines []extensions.StatusLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Tone+"|"+l.Text)
	}
	return out
}

func testExt() *extensions.Ext {
	return &extensions.Ext{ID: ID, DataDir: filepath.Join(config.ExtDataDir(), ID), HomeDir: homeDir()}
}

// The card follows a sync from the first status poll: it asks the API,
// starts the sync, shows its progress and then the versions.
func TestStatus(t *testing.T) {
	b := newBox(t)
	b.install(b.disk, games[0])
	b.log(b.disk, games[0], "1.62.0.5s")
	h := newStatusHelper(t)
	x := testExt()

	h.Status(context.Background(), x)
	h.waitAPI(t)
	waitFor(t, func() bool {
		h.Helper.mu.Lock()
		defer h.Helper.mu.Unlock()
		return h.job != nil && h.job.total == 1000
	})
	if h.count() != 1 {
		t.Fatalf("syncs %d", h.count())
	}
	lines := h.Status(context.Background(), x)
	want := []string{
		"|TruckersMP 0.7.7.9 supports ETS2 1.61.1.1s and ATS 1.61.3.1s.",
		"warning|ETS2 1.62.0.5s is newer than TruckersMP supports. Switch to the supported version.",
		"|Downloading the TruckersMP mod: 25 %",
	}
	if !slices.Equal(texts(lines), want) {
		t.Errorf("while syncing:\n%q\nwant\n%q", texts(lines), want)
	}
	if busy, why := h.Busy(); !busy || why != busyReason {
		t.Error("not busy while downloading")
	}

	// Done: the sync's manifest says the files are ready.
	write(t, filepath.Join(homeDir(), manifestRel), `{"version":"0.7.7.9","checked":"`+time.Now().UTC().Format(time.RFC3339)+`","games":["ets2"],"files":[]}`)
	close(h.release)
	h.waitIdle(t)
	if busy, _ := h.Busy(); busy {
		t.Error("busy after the sync")
	}
	lines = h.Status(context.Background(), x)
	if got := texts(lines)[2]; got != "|TruckersMP 0.7.7.9 is ready." {
		t.Errorf("after: %q", texts(lines))
	}
	if h.count() != 1 {
		t.Errorf("synced again: %d", h.count())
	}
}

// When a sync is due: none while not installed; one when there is no
// manifest, when TruckersMP's version changed, when a game is installed
// that the files lack and once a day; never two within syncGap.
func TestSchedule(t *testing.T) {
	b := newBox(t)
	b.install(b.disk, games[0])
	h := newStatusHelper(t)
	close(h.release)
	fresh := &manifest{Version: "0.7.7.9", Checked: time.Now(), Games: []string{"ets2"}}
	gs := h.games()

	h.active = func() bool { return false }
	if lines := h.Status(context.Background(), testExt()); lines != nil || h.count() != 0 {
		t.Fatalf("not installed: lines %q, syncs %d", texts(lines), h.count())
	}
	h.active = func() bool { return true }

	h.schedule(nil, gs)
	h.waitIdle(t)
	if h.count() != 1 {
		t.Fatalf("no manifest: %d", h.count())
	}
	h.waitAPI(t)
	h.schedule(nil, gs)
	if h.count() != 1 {
		t.Fatal("synced again within the gap")
	}
	for name, c := range map[string]struct {
		m    *manifest
		gs   []gameState
		want bool
	}{
		"up to date":    {fresh, gs, false},
		"new version":   {&manifest{Version: "0.7.7.8", Checked: time.Now(), Games: []string{"ets2"}}, gs, true},
		"a day old":     {&manifest{Version: "0.7.7.9", Checked: time.Now().Add(-25 * time.Hour), Games: []string{"ets2"}}, gs, true},
		"ATS installed": {fresh, []gameState{{g: games[0], lib: b.disk}, {g: games[1], lib: b.steam}}, true},
	} {
		n := h.count()
		h.Helper.mu.Lock()
		h.lastRun = time.Now().Add(-syncGap - time.Second)
		h.Helper.mu.Unlock()
		h.schedule(c.m, c.gs)
		h.waitIdle(t)
		if got := h.count() > n; got != c.want {
			t.Errorf("%s: synced %v", name, got)
		}
	}
}

func TestReadProgress(t *testing.T) {
	var got []int64
	readProgress(strings.NewReader("{\"bytes\":0,\"total\":0}\nnoise\n{\"bytes\":5,\"total\":10}\n{\"bytes\":-1,\"total\":3}\n"),
		func(done, total int64) { got = append(got, done, total) })
	if !slices.Equal(got, []int64{0, 0, 5, 10}) {
		t.Errorf("%v", got)
	}
}
