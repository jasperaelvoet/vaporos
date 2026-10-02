package extensions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// webRig is a box running CoolerControl, whose server is an httptest
// upstream, with the proxy's listener on a loopback port of the test's
// and the unit states in states.
type webRig struct {
	*rig
	upstream *httptest.Server
	seen     chan *http.Request // what the upstream got

	mu     sync.Mutex
	states map[string]string
	addrs  map[int]string // proxied port -> the test listener's address
}

func newWebRig(t *testing.T) *webRig {
	t.Helper()
	w := &webRig{rig: newRig(t), seen: make(chan *http.Request, 16),
		states: map[string]string{"coolercontrold.service": "active"}, addrs: map[int]string{}}
	w.upstream = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.seen <- r.Clone(context.Background())
		switch r.URL.Path {
		case "/sse":
			rw.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(rw, "data: 1\n\n")
			rw.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.SetCookie(rw, &http.Cookie{Name: "vos_session", Value: "planted"})
			http.SetCookie(rw, &http.Cookie{Name: "cc", Value: "theirs"})
			fmt.Fprint(rw, "coolercontrol")
		}
	}))
	t.Cleanup(w.upstream.Close)
	up := strings.TrimPrefix(w.upstream.URL, "http://")
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "coolercontrol.json"), withPorts(t, shipped["coolercontrol"],
		`[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"`+up+`"}]`))
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	w.report(bootWith(w.rig, "proton", "coolercontrol"))
	w.s.web.state = func(_ context.Context, unit string, user bool) string {
		w.mu.Lock()
		defer w.mu.Unlock()
		if user {
			return ""
		}
		return w.states[unit]
	}
	w.s.web.listen = func(port int) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			w.mu.Lock()
			w.addrs[port] = ln.Addr().String()
			w.mu.Unlock()
		}
		return ln, err
	}
	w.s.SetWebGuard(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-Test-Signed-In") != "1" {
			http.Redirect(rw, r, "http://vapor.local/login", http.StatusSeeOther)
			return false
		}
		return true
	})
	return w
}

func (w *webRig) set(unit, state string) {
	w.mu.Lock()
	w.states[unit] = state
	w.mu.Unlock()
}

func (w *webRig) addr() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addrs[11987]
}

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       5 * time.Second,
}

func (w *webRig) get(path string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequest("GET", "http://"+w.addr()+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return noRedirect.Do(req)
}

func TestProxyServesWhileTheServiceRuns(t *testing.T) {
	w := newWebRig(t)
	servers := map[int]*webServer{}
	t.Cleanup(func() {
		for _, s := range servers {
			s.close()
		}
	})
	if w.card("coolercontrol").WebRunning {
		t.Fatal("web_running before vosd serves the port")
	}
	w.s.serveWeb(t.Context(), servers)
	if servers[11987] == nil {
		t.Fatal("not serving 11987 while coolercontrold is active")
	}
	if !w.card("coolercontrol").WebRunning || w.card("lact").WebRunning {
		t.Fatal("the card does not say its web UI is served")
	}

	resp, err := w.get("/", nil)
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || len(w.seen) != 0 {
		t.Fatalf("signed out: %d, upstream saw %d requests", resp.StatusCode, len(w.seen))
	}

	resp, err = w.get("/dashboard?x=1", map[string]string{
		"X-Test-Signed-In": "1", "X-VOS-CSRF": "token", "X-Vos-Passive": "1", "X-Forwarded-For": "203.0.113.9",
		"Cookie": "vos_session=secret; cc=1; vos_setup=code; other=2",
	})
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "coolercontrol" {
		t.Fatalf("signed in: %d %q", resp.StatusCode, body)
	}
	got := <-w.seen
	if got.URL.RequestURI() != "/dashboard?x=1" || got.Host != w.addr() {
		t.Errorf("upstream got %s for host %s, want /dashboard?x=1 for %s", got.URL.RequestURI(), got.Host, w.addr())
	}
	for k := range got.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-vos-") {
			t.Errorf("upstream got vosd's header %s", k)
		}
	}
	if c := got.Header.Get("Cookie"); c != "cc=1; other=2" {
		t.Errorf("upstream got cookies %q, want only its own", c)
	}
	if xff := got.Header.Get("X-Forwarded-For"); xff != "127.0.0.1" {
		t.Errorf("X-Forwarded-For %q: the client's own must not pass", xff)
	}
	if got.Header.Get("X-Forwarded-Host") != w.addr() || got.Header.Get("X-Forwarded-Proto") != "http" {
		t.Errorf("X-Forwarded-Host %q, -Proto %q", got.Header.Get("X-Forwarded-Host"), got.Header.Get("X-Forwarded-Proto"))
	}
	var names []string
	for _, c := range resp.Cookies() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "cc" {
		t.Errorf("cookies set through the proxy: %v, want only cc", names)
	}
	if resp.Header.Get("Content-Security-Policy") != "" {
		t.Error("the proxy added vosd's CSP")
	}

	// A restart keeps the port; a stop closes it.
	w.set("coolercontrold.service", "activating")
	w.s.serveWeb(t.Context(), servers)
	if servers[11987] == nil {
		t.Fatal("stopped serving during a restart")
	}
	w.set("coolercontrold.service", "inactive")
	w.s.serveWeb(t.Context(), servers)
	if servers[11987] != nil || w.card("coolercontrol").WebRunning {
		t.Fatal("still serving with coolercontrold stopped")
	}
	if _, err := w.get("/", nil); err == nil {
		t.Fatal("the port still answers")
	}
	w.set("coolercontrold.service", "activating")
	w.s.serveWeb(t.Context(), servers)
	if servers[11987] != nil {
		t.Fatal("serving a service that has not started yet")
	}
}

