package steamui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

var bg = context.Background()

func TestTargets(t *testing.T) {
	f := newFakeSteam(t)
	f.set(func(f *fakeSteam) {
		f.targets = append(f.targets,
			fakeTarget{ID: "../../json/new?http://evil", Title: "SharedJSContext", URL: "https://steamloopback.host/"},
			fakeTarget{ID: strings.Repeat("A", 65), Title: "QuickAccess_uid3"},
			fakeTarget{ID: strings.Repeat("A", 64), Title: "MainMenu_uid3"},
			fakeTarget{ID: "", Title: "SharedJSContext", URL: "https://steamloopback.host/"},
		)
	})
	ts, err := f.client().Targets(bg)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tg := range ts {
		ids = append(ids, tg.ID)
	}
	if want := []string{"BP-1", "SHARED-1", "QA-1", "MM-1", strings.Repeat("A", 64)}; !slices.Equal(ids, want) {
		t.Errorf("ids %q, want %q", ids, want)
	}
	if ts[1] != (Target{ID: "SHARED-1", Type: "page", Title: "SharedJSContext", URL: "https://steamloopback.host/index.html"}) {
		t.Errorf("decoded %+v", ts[1])
	}
	f.set(func(f *fakeSteam) {
		if len(f.origins) != 0 || !slices.Equal(f.hosts, []string{f.addr()}) {
			t.Errorf("origins %q, hosts %q", f.origins, f.hosts)
		}
	})
}

func TestShared(t *testing.T) {
	steam := Target{ID: "S", Title: "SharedJSContext", URL: "https://steamloopback.host/index.html"}
	for _, tc := range []struct {
		name string
		ts   []Target
		want string
	}{
		{"steam's", []Target{{ID: "B", Title: "Steam Big Picture Mode", URL: "https://steamloopback.host/index.html"}, steam}, "S"},
		{"the older title", []Target{{ID: "V", Title: "Steam Shared Context presented by Valve™", URL: "https://steamloopback.host/x"}}, "V"},
		{"a web page with its title", []Target{{ID: "W", Title: "SharedJSContext", URL: "https://store.steampowered.com/"}, steam}, "S"},
		{"a look-alike host", []Target{{ID: "W", Title: "SharedJSContext", URL: "https://steamloopback.host.evil/"}}, ""},
		{"no scheme", []Target{{ID: "W", Title: "SharedJSContext", URL: "steamloopback.host/"}}, ""},
		{"none", []Target{{ID: "Q", Title: "QuickAccess_uid2", URL: "about:blank"}}, ""},
		{"empty", nil, ""},
	} {
		got, ok := Shared(tc.ts)
		if ok != (tc.want != "") || got.ID != tc.want {
			t.Errorf("%s: got %+v, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
	views := Views([]Target{
		{ID: "1", Title: "QuickAccess_uid2"}, {ID: "2", Title: "MainMenu_uid2"}, {ID: "3", Title: "notificationtoasts_uid2"},
		{ID: "4", Title: "SharedJSContext"}, {ID: "5", Title: "quickaccess_uid2"}, {ID: "6", Title: "MainMenu"},
	})
	if len(views) != 2 || views[0].ID != "1" || views[1].ID != "2" {
		t.Errorf("views %+v", views)
	}
}

// /json/list's redirect is never followed, wherever it points.
func TestTargetsRefusesRedirects(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	f := newFakeSteam(t)
	for _, to := range []string{"/json/version", elsewhere.URL + "/upstream"} {
		f.set(func(f *fakeSteam) {
			f.list = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
		})
		if _, err := f.client().Targets(bg); err == nil || !strings.Contains(err.Error(), "302") {
			t.Errorf("redirect to %s: %v", to, err)
		}
	}
	f.set(func(f *fakeSteam) {
		if slices.Contains(f.paths, "/json/version") {
			t.Error("followed the redirect")
		}
	})
	if hits.Load() != 0 {
		t.Error("followed the redirect elsewhere")
	}
}

func TestTargetsRefusesBadAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		list http.HandlerFunc
	}{
		{"over 1 MiB", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("[" + strings.Repeat(" ", maxBody) + "]"))
		}},
		{"an error", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusInternalServerError) }},
		{"not a list", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"id":"SHARED-1"}`)) }},
		{"a wrong type", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"id":5,"title":"SharedJSContext"}]`)) }},
		{"gzip anyway", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Write([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSteam(t)
			f.set(func(f *fakeSteam) { f.list = tc.list })
			if ts, err := f.client().Targets(bg); err == nil {
				t.Fatalf("accepted: %+v", ts)
			}
		})
	}
}

