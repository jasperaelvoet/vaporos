package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"golang.org/x/crypto/argon2"
)

const (
	lanClient = "192.168.1.10:51000"
	ourIP     = "192.168.1.50"
	ourHost   = "vapor.local"
)

// clock is a settable time source for sessions and rate limits.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// env points every config path at a temp dir and names the machine "vapor".
func env(t *testing.T) {
	t.Helper()
	oldState, oldHost := config.StateDir, config.HostnamePath
	dir := t.TempDir()
	config.StateDir = filepath.Join(dir, "state")
	config.HostnamePath = filepath.Join(dir, "hostname")
	if err := os.WriteFile(config.HostnamePath, []byte("vapor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { config.StateDir, config.HostnamePath = oldState, oldHost })
}

func newServer(t *testing.T, opts Options) (*Server, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	s := New(opts)
	s.now = clk.Now
	s.localIPs = func() []string { return []string{ourIP, "fd00::50"} }
	s.hub = events.NewHub()
	return s, clk
}

// writeAdmin stores password with cheap argon2 parameters, so tests don't
// pay 64 MiB per login. CheckPassword honours the parameters in the hash.
func writeAdmin(t *testing.T, password string) {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(password), salt, 1, 64, 1, 32)
	b64 := base64.RawStdEncoding
	h := fmt.Sprintf("$argon2id$v=19$m=64,t=1,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(key))
	if err := config.WriteJSONAtomic(config.AuthPath(), auth.File{User: "admin", Hash: h}, 0o600); err != nil {
		t.Fatal(err)
	}
}

type req struct {
	method, path, body string
	remote, host       string
	hdr                map[string]string
	cookies            []*http.Cookie
}

func do(t *testing.T, s *Server, r req) *httptest.ResponseRecorder {
	t.Helper()
	if r.method == "" {
		r.method = "GET"
	}
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.RemoteAddr = lanClient
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	hr.Host = ourHost
	if r.host == "-" {
		hr.Host = ""
	} else if r.host != "" {
		hr.Host = r.host
	}
	for k, v := range r.hdr {
		hr.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, hr)
	return rec
}

func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
}

// login returns the session cookie and CSRF token.
func login(t *testing.T, s *Server, password string) (*http.Cookie, string) {
	t.Helper()
	rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"` + password + `"}`})
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var out struct{ CSRF string }
	decode(t, rec, &out)
	c := cookie(rec, sessionCookie)
	if c == nil || out.CSRF == "" {
		t.Fatalf("login gave cookie %v csrf %q", c, out.CSRF)
	}
	return c, out.CSRF
}

func TestPing(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Version: "20260929.1"})
	rec := do(t, s, req{path: "/api/v1/ping"})
	var out map[string]any
	decode(t, rec, &out)
	if rec.Code != 200 || out["ok"] != true || out["mode"] != "os" || out["version"] != "20260929.1" {
		t.Fatalf("ping: %d %v", rec.Code, out)
	}
	s2, _ := newServer(t, Options{Installer: true})
	decode(t, do(t, s2, req{path: "/api/v1/ping"}), &out)
	if out["mode"] != "installer" || out["version"] != config.BinaryVersion {
		t.Fatalf("installer ping: %v", out)
	}
}

func TestSourceIPPolicy(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	cases := map[string]int{
		"127.0.0.1:1":          200,
		"[::1]:1":              200,
		"10.1.2.3:1":           200,
		"172.20.0.9:1":         200,
		"192.168.0.2:1":        200,
		"169.254.10.1:1":       200,
		"[fe80::1%eth0]:1":     200,
		"[fd12:3456::1]:1":     200,
		"[::ffff:10.0.0.1]:1":  200,
		"8.8.8.8:1":            403,
		"100.64.0.1:1":         403,
		"[2001:db8::1]:1":      403,
		"[::ffff:1.1.1.1]:1":   403,
		"not-an-address":       403,
		"172.32.0.1:1":         403,
		"192.169.0.1:1":        403,
		"[fe00::1]:1":          403,
		"[2a02:1810::bad]:443": 403,
	}
	for remote, want := range cases {
		if got := do(t, s, req{path: "/api/v1/ping", remote: remote}).Code; got != want {
			t.Errorf("remote %s: got %d, want %d", remote, got, want)
		}
	}
	var public atomic.Bool
	s.SetAllowPublic(public.Load)
	if got := do(t, s, req{path: "/api/v1/ping", remote: "8.8.8.8:1"}).Code; got != 403 {
		t.Fatalf("allow_public=false: %d", got)
	}
	public.Store(true)
	if got := do(t, s, req{path: "/api/v1/ping", remote: "8.8.8.8:1"}).Code; got != 200 {
		t.Fatalf("allow_public=true: %d", got)
	}
}

func TestHostAllowlist(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	cases := map[string]int{
		"vapor.local":          200,
		"VAPOR.LOCAL":          200,
		"vapor.local.":         200,
		"vapor.local:80":       200,
		"vapor":                200,
		"localhost":            200,
		"localhost:8080":       200,
		"127.0.0.1":            200,
		"127.0.0.1:80":         200,
		"[::1]":                200,
		"[::1]:80":             200,
		"vaporos-setup.local":  200,
		ourIP:                  200,
		ourIP + ":80":          200,
		"[fd00::50]:80":        200,
		"192.168.1.51":         421,
		"evil.example":         421,
		"vapor.local.evil.com": 421,
		"vapor.evil":           421,
		"-":                    421, // no Host at all
		"[fd00::51]":           421,
	}
	for host, want := range cases {
		if got := do(t, s, req{path: "/api/v1/ping", host: host}).Code; got != want {
			t.Errorf("Host %q: got %d, want %d", host, got, want)
		}
	}
	// A hostname change is picked up without a restart.
	if err := os.WriteFile(config.HostnamePath, []byte("den\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.hosts.mu.Lock()
	s.hosts.at = time.Time{}
	s.hosts.mu.Unlock()
	if got := do(t, s, req{path: "/api/v1/ping", host: "den.local"}).Code; got != 200 {
		t.Fatalf("new hostname: %d", got)
	}
	if got := do(t, s, req{path: "/api/v1/ping", host: "vapor.local"}).Code; got != 421 {
		t.Fatalf("old hostname: %d", got)
	}
}

func TestHostMatchesConnectedAddress(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	s.localIPs = func() []string { return nil }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	// ts listens on 127.0.0.1; ask for it by a link-local-looking literal
	// that is not ours to prove the literal must match the local address.
	r, _ := http.NewRequest("GET", ts.URL+"/api/v1/ping", nil)
	r.Host = "169.254.1.1"
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 421 {
		t.Fatalf("foreign literal: %d", resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/api/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("connected address: %d", resp.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	rec := do(t, s, req{path: "/api/v1/ping"})
	h := rec.Header()
	if h.Get("Content-Security-Policy") != "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'" {
		t.Errorf("CSP = %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers: %v", h)
	}
	if h.Get("Cache-Control") != "no-store" || h.Get("Content-Type") != "application/json" {
		t.Errorf("json headers: %v", h)
	}
}

func TestLoginSessionCSRFLogout(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	var hits atomic.Int32
	s.Handle("POST", "/thing", Authed, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); OK(w) })
	s.Handle("GET", "/thing", Authed, func(w http.ResponseWriter, r *http.Request) { OK(w) })

	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"nope-nope"}`}); rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `not json`}); rec.Code != 400 {
		t.Fatalf("bad body: %d", rec.Code)
	}
	if rec := do(t, s, req{path: "/api/v1/thing"}); rec.Code != 401 {
		t.Fatalf("unauthenticated GET: %d", rec.Code)
	}

	c, csrf := login(t, s, "correct-pass")
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie attributes: %+v", c)
	}

	var me meResponse
	decode(t, do(t, s, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}}), &me)
	if !me.Authenticated || me.CSRF != csrf || me.NeedsSetup || me.Installer {
		t.Fatalf("me: %+v", me)
	}

	if rec := do(t, s, req{path: "/api/v1/thing", cookies: []*http.Cookie{c}}); rec.Code != 200 {
		t.Fatalf("GET needs no CSRF: %d", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/thing", cookies: []*http.Cookie{c}}); rec.Code != 403 {
		t.Fatalf("POST without CSRF: %d", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/thing", cookies: []*http.Cookie{c}, hdr: map[string]string{"X-VOS-CSRF": csrf + "x"}}); rec.Code != 403 {
		t.Fatalf("POST with wrong CSRF: %d", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/thing", cookies: []*http.Cookie{c}, hdr: map[string]string{"X-VOS-CSRF": csrf}}); rec.Code != 200 || hits.Load() != 1 {
		t.Fatalf("POST with CSRF: %d hits %d", rec.Code, hits.Load())
	}

	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/logout", cookies: []*http.Cookie{c}}); rec.Code != 403 {
		t.Fatalf("logout without CSRF: %d", rec.Code)
	}
	rec := do(t, s, req{method: "POST", path: "/api/v1/auth/logout", cookies: []*http.Cookie{c}, hdr: map[string]string{"X-VOS-CSRF": csrf}})
	if rec.Code != 200 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if cl := cookie(rec, sessionCookie); cl == nil || cl.MaxAge >= 0 {
		t.Fatalf("logout did not clear the cookie: %+v", cl)
	}
	decode(t, do(t, s, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}}), &me)
	if me.Authenticated || me.CSRF != "" {
		t.Fatalf("me after logout: %+v", me)
	}
}

