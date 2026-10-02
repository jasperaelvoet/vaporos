package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// webReq sends r to a handler that serves "ok" behind GuardWeb, as an
// extension's web UI port would, on vapor.local:11987.
func webReq(t *testing.T, s *Server, r req) *httptest.ResponseRecorder {
	t.Helper()
	if r.method == "" {
		r.method = "GET"
	}
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.RemoteAddr = lanClient
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	hr.Host = ourHost + ":11987"
	if r.host != "" {
		hr.Host = r.host
	}
	for k, v := range r.hdr {
		hr.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.GuardWeb(w, r) {
			w.Write([]byte("ok"))
		}
	}).ServeHTTP(rec, hr)
	return rec
}

func TestGuardWeb(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	s.SetAllowPublic(func() bool { return true }) // the extension's port never follows it
	var touched atomic.Int32
	s.OnActivity(func() { touched.Add(1) })
	c, _ := login(t, s, "correct-pass")
	touched.Store(0)
	session := []*http.Cookie{c}
	html := map[string]string{"Accept": "text/html,application/xhtml+xml"}

	cases := []struct {
		name     string
		r        req
		code     int
		location string
		active   bool
	}{
		{"signed out page", req{path: "/dashboard?x=1"}, http.StatusSeeOther,
			"http://vapor.local/login?next=http%3A%2F%2Fvapor.local%3A11987%2Fdashboard%3Fx%3D1", false},
		{"signed out by address", req{path: "/", host: "[fd00::50]:11987"}, http.StatusSeeOther,
			"http://[fd00::50]/login?next=http%3A%2F%2F%5Bfd00%3A%3A50%5D%3A11987%2F", false},
		{"signed out write", req{method: "POST", path: "/api/x"}, http.StatusUnauthorized, "", false},
		{"public source", req{path: "/", remote: "203.0.113.9:4000", cookies: session}, http.StatusForbidden, "", false},
		{"unknown host", req{path: "/", host: "evil.example:11987", cookies: session}, http.StatusMisdirectedRequest, "", false},
		{"cross-origin write", req{method: "POST", path: "/api/x", cookies: session,
			hdr: map[string]string{"Origin": "http://evil.example"}}, http.StatusForbidden, "", false},
		{"navigation", req{path: "/", cookies: session, hdr: html}, http.StatusOK, "", true},
		{"a page's polling", req{path: "/api/status", cookies: session,
			hdr: map[string]string{"Accept": "application/json"}}, http.StatusOK, "", false},
		{"a fetch that accepts anything", req{path: "/x", cookies: session,
			hdr: map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "cors"}}, http.StatusOK, "", false},
		{"same-origin write", req{method: "POST", path: "/api/x", cookies: session,
			hdr: map[string]string{"Origin": "http://vapor.local:11987"}}, http.StatusOK, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			touched.Store(0)
			rec := webReq(t, s, tc.r)
			if rec.Code != tc.code {
				t.Fatalf("code %d, want %d (%s)", rec.Code, tc.code, rec.Body)
			}
			if got := rec.Header().Get("Location"); got != tc.location {
				t.Errorf("Location %q, want %q", got, tc.location)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != "" {
				t.Errorf("vosd's CSP on an extension's page: %q", got)
			}
			if active := touched.Load() > 0; active != tc.active {
				t.Errorf("counted as activity: %v, want %v", active, tc.active)
			}
		})
	}
}

func TestGuardWebLoginOnAnotherPort(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Addr: ":8080"})
	rec := webReq(t, s, req{path: "/"})
	if want := "http://vapor.local:8080/login?next=http%3A%2F%2Fvapor.local%3A11987%2F"; rec.Header().Get("Location") != want {
		t.Fatalf("Location %q, want %q", rec.Header().Get("Location"), want)
	}
}