func TestProxyStreamsEvents(t *testing.T) {
	w := newWebRig(t)
	servers := map[int]*webServer{}
	t.Cleanup(func() {
		for _, s := range servers {
			s.close()
		}
	})
	w.s.serveWeb(t.Context(), servers)
	resp, err := w.get("/sse", map[string]string{"X-Test-Signed-In": "1", "Accept": "text/event-stream"})
	must(t, err)
	defer resp.Body.Close()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "data: 1\n" {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the event did not come through before the stream ended")
	}
}

// Removing the extension stops its page at once; a bind failure is
// retried and never stops anything.
func TestProxyFollowsWantedAndSurvivesBindFailure(t *testing.T) {
	w := newWebRig(t)
	servers := map[int]*webServer{}
	t.Cleanup(func() {
		for _, s := range servers {
			s.close()
		}
	})
	listen := w.s.web.listen
	w.s.web.listen = func(int) (net.Listener, error) { return nil, errors.New("address already in use") }
	w.s.serveWeb(t.Context(), servers)
	w.s.serveWeb(t.Context(), servers)
	if len(servers) != 0 {
		t.Fatal("serving without a listener")
	}
	w.s.web.listen = listen
	w.s.serveWeb(t.Context(), servers)
	if servers[11987] == nil {
		t.Fatal("not serving once the port is free")
	}
	locked(t, func() error { return store.WriteWanted(nil) })
	w.s.serveWeb(t.Context(), servers)
	if len(servers) != 0 {
		t.Fatal("still serving a removed extension's page")
	}
}

func TestProxyWithoutGuardRefuses(t *testing.T) {
	w := newWebRig(t)
	w.s.SetWebGuard(nil)
	rec := httptest.NewRecorder()
	w.s.proxyHandler(exposed{id: "coolercontrol", name: "CoolerControl", upstream: strings.TrimPrefix(w.upstream.URL, "http://")}).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusServiceUnavailable || len(w.seen) != 0 {
		t.Fatalf("no guard: %d, upstream saw %d", rec.Code, len(w.seen))
	}
}

func TestProxyUpstreamDown(t *testing.T) {
	w := newWebRig(t)
	w.upstream.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Test-Signed-In", "1")
	w.s.proxyHandler(exposed{id: "coolercontrol", name: "CoolerControl", upstream: strings.TrimPrefix(w.upstream.URL, "http://")}).
		ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "CoolerControl isn't answering") {
		t.Fatalf("upstream down: %d %q", rec.Code, rec.Body)
	}
}

func TestStripVOS(t *testing.T) {
	h := http.Header{}
	h.Set("X-VOS-CSRF", "a")
	h.Set("X-Vos-Setup", "b")
	h.Set("X-Other", "c")
	h.Add("Cookie", "vos_session=x; a=1")
	h.Add("Cookie", " vos_setup=y ;b=2;;")
	stripVOS(h)
	if h.Get("X-Vos-Csrf") != "" || h.Get("X-Vos-Setup") != "" || h.Get("X-Other") != "c" {
		t.Errorf("headers %v", h)
	}
	if c := h.Values("Cookie"); len(c) != 1 || c[0] != "a=1; b=2" {
		t.Errorf("cookies %q", c)
	}
	h = http.Header{"Cookie": {"vos_session=x"}}
	stripVOS(h)
	if _, ok := h["Cookie"]; ok {
		t.Errorf("an empty Cookie header was kept: %v", h)
	}
}