func TestLoginWithoutAdmin(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"whatever1"}`})
	if rec.Code != 409 {
		t.Fatalf("login without auth.json: %d", rec.Code)
	}
	var me meResponse
	decode(t, do(t, s, req{path: "/api/v1/auth/me"}), &me)
	if !me.NeedsSetup || !me.Installer || me.Authenticated {
		t.Fatalf("me: %+v", me)
	}
}

func TestLoginRateLimit(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, clk := newServer(t, Options{})
	try := func(remote, pw string) *httptest.ResponseRecorder {
		return do(t, s, req{method: "POST", path: "/api/v1/auth/login", remote: remote, body: `{"password":"` + pw + `"}`})
	}
	for i := 0; i < 5; i++ {
		if rec := try(lanClient, "wrong"); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	rec := try(lanClient, "correct-pass")
	if rec.Code != 429 {
		t.Fatalf("locked out, right password: %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "15" {
		t.Fatalf("Retry-After = %q", ra)
	}
	// Another client is unaffected.
	if rec := try("192.168.1.11:1", "correct-pass"); rec.Code != 200 {
		t.Fatalf("other client: %d", rec.Code)
	}
	clk.Advance(16 * time.Second)
	if rec := try(lanClient, "wrong"); rec.Code != 401 {
		t.Fatalf("after lockout: %d", rec.Code)
	}
	if rec := try(lanClient, "correct-pass"); rec.Code != 429 || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("second lockout: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	clk.Advance(31 * time.Second)
	if rec := try(lanClient, "correct-pass"); rec.Code != 200 {
		t.Fatalf("success after lockout: %d", rec.Code)
	}
	// Success resets the count.
	for i := 0; i < 4; i++ {
		if rec := try(lanClient, "wrong"); rec.Code != 401 {
			t.Fatalf("after reset %d: %d", i, rec.Code)
		}
	}
}

func TestLimiterBackoff(t *testing.T) {
	clk := &clock{t: time.Unix(1e9, 0)}
	l := newLimiter(clk.Now)
	want := []time.Duration{0, 0, 0, 0, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute,
		4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		l.fail("k")
		d, ok := l.check("k")
		if w == 0 {
			if !ok {
				t.Fatalf("failure %d locked out early", i+1)
			}
			continue
		}
		if ok || d != w {
			t.Fatalf("failure %d: lockout %v ok=%v, want %v", i+1, d, ok, w)
		}
		clk.Advance(d)
	}
	for i := 0; i < 40; i++ { // no overflow however long it goes on
		l.fail("k")
		if d, _ := l.check("k"); d != 15*time.Minute {
			t.Fatalf("lockout %v", d)
		}
		clk.Advance(15 * time.Minute)
	}
	// An hour of quiet starts over.
	clk.Advance(61 * time.Minute)
	l.fail("k")
	if _, ok := l.check("k"); !ok {
		t.Fatal("not forgotten after an hour")
	}
}

// A burst of parallel guesses must not all be checked before the first
// failure is recorded: the lockout applies to every guess.
func TestLoginBurstIsLimited(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	const n = 30
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"wrong"}`}).Code
		}()
	}
	wg.Wait()
	close(codes)
	count := map[int]int{}
	for c := range codes {
		count[c]++
	}
	if count[401] < 1 || count[401] > 5 || count[401]+count[429] != n {
		t.Fatalf("burst of %d wrong passwords: %v, want at most 5 checked (401) and the rest refused (429)", n, count)
	}
}

