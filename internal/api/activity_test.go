package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPassiveRequestsAreNotActivity(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, clk := newServer(t, Options{})
	var n atomic.Int32
	s.OnActivity(func() { n.Add(1) })
	s.Handle("GET", "/thing", Authed, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	c, _ := login(t, s, "correct-pass")
	signedIn := s.LastActivity()
	clk.Advance(time.Minute)

	passive := map[string]string{"X-VOS-Passive": "1"}
	if rec := do(t, s, req{path: "/api/v1/thing", hdr: passive, cookies: []*http.Cookie{c}}); rec.Code != 200 {
		t.Fatalf("passive GET: %d", rec.Code)
	}
	if rec := do(t, s, req{path: "/api/v1/thing?passive=1", cookies: []*http.Cookie{c}}); rec.Code != 200 {
		t.Fatalf("passive query GET: %d", rec.Code)
	}
	if rec := streamOnce(t, s, "/api/v1/events?passive=1", nil, c); rec.Code != 200 {
		t.Fatalf("passive event stream: %d", rec.Code)
	}
	if n.Load() != 1 || !s.LastActivity().Equal(signedIn) {
		t.Fatalf("passive requests counted: activity %d at %v", n.Load(), s.LastActivity())
	}
	if rec := do(t, s, req{path: "/api/v1/thing", hdr: passive}); rec.Code != 401 {
		t.Fatalf("passive GET without a session: %d", rec.Code)
	}

	do(t, s, req{path: "/api/v1/thing", hdr: map[string]string{"X-VOS-Passive": "0"}, cookies: []*http.Cookie{c}})
	streamOnce(t, s, "/api/v1/events", nil, c)
	if n.Load() != 3 || !s.LastActivity().Equal(clk.Now()) {
		t.Fatalf("active requests: activity %d at %v, want 3 at %v", n.Load(), s.LastActivity(), clk.Now())
	}
}

func TestPassiveSetupRequestsAreNotActivity(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	s.SetSetupCode("K7M2-P9QX")
	var n atomic.Int32
	s.OnActivity(func() { n.Add(1) })
	s.Handle("GET", "/install/status", Setup, func(w http.ResponseWriter, r *http.Request) { OK(w) })

	code := map[string]string{"X-VOS-Setup": "K7M2-P9QX", "X-VOS-Passive": "1"}
	if rec := do(t, s, req{path: "/api/v1/install/status", hdr: code}); rec.Code != 200 {
		t.Fatalf("passive setup GET: %d", rec.Code)
	}
	if rec := streamOnce(t, s, "/api/v1/events?passive=1", map[string]string{"X-VOS-Setup": "K7M2-P9QX"}, nil); rec.Code != 200 {
		t.Fatalf("passive setup event stream: %d", rec.Code)
	}
	if n.Load() != 0 {
		t.Fatalf("passive setup requests counted %d times", n.Load())
	}
	delete(code, "X-VOS-Passive")
	do(t, s, req{path: "/api/v1/install/status", hdr: code})
	if n.Load() != 1 {
		t.Fatalf("setup request counted %d times, want 1", n.Load())
	}
}

// streamOnce opens the event stream with a request that has already been
// cancelled, so the handler sends its replay and returns.
func streamOnce(t *testing.T, s *Server, path string, hdr map[string]string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hr := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	hr.RemoteAddr = lanClient
	hr.Host = ourHost
	for k, v := range hdr {
		hr.Header.Set(k, v)
	}
	if c != nil {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, hr)
	return rec
}