// Nothing is asked of a port whose listener is not Steam's.
func TestNotSteamsListener(t *testing.T) {
	f := newFakeSteam(t)
	f.proc.reset()
	f.proc.process(900, "1000\t1000\t1000\t1000", "python3", 60001)
	f.proc.listen("127.0.0.1", f.port, 60001)
	f.proc.write()
	c := f.client()
	_, err := c.Read(bg)
	var le *ListenerError
	if !errors.Is(err, ErrNoDebugger) || !errors.As(err, &le) || le.Owner.PID != 900 || le.Owner.Comm != "python3" {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Views(bg); !errors.Is(err, ErrNoDebugger) {
		t.Fatalf("views: %v", err)
	}
	if _, err := c.Dial(bg, Target{ID: "SHARED-1"}); !errors.Is(err, ErrNoDebugger) {
		t.Fatalf("dial: %v", err)
	}
	f.set(func(f *fakeSteam) {
		if len(f.paths) != 0 {
			t.Errorf("asked %q", f.paths)
		}
	})
}

func TestDialRefusesBadIDs(t *testing.T) {
	f := newFakeSteam(t)
	c := f.client()
	for _, id := range []string{"", "../json/new", "SHARED-1?x", "SHARED-1/../x", "a b", "é", strings.Repeat("a", 65), "SHARED_1"} {
		if conn, err := c.Dial(bg, Target{ID: id}); err == nil {
			conn.Close()
			t.Errorf("dialled %q", id)
		}
	}
	f.set(func(f *fakeSteam) {
		if f.dials != 0 || len(f.paths) != 0 {
			t.Errorf("dials %d, paths %q", f.dials, f.paths)
		}
	})
}

func TestReadSetAuto(t *testing.T) {
	f := newFakeSteam(t)
	c := f.client()
	defer c.Close()

	st, err := c.Read(bg)
	want := State{Name: `External: VaporOS 27"|||Windowed`, External: true, Auto: true, Current: 1.71, AutoValue: 1.71, Min: 0.71, Max: 3.41}
	if err != nil || st != want {
		t.Fatalf("read: %+v, %v", st, err)
	}

	if v, err := c.Set(bg, 1, 1.3); err != nil || v != 1.3 {
		t.Fatalf("set: %v, %v", v, err)
	}
	st, _ = c.Read(bg)
	if st.Auto || st.Current != 1.3 || st.Gen != 1 || st.Value != 1.3 {
		t.Errorf("after set: %+v", st)
	}

	// Steam's bounds win.
	if v, err := c.Set(bg, 2, 5); err != nil || v != 3.41 {
		t.Errorf("set above max: %v, %v", v, err)
	}
	// The same generation sets again; an older one is stale.
	if v, err := c.Set(bg, 2, 1.5); err != nil || v != 1.5 {
		t.Errorf("set again: %v, %v", v, err)
	}
	if _, err := c.Set(bg, 1, 1.2); !errors.Is(err, ErrStale) {
		t.Errorf("stale set: %v", err)
	}
	if err := c.Auto(bg, 1); !errors.Is(err, ErrStale) {
		t.Errorf("stale auto: %v", err)
	}
	st, _ = c.Read(bg)
	if st.Current != 1.5 || st.Gen != 2 || st.Value != 1.5 {
		t.Errorf("after stale calls: %+v", st)
	}

	if err := c.Auto(bg, 3); err != nil {
		t.Fatal(err)
	}
	st, _ = c.Read(bg)
	if !st.Auto || st.Current != 1.71 || st.Gen != 3 || st.Value != 0 {
		t.Errorf("after auto: %+v", st)
	}

	f.set(func(f *fakeSteam) {
		ui := f.windows["SHARED-1"]
		want := []string{"auto(false)", "manual(1.3)", "auto(false)", "manual(3.41)", "auto(false)", "manual(1.5)", "auto(true)"}
		if !slices.Equal(ui.calls, want) {
			t.Errorf("calls %q, want %q", ui.calls, want)
		}
		// One listing and one connection for all of it, and nothing but
		// Runtime.evaluate on it.
		if f.lists != 1 || f.dials != 1 {
			t.Errorf("lists %d, dials %d", f.lists, f.dials)
		}
		for _, m := range f.methods {
			if m != "Runtime.evaluate" {
				t.Errorf("sent %s", m)
			}
		}
		if len(f.origins) != 0 {
			t.Errorf("origins %q", f.origins)
		}
		if !slices.Contains(f.paths, "/devtools/page/SHARED-1") {
			t.Errorf("paths %q", f.paths)
		}
	})
}

func TestSetRefusesBadNumbers(t *testing.T) {
	f := newFakeSteam(t)
	c := f.client()
	nan := 0.0
	nan /= nan
	for _, s := range []float64{0, -1, 8.01, nan} {
		if _, err := c.Set(bg, 1, s); err == nil {
			t.Errorf("set %v", s)
		}
	}
	for _, gen := range []uint64{0, 1 << 53} {
		if _, err := c.Set(bg, gen, 1); err == nil {
			t.Errorf("set gen %d", gen)
		}
		if err := c.Auto(bg, gen); err == nil {
			t.Errorf("auto gen %d", gen)
		}
	}
	if _, err := c.Set(bg, 1<<53-1, 1); err != nil {
		t.Errorf("the largest generation: %v", err)
	}
	f.set(func(f *fakeSteam) {
		if len(f.methods) != 1 {
			t.Errorf("sent %d requests", len(f.methods))
		}
	})
}

func TestNotReadyAndUnsupported(t *testing.T) {
	f := newFakeSteam(t)
	c := f.client()
	ui := f.ui()

	f.set(func(*fakeSteam) { ui.settings = nil })
	if st, err := c.Read(bg); !errors.Is(err, ErrNotReady) || st.Min != 0.5 || st.Max != 2.5 {
		t.Errorf("starting: %+v, %v", st, err)
	}
	if _, err := c.Set(bg, 1, 1.5); !errors.Is(err, ErrNotReady) {
		t.Errorf("set while starting: %v", err)
	}

	f.set(func(*fakeSteam) {
		ui.settings = readyUI().settings
		ui.settings["strDisplayName"] = `External: xwayland-0 27"|||Windowed`
	})
	if st, err := c.Read(bg); !errors.Is(err, ErrNotReady) || st.Name != `External: xwayland-0 27"|||Windowed` {
		t.Errorf("xwayland: %+v, %v", st, err)
	}

	f.set(func(*fakeSteam) {
		ui.settings = readyUI().settings
		ui.client = "bare"
	})
	if _, err := c.Read(bg); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no setters: %v", err)
	}
	if err := c.Auto(bg, 1); !errors.Is(err, ErrUnsupported) {
		t.Errorf("auto without setters: %v", err)
	}
	f.set(func(*fakeSteam) {
		if len(ui.calls) != 0 {
			t.Errorf("calls %q", ui.calls)
		}
	})
}