// blockingVerify replaces the password check with one that waits until
// released, so tests can hold a check in flight.
type blockingVerify struct {
	entered chan string
	release chan struct{}
}

func newBlockingVerify(s *Server) *blockingVerify {
	b := &blockingVerify{entered: make(chan string, 64), release: make(chan struct{})}
	s.verifyAdmin = func(ctx context.Context, pw string) (bool, error) {
		b.entered <- pw
		select {
		case <-b.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		return pw == "correct-pass", nil
	}
	return b
}

func TestLoginOneCheckAtATimePerClient(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	b := newBlockingVerify(s)
	login := func(pw string) *httptest.ResponseRecorder {
		return do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"` + pw + `"}`})
	}
	first := make(chan int, 1)
	go func() { first <- login("wrong").Code }()
	<-b.entered
	// The right password, sent while a guess is still being checked, is
	// refused rather than checked in parallel.
	rec := login("correct-pass")
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("second attempt while one is in flight: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Other clients are not held up.
	other := make(chan int, 1)
	go func() {
		other <- do(t, s, req{method: "POST", path: "/api/v1/auth/login", remote: "192.168.1.11:1", body: `{"password":"correct-pass"}`}).Code
	}()
	<-b.entered
	close(b.release)
	if c := <-first; c != 401 {
		t.Fatalf("first attempt: %d", c)
	}
	if c := <-other; c != 200 {
		t.Fatalf("other client: %d", c)
	}
	if rec := login("correct-pass"); rec.Code != 200 {
		t.Fatalf("after the check finished: %d", rec.Code)
	}
}

func TestLoginAbandonedCheckIsNotCounted(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	s.verifyAdmin = func(ctx context.Context, pw string) (bool, error) { return false, context.Canceled }
	for range 10 {
		if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"wrong"}`}); rec.Code != 503 {
			t.Fatalf("abandoned check: %d", rec.Code)
		}
	}
	s.verifyAdmin = auth.VerifyAdminContext
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"correct-pass"}`}); rec.Code != 200 {
		t.Fatalf("checks that never ran locked the client out: %d", rec.Code)
	}
}

func TestPasswordChecksAreBoundedAcrossClients(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	b := newBlockingVerify(s)
	var wg sync.WaitGroup
	for i := range maxPasswordChecks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			do(t, s, req{method: "POST", path: "/api/v1/auth/login", remote: fmt.Sprintf("192.168.2.%d:1", i+1), body: `{"password":"wrong"}`})
		}()
	}
	for range maxPasswordChecks {
		<-b.entered
	}
	rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", remote: "192.168.3.1:1", body: `{"password":"correct-pass"}`})
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("check beyond the cap: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	close(b.release)
	wg.Wait()
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", remote: "192.168.3.1:1", body: `{"password":"correct-pass"}`}); rec.Code != 200 {
		t.Fatalf("after the burst: %d", rec.Code)
	}
}

func TestLimiterKeepsReservationsWhenPruning(t *testing.T) {
	clk := &clock{t: time.Unix(1e9, 0)}
	l := newLimiter(clk.Now)
	if _, ok := l.begin("k"); !ok {
		t.Fatal("first begin refused")
	}
	clk.Advance(2 * time.Hour) // stale by age, but still in flight
	for i := range maxTracked + 10 {
		l.fail(fmt.Sprint("other-", i))
	}
	if d, ok := l.begin("k"); ok || d != busyRetry {
		t.Fatalf("a pruned reservation let a second attempt in: %v %v", d, ok)
	}
	l.end("k")
	if _, ok := l.begin("k"); !ok {
		t.Fatal("begin refused after end")
	}
	l.end("k")
	if _, ok := l.m["k"]; ok {
		t.Fatal("an attempt that recorded nothing left an entry behind")
	}
}

func TestLimitKeyGroupsIPv6Prefix(t *testing.T) {
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.RemoteAddr = "[fd00:1:2:3:aaaa::1]:5"
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "[fd00:1:2:3:bbbb::2]:6"
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.RemoteAddr = "[::ffff:192.168.1.9]:7"
	if limitKey(r1) != limitKey(r2) {
		t.Fatalf("same /64 differs: %s %s", limitKey(r1), limitKey(r2))
	}
	if limitKey(r3) != "192.168.1.9" {
		t.Fatalf("mapped v4 key = %s", limitKey(r3))
	}
}

func TestSessionsPersistHashedAndSlide(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, clk := newServer(t, Options{})
	c, csrf := login(t, s, "correct-pass")

	st, err := os.Stat(config.SessionsPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("sessions.json: %v %v", err, st)
	}
	raw, _ := os.ReadFile(config.SessionsPath())
	if strings.Contains(string(raw), c.Value) {
		t.Fatal("sessions.json contains the raw token")
	}
	tok, _ := base64.RawURLEncoding.DecodeString(c.Value)
	sum := sha256.Sum256(tok)
	var file map[string]sessionRec
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	rec, ok := file[hex.EncodeToString(sum[:])]
	if !ok || rec.CSRF != csrf || len(tok) != 32 {
		t.Fatalf("sessions.json = %s", raw)
	}
	firstExpiry := rec.Expires

	// A restarted vosd knows the session.
	s2, _ := newServer(t, Options{})
	s2.now = clk.Now
	var me meResponse
	decode(t, do(t, s2, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}}), &me)
	if !me.Authenticated || me.CSRF != csrf {
		t.Fatalf("after reload: %+v", me)
	}

	// Within the refresh window nothing is rewritten; after it, the expiry
	// slides and the cookie is re-sent.
	clk.Advance(10 * time.Minute)
	if r := do(t, s2, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}}); cookie(r, sessionCookie) != nil {
		t.Fatal("cookie re-sent inside the refresh window")
	}
	clk.Advance(2 * time.Hour)
	r := do(t, s2, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}})
	if nc := cookie(r, sessionCookie); nc == nil || nc.Value != c.Value || nc.MaxAge != 30*24*3600 {
		t.Fatalf("slid cookie: %+v", nc)
	}
	raw, _ = os.ReadFile(config.SessionsPath())
	json.Unmarshal(raw, &file)
	if got := file[hex.EncodeToString(sum[:])].Expires; !got.After(firstExpiry) {
		t.Fatalf("expiry did not slide: %v -> %v", firstExpiry, got)
	}

	// Thirty idle days end it.
	clk.Advance(31 * 24 * time.Hour)
	r = do(t, s2, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}})
	decode(t, r, &me)
	if me.Authenticated {
		t.Fatal("session outlived 30 idle days")
	}
	if cl := cookie(r, sessionCookie); cl == nil || cl.MaxAge >= 0 {
		t.Fatal("expired cookie not cleared")
	}
}

func TestSessionStoreRejectsGarbageAndCaps(t *testing.T) {
	env(t)
	clk := &clock{t: time.Unix(2e9, 0)}
	st := newSessionStore(config.SessionsPath(), clk.Now)
	for _, tok := range []string{"", "abc", strings.Repeat("A", 43) + "=", strings.Repeat("*", 43)} {
		if _, _, _, ok := st.lookup(tok); ok {
			t.Fatalf("garbage token %q accepted", tok)
		}
	}
	var first string
	for i := 0; i < maxSessions+5; i++ {
		tok, _, err := st.create()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = tok
		}
		clk.Advance(time.Second)
	}
	if len(st.m) != maxSessions {
		t.Fatalf("%d sessions, want %d", len(st.m), maxSessions)
	}
	if _, _, _, ok := st.lookup(first); ok {
		t.Fatal("oldest session survived eviction")
	}
	// A corrupt file is ignored rather than fatal.
	os.WriteFile(config.SessionsPath(), []byte("{broken"), 0o600)
	if st2 := newSessionStore(config.SessionsPath(), clk.Now); len(st2.m) != 0 {
		t.Fatal("corrupt sessions.json yielded sessions")
	}
}

func TestSetupAccess(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	s.SetSetupCode("ABCD-EF12")
	s.Handle("GET", "/wizard", Setup, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	s.Handle("POST", "/wizard", Setup, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	s.HandleRaw("GET /setup", Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.SetSetupCookie(w, r, r.URL.Query().Get("code")) {
			fmt.Fprint(w, "ok")
			return
		}
		http.Error(w, "bad code", 403)
	}))

	if rec := do(t, s, req{path: "/api/v1/wizard"}); rec.Code != 403 {
		t.Fatalf("no code: %d", rec.Code)
	}
	if rec := do(t, s, req{path: "/api/v1/wizard", hdr: map[string]string{"X-VOS-Setup": "ABCD-EF13"}}); rec.Code != 403 {
		t.Fatalf("wrong code: %d", rec.Code)
	}
	// Typed sloppily: lower case, a space, O for 0 and l for 1.
	s.SetSetupCode("ABCD-E012")
	if rec := do(t, s, req{method: "POST", path: "/api/v1/wizard", hdr: map[string]string{"X-VOS-Setup": "abcd eOl2"}}); rec.Code != 200 {
		t.Fatalf("normalised header code: %d %s", rec.Code, rec.Body)
	}

	rec := do(t, s, req{path: "/setup?code=abcd-e012"})
	if rec.Code != 200 {
		t.Fatalf("GET /setup?code: %d", rec.Code)
	}
	sc := cookie(rec, setupCookie)
	if sc == nil || !sc.HttpOnly || sc.SameSite != http.SameSiteStrictMode || sc.Value != "ABCDE012" {
		t.Fatalf("setup cookie: %+v", sc)
	}
	if rec := do(t, s, req{path: "/api/v1/wizard", cookies: []*http.Cookie{sc}}); rec.Code != 200 {
		t.Fatalf("cookie access: %d", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/wizard", cookies: []*http.Cookie{sc}}); rec.Code != 200 {
		t.Fatalf("cookie POST: %d", rec.Code)
	}
	if !s.HasSetupAccess(func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = lanClient
		r.AddCookie(sc)
		return r
	}()) {
		t.Fatal("HasSetupAccess false with the cookie")
	}

	// A stale cookie is refused and cleared.
	stale := &http.Cookie{Name: setupCookie, Value: "ZZZZZZZZ"}
	rec = do(t, s, req{path: "/api/v1/wizard", remote: "192.168.1.20:1", cookies: []*http.Cookie{stale}})
	if cl := cookie(rec, setupCookie); rec.Code != 403 || cl == nil || cl.MaxAge >= 0 {
		t.Fatalf("stale cookie: %d %+v", rec.Code, cl)
	}

	// Guessing gets locked out on every channel.
	guesser := "192.168.1.30:1"
	for i := 0; i < 5; i++ {
		do(t, s, req{path: "/setup?code=WRONG" + fmt.Sprint(i), remote: guesser})
	}
	if rec := do(t, s, req{path: "/api/v1/wizard", remote: guesser, hdr: map[string]string{"X-VOS-Setup": "ABCD-E012"}}); rec.Code != 429 {
		t.Fatalf("locked-out guesser with right code: %d", rec.Code)
	}
	if rec := do(t, s, req{path: "/setup?code=ABCD-E012", remote: guesser}); rec.Code != 403 {
		t.Fatalf("locked-out guesser via page: %d", rec.Code)
	}

	// No code, no setup.
	s.SetSetupCode("")
	if rec := do(t, s, req{path: "/api/v1/wizard", cookies: []*http.Cookie{sc}}); rec.Code != 403 {
		t.Fatalf("after setup ended: %d", rec.Code)
	}
}

// Another site can make a browser load /setup?code=… (an <img>, a popup it
// re-navigates). Such codes are ignored, so they can neither lock the
// browser out nor serve as free guesses.
func TestSetupCodeFromOtherSitesIsIgnored(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	s.SetSetupCode("ABCD-EF12")
	s.Handle("GET", "/wizard", Setup, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	s.HandleRaw("GET /setup", Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.SetSetupCookie(w, r, r.URL.Query().Get("code")) {
			fmt.Fprint(w, "ok")
			return
		}
		http.Error(w, "bad code", 403)
	}))
	victim := "192.168.1.40:1"
	foreign := []map[string]string{
		{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Dest": "image"},
		{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Dest": "document"},
		{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Dest": "document"},
		{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Dest": "image"},
	}
	for i := 0; i < 10; i++ {
		rec := do(t, s, req{path: "/setup?code=WRONG" + fmt.Sprint(i), remote: victim, hdr: foreign[i%len(foreign)]})
		if cookie(rec, setupCookie) != nil {
			t.Fatal("a foreign request got a setup cookie")
		}
	}
	// The right code from another site is not honoured either: the
	// response must not tell a forged request whether its guess was right.
	for _, hdr := range foreign {
		if rec := do(t, s, req{path: "/setup?code=ABCD-EF12", remote: victim, hdr: hdr}); cookie(rec, setupCookie) != nil {
			t.Fatalf("right code with %v set the cookie", hdr)
		}
	}
	if rec := do(t, s, req{path: "/api/v1/wizard", remote: victim, hdr: map[string]string{"X-VOS-Setup": "ABCD-EF12"}}); rec.Code != 200 {
		t.Fatalf("the victim was locked out by foreign requests: %d", rec.Code)
	}
	// A scanned QR code or typed URL, and a client without Fetch Metadata.
	for _, hdr := range []map[string]string{{"Sec-Fetch-Site": "none", "Sec-Fetch-Dest": "document"}, {"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Dest": "document"}, nil} {
		if rec := do(t, s, req{path: "/setup?code=abcd-ef12", remote: victim, hdr: hdr}); rec.Code != 200 || cookie(rec, setupCookie) == nil {
			t.Fatalf("navigation %v: %d, cookie %v", hdr, rec.Code, cookie(rec, setupCookie))
		}
	}
}

func TestSetupWaiver(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	s.SetSetupCode("ABCD-EF12")
	s.Handle("GET", "/wizard", Setup, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	var headless atomic.Bool
	s.SetSetupWaiver(headless.Load)
	bare := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/events", nil)
		r.RemoteAddr = lanClient
		return r
	}

	if rec := do(t, s, req{path: "/api/v1/wizard"}); rec.Code != 403 || s.SetupWaived() {
		t.Fatalf("monitor attached, no code: %d", rec.Code)
	}
	headless.Store(true)
	if !s.SetupWaived() || !s.stillAllowed(bare()) || !s.HasSetupAccess(bare()) {
		t.Fatal("no monitor: the code is not waived")
	}
	if rec := do(t, s, req{path: "/api/v1/wizard"}); rec.Code != 200 {
		t.Fatalf("no monitor, no code: %d", rec.Code)
	}
	// The harness sends "X-VOS-Setup: -" when VOS-READY says code=-; any
	// code passes and none is counted.
	for range 10 {
		if rec := do(t, s, req{path: "/api/v1/wizard", hdr: map[string]string{"X-VOS-Setup": "-"}}); rec.Code != 200 {
			t.Fatalf("no monitor, placeholder code: %d", rec.Code)
		}
	}
	// The source-IP rule still holds.
	if rec := do(t, s, req{path: "/api/v1/wizard", remote: "8.8.8.8:1"}); rec.Code != 403 {
		t.Fatalf("public address while waived: %d", rec.Code)
	}
	// A monitor plugged in re-arms the code at once, streams included.
	headless.Store(false)
	if rec := do(t, s, req{path: "/api/v1/wizard"}); rec.Code != 403 {
		t.Fatalf("monitor plugged in, no code: %d", rec.Code)
	}
	if s.stillAllowed(bare()) {
		t.Fatal("a stream admitted by the waiver outlived it")
	}
	if rec := do(t, s, req{path: "/api/v1/wizard", hdr: map[string]string{"X-VOS-Setup": "ABCD-EF12"}}); rec.Code != 200 {
		t.Fatalf("right code after the waiver (guesses must not have counted): %d", rec.Code)
	}

	// First-run setup on an installed system never waives the code.
	installed, _ := newServer(t, Options{})
	installed.SetSetupCode("ABCD-EF12")
	installed.Handle("GET", "/wizard", Setup, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	installed.SetSetupWaiver(func() bool { return true })
	if rec := do(t, installed, req{path: "/api/v1/wizard"}); rec.Code != 403 || installed.SetupWaived() {
		t.Fatalf("OS mode honoured the waiver: %d", rec.Code)
	}
}

func TestFirstRunSetup(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	var codes []string
	s.SetSetupCode("K7M2-P9QX")
	s.OnSetupCodeChange(func(c string) { codes = append(codes, c) })
	hdr := map[string]string{"X-VOS-Setup": "K7M2-P9QX"}

	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/setup", body: `{"password":"new-admin-pw"}`}); rec.Code != 403 {
		t.Fatalf("setup without code: %d", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/setup", hdr: hdr, body: `{"password":"short"}`}); rec.Code != 400 {
		t.Fatalf("short password: %d", rec.Code)
	}
	rec := do(t, s, req{method: "POST", path: "/api/v1/auth/setup", hdr: hdr, body: `{"password":"new-admin-pw"}`})
	if rec.Code != 200 {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	var out struct{ CSRF string }
	decode(t, rec, &out)
	c := cookie(rec, sessionCookie)
	if out.CSRF == "" || c == nil {
		t.Fatalf("setup did not log in: %s", rec.Body)
	}
	if !auth.HasAdmin() {
		t.Fatal("no auth.json after setup")
	}
	if s.SetupCode() != "" || len(codes) != 1 || codes[0] != "" {
		t.Fatalf("code after setup %q, hook saw %v", s.SetupCode(), codes)
	}
	var me meResponse
	decode(t, do(t, s, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c}}), &me)
	if !me.Authenticated || me.NeedsSetup {
		t.Fatalf("me after setup: %+v", me)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/setup", hdr: hdr, body: `{"password":"another-pw"}`}); rec.Code != 403 {
		t.Fatalf("second setup: %d", rec.Code)
	}
	if ok, _ := auth.VerifyAdmin("new-admin-pw"); !ok {
		t.Fatal("password not stored")
	}
}

func TestInstallerRefusesFirstRunSetup(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	s.SetSetupCode("K7M2-P9QX")
	rec := do(t, s, req{method: "POST", path: "/api/v1/auth/setup", hdr: map[string]string{"X-VOS-Setup": "K7M2-P9QX"}, body: `{"password":"new-admin-pw"}`})
	if rec.Code != 409 || auth.HasAdmin() {
		t.Fatalf("installer setup: %d", rec.Code)
	}
}

func TestChangePassword(t *testing.T) {
	env(t)
	writeAdmin(t, "old-password")
	s, _ := newServer(t, Options{})
	c1, csrf1 := login(t, s, "old-password")
	c2, _ := login(t, s, "old-password")
	change := func(body string) *httptest.ResponseRecorder {
		return do(t, s, req{method: "POST", path: "/api/v1/auth/password", body: body,
			cookies: []*http.Cookie{c1}, hdr: map[string]string{"X-VOS-CSRF": csrf1}})
	}
	if rec := change(`{"current":"not-it","new":"new-password"}`); rec.Code != 403 {
		t.Fatalf("wrong current: %d", rec.Code)
	}
	if rec := change(`{"current":"old-password","new":"short"}`); rec.Code != 400 {
		t.Fatalf("short new: %d", rec.Code)
	}
	if rec := change(`{"current":"old-password","new":"new-password"}`); rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	var me meResponse
	decode(t, do(t, s, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c1}}), &me)
	if !me.Authenticated {
		t.Fatal("changing the password logged out the session that did it")
	}
	decode(t, do(t, s, req{path: "/api/v1/auth/me", cookies: []*http.Cookie{c2}}), &me)
	if me.Authenticated {
		t.Fatal("other session survived a password change")
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"old-password"}`}); rec.Code != 401 {
		t.Fatalf("old password still works: %d", rec.Code)
	}
	login(t, s, "new-password")
}

// Reauth shares the login limit: a wrong password is 403 and counts, and
// after the free failures even the right one is refused for a while.
func TestReauthSharesTheLoginLimit(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	c, csrf := login(t, s, "correct-pass")
	s.Handle("POST", "/guarded", Authed, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		ReadJSON(r, &body)
		if s.Reauth(w, r, body.Password) {
			OK(w)
		}
	})
	try := func(pw string) *httptest.ResponseRecorder {
		return do(t, s, req{method: "POST", path: "/api/v1/guarded", body: `{"password":"` + pw + `"}`,
			cookies: []*http.Cookie{c}, hdr: map[string]string{"X-VOS-CSRF": csrf}})
	}
	if rec := try("correct-pass"); rec.Code != 200 {
		t.Fatalf("right password: %d %s", rec.Code, rec.Body)
	}
	for i := range 5 {
		if rec := try("wrong"); rec.Code != 403 || !strings.Contains(rec.Body.String(), "the password is wrong") {
			t.Fatalf("wrong password %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := try("correct-pass"); rec.Code != 429 {
		t.Fatalf("after 5 failures: %d, want the login limit's 429", rec.Code)
	}
	if rec := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: `{"password":"correct-pass"}`}); rec.Code != 429 {
		t.Fatalf("sign-in after 5 wrong re-auths: %d, want 429", rec.Code)
	}
}