func TestNoTarget(t *testing.T) {
	f := newFakeSteam(t)
	f.set(func(f *fakeSteam) { f.targets = f.targets[:1] }) // Big Picture only
	if _, err := f.client().Read(bg); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("got %v", err)
	}
}

// An exception inside Steam keeps the connection.
func TestReadException(t *testing.T) {
	f := newFakeSteam(t)
	c := f.client()
	if _, err := c.Read(bg); err != nil {
		t.Fatal(err)
	}
	var thrown atomic.Bool
	f.set(func(f *fakeSteam) {
		f.hook = func(p *wsPeer, id int64, expr string) bool {
			if expr != readJS || thrown.Swap(true) {
				return false
			}
			p.message([]byte(`{"id":` + strconv.FormatInt(id, 10) + `,"result":{"result":{"type":"object"},"exceptionDetails":{"text":"Uncaught","exception":{"description":"TypeError: settingsStore.settings is a getter"}}}}`))
			return true
		}
	})
	_, err := c.Read(bg)
	var ee *EvalError
	if !errors.As(err, &ee) || !strings.Contains(ee.Text, "TypeError") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Read(bg); err != nil {
		t.Fatal(err)
	}
	f.set(func(f *fakeSteam) {
		if f.dials != 1 {
			t.Errorf("dials %d", f.dials)
		}
	})
}