func TestLocalAccess(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	s.Handle("GET", "/welcome", Local, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	if rec := do(t, s, req{path: "/api/v1/welcome", remote: "127.0.0.1:9", host: "127.0.0.1"}); rec.Code != 200 {
		t.Fatalf("loopback: %d", rec.Code)
	}
	if rec := do(t, s, req{path: "/api/v1/welcome"}); rec.Code != 403 {
		t.Fatalf("LAN: %d", rec.Code)
	}
}

func TestCrossOriginWritesRefused(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, _ := newServer(t, Options{})
	body := `{"password":"correct-pass"}`
	cases := []struct {
		hdr  map[string]string
		want int
	}{
		{map[string]string{"Origin": "http://evil.example"}, 403},
		{map[string]string{"Origin": "null"}, 403},
		{map[string]string{"Origin": "https://vapor.local"}, 403},
		{map[string]string{"Origin": "http://vapor.local:8080"}, 403},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{map[string]string{"Origin": "http://vapor.local"}, 200},
		{map[string]string{"Origin": "http://VAPOR.local:80"}, 200},
		{map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{nil, 200},
	}
	for _, c := range cases {
		if got := do(t, s, req{method: "POST", path: "/api/v1/auth/login", body: body, hdr: c.hdr}).Code; got != c.want {
			t.Errorf("%v: got %d, want %d", c.hdr, got, c.want)
		}
	}
	// Reads are not affected.
	if got := do(t, s, req{path: "/api/v1/ping", hdr: map[string]string{"Origin": "http://evil.example"}}).Code; got != 200 {
		t.Errorf("cross-origin GET: %d", got)
	}
}

func TestUnknownRoutes(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	s.Handle("DELETE", "/sunshine/clients/{uuid}", Authed, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	rec := do(t, s, req{path: "/api/v1/nope"})
	var e map[string]string
	decode(t, rec, &e)
	if rec.Code != 404 || e["error"] == "" {
		t.Fatalf("404: %d %v", rec.Code, e)
	}
	rec = do(t, s, req{method: "POST", path: "/api/v1/ping"})
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("405: %d %q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := do(t, s, req{path: "/api/v1/sunshine/clients/abc"}); rec.Code != 405 {
		t.Fatalf("wildcard 405: %d", rec.Code)
	}
	if rec := do(t, s, req{path: "/api/v1/sunshine/clients/abc/x"}); rec.Code != 404 {
		t.Fatalf("wildcard 404: %d", rec.Code)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/ping", "/ping", true},
		{"/ping", "/pong", false},
		{"/a/{x}", "/a/1", true},
		{"/a/{x}", "/a/", false},
		{"/a/{x}", "/a/1/2", false},
		{"/a/{rest...}", "/a/1/2", true},
		{"/static/", "/static/app.js", true},
		{"/a/{$}", "/a/", true},
	}
	for _, c := range cases {
		if got := matchPattern(c.pattern, c.path); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v", c.pattern, c.path, got)
		}
	}
}

func TestRawPagesRedirectToLogin(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{})
	s.HandleRaw("GET /updates", Authed, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "page") }))
	s.HandleRaw("GET /{$}", Authed, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "home") }))
	rec := do(t, s, req{path: "/updates?tab=2"})
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?next=%2Fupdates%3Ftab%3D2" {
		t.Fatalf("redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(t, s, req{path: "/"}); rec.Header().Get("Location") != "/login" {
		t.Fatalf("home redirect: %q", rec.Header().Get("Location"))
	}
}

func TestActivityHook(t *testing.T) {
	env(t)
	writeAdmin(t, "correct-pass")
	s, clk := newServer(t, Options{})
	var n atomic.Int32
	s.OnActivity(func() { n.Add(1) })
	s.Handle("GET", "/thing", Authed, func(w http.ResponseWriter, r *http.Request) { OK(w) })
	do(t, s, req{path: "/api/v1/ping"})
	do(t, s, req{path: "/api/v1/auth/me"})
	if n.Load() != 0 || !s.LastActivity().IsZero() {
		t.Fatalf("public requests counted as activity: %d", n.Load())
	}
	c, _ := login(t, s, "correct-pass")
	clk.Advance(time.Minute)
	do(t, s, req{path: "/api/v1/thing", cookies: []*http.Cookie{c}})
	if n.Load() != 2 || !s.LastActivity().Equal(clk.Now()) {
		t.Fatalf("activity %d at %v, want 2 at %v", n.Load(), s.LastActivity(), clk.Now())
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{
		"abcd-efgh":             "ABCDEFGH",
		" K7M2 p9qx ":           "K7M2P9QX",
		"O0-Il":                 "0011",
		strings.Repeat("a", 65): "",
	} {
		if got := NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
	if codesEqual("", "") || codesEqual("x", "") {
		t.Fatal("empty codes compare equal")
	}
}

// sseReader reads events from an SSE response body.
type sseReader struct{ sc *bufio.Scanner }

// next returns the next "event: data" pair or comment, skipping blank lines.
func (r *sseReader) next(t *testing.T) string {
	t.Helper()
	var parts []string
	for r.sc.Scan() {
		line := r.sc.Text()
		if line == "" {
			if len(parts) > 0 {
				return strings.Join(parts, "|")
			}
			continue
		}
		parts = append(parts, line)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "|")
	}
	return "EOF"
}

func TestEventStream(t *testing.T) {
	env(t)
	s, _ := newServer(t, Options{Installer: true})
	s.heartbeat = 50 * time.Millisecond
	s.SetSetupCode("K7M2-P9QX")
	s.hub.Publish("install.progress", map[string]any{"step": "partition", "percent": 5})
	s.hub.Publish("display.changed", struct{}{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var readyAddr net.Addr
	s.opts.Ready = func(a net.Addr) { readyAddr = a }
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()

	resp, err := http.Get(base + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("events without credentials: %d", resp.StatusCode)
	}

	r, _ := http.NewRequest("GET", base+"/api/v1/events", nil)
	r.Header.Set("X-VOS-Setup", "K7M2-P9QX")
	resp, err = http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if readyAddr == nil {
		t.Fatal("Ready not called")
	}
	sr := &sseReader{bufio.NewScanner(resp.Body)}
	if got := sr.next(t); got != "retry: 3000" {
		t.Fatalf("first frame %q", got)
	}
	// Last() comes first, sorted by topic.
	if got := sr.next(t); got != "event: display.changed|data: {}" {
		t.Fatalf("replayed event %q", got)
	}
	if got := sr.next(t); got != `event: install.progress|data: {"percent":5,"step":"partition"}` {
		t.Fatalf("replayed event %q", got)
	}
	s.hub.Publish("system.message", map[string]string{"level": "info", "text": "hi"})
	var got string
	for got = sr.next(t); got == ": ping"; got = sr.next(t) {
	}
	if got != `event: system.message|data: {"level":"info","text":"hi"}` {
		t.Fatalf("live event %q", got)
	}
	if got := sr.next(t); got != ": ping" {
		t.Fatalf("heartbeat %q", got)
	}
	// When setup ends the stream closes at the next heartbeat.
	s.SetSetupCode("")
	deadline := time.Now().Add(2 * time.Second)
	for sr.next(t) != "EOF" {
		if time.Now().After(deadline) {
			t.Fatal("stream outlived its setup code")
		}
	}

	// A stream that stays open does not hold up shutdown.
	r2, _ := http.NewRequest("GET", base+"/api/v1/events", nil)
	r2.Header.Set("X-Forwarded-For", "1.2.3.4") // ignored
	s.SetSetupCode("K7M2-P9QX")
	r2.Header.Set("X-VOS-Setup", "K7M2-P9QX")
	resp2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("shutdown took %v", time.Since(start))
	}
}

func TestWriteEventSplitsLines(t *testing.T) {
	var b strings.Builder
	writeEvent(&b, events.Event{Topic: "a\nb", Data: json.RawMessage("{\n\"x\":1\r\n}")})
	if b.String() != "event: ab\ndata: {\ndata: \"x\":1\ndata: }\n\n" {
		t.Fatalf("%q", b.String())
	}
}