// A connection that died (Steam restarted) is replaced within the same
// call, by one to the new Steam's target.
func TestReconnects(t *testing.T) {
	f := newFakeSteam(t)
	c := f.client()
	defer c.Close()
	if _, err := c.Read(bg); err != nil {
		t.Fatal(err)
	}

	f.drop()
	if _, err := c.Read(bg); err != nil {
		t.Fatalf("after a drop: %v", err)
	}

	f.boot("2")
	f.proc.reset()
	f.proc.process(800, "1000\t1000\t1000\t1000", "steamwebhelper", 52200)
	f.proc.listen("127.0.0.1", f.port, 52200)
	f.proc.write()
	f.drop()
	if v, err := c.Set(bg, 1, 1.4); err != nil || v != 1.4 {
		t.Fatalf("after a restart: %v, %v", v, err)
	}
	f.set(func(f *fakeSteam) {
		if f.dials != 3 || f.lists != 3 || !slices.Contains(f.paths, "/devtools/page/SHARED-2") {
			t.Errorf("dials %d, lists %d, paths %q", f.dials, f.lists, f.paths)
		}
		if got := f.windows["SHARED-2"].calls; len(got) != 2 {
			t.Errorf("new Steam's calls %q", got)
		}
	})

	// Steam closing the connection after every answer costs a connection
	// per call, never an error.
	f.set(func(f *fakeSteam) { f.closeAfter = 1 })
	for i := 0; i < 3; i++ {
		if _, err := c.Read(bg); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}

	// A connection that fails while new is not tried twice.
	f.set(func(f *fakeSteam) {
		f.closeAfter = 0
		f.hook = func(p *wsPeer, id int64, expr string) bool { p.nc.Close(); return true }
	})
	c.Close()
	before := 0
	f.set(func(f *fakeSteam) { before = f.dials })
	if _, err := c.Read(bg); err == nil {
		t.Fatal("read through a dead connection")
	}
	f.set(func(f *fakeSteam) {
		if f.dials != before+1 {
			t.Errorf("dials %d after %d", f.dials, before)
		}
	})
}

func TestViews(t *testing.T) {
	f := newFakeSteam(t)
	f.set(func(f *fakeSteam) {
		f.windows["MM-1"].dpr = "2" // decoded typed: not a number
	})
	vs, err := f.client().Views(bg)
	if err != nil {
		t.Fatal(err)
	}
	want := []View{
		{ID: "QA-1", Title: "QuickAccess_uid2", DPR: 1.5, Height: 1},
		{ID: "MM-1", Title: "MainMenu_uid2", DPR: 0, Height: 1263},
	}
	if !slices.Equal(vs, want) {
		t.Errorf("views %+v, want %+v", vs, want)
	}
	// A view that is gone by the time it is asked is left out.
	f.set(func(f *fakeSteam) { delete(f.windows, "QA-1") })
	vs, err = f.client().Views(bg)
	if err != nil || len(vs) != 1 || vs[0].ID != "MM-1" {
		t.Errorf("views %+v, %v", vs, err)
	}
	f.set(func(f *fakeSteam) {
		for _, m := range f.methods {
			if m != "Runtime.evaluate" {
				t.Errorf("sent %s", m)
			}
		}
	})
}

// What the display package's scaler needs of a Client, as it would
// declare it.
type scalerUI interface {
	Read(ctx context.Context) (State, error)
	Set(ctx context.Context, gen uint64, scale float64) (float64, error)
	Auto(ctx context.Context, gen uint64) error
	Views(ctx context.Context) ([]View, error)
	Close()
}

var _ scalerUI = (*Client)(nil)

// The zero Client asks Steam's own port on the running system.
func TestZeroClient(t *testing.T) {
	var c Client
	if c.port() != DevtoolsPort || c.procDir() != "/proc" || c.addr() != "127.0.0.1:31911" {
		t.Errorf("%d %s %s", c.port(), c.procDir(), c.addr())
	}
}

func TestClamps(t *testing.T) {
	s := clampString(strings.Repeat("é", 200), 255) // 2 bytes each: the cut falls inside one
	if len(s) != 254 || !utf8.ValidString(s) {
		t.Errorf("cut %d bytes, valid %v", len(s), utf8.ValidString(s))
	}
	if s := clampString("a\xffb", 10); s != "a�b" {
		t.Errorf("invalid UTF-8: %q", s)
	}
	if _, err := (applyResult{Status: "done"}).result(); err == nil {
		t.Error("an unknown status passed")
	}
	for _, tc := range []struct {
		v    float64
		want uint64
	}{{1, 1}, {0, 0}, {-1, 0}, {2.5, 0}, {maxGen, maxGen}, {maxGen + 1, 0}} {
		if got := genOf(&tc.v); got != tc.want {
			t.Errorf("genOf(%v) = %d", tc.v, got)
		}
	}
}
