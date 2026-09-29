// Package api is vosd's HTTP server: routing, access control (sessions,
// CSRF, setup codes, Host/source-IP checks) and JSON helpers. Domain
// packages register their routes with Handle; the web package registers
// pages and static files with HandleRaw. See docs/CONTRACTS.md.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Access is who may call a route.
type Access int

const (
	Public Access = iota // anyone allowed by the source-IP/Host checks
	Authed               // a logged-in admin session (+ X-VOS-CSRF on non-GET)
	Setup                // the installer / first-run setup code
	Local                // loopback only
)

// authedOrSetup admits either an admin session or the setup code. Only the
// event stream uses it: the installer wizard and the dashboard both need it.
const authedOrSetup Access = -1

// Prefix is where every JSON route lives.
const Prefix = "/api/v1"

type Options struct {
	Addr      string // ":80"
	Installer bool   // live ISO: installer mode
	// Version is what /ping reports (the image version); "" means the
	// binary's own version.
	Version string
	// Ready, if set, is called once the listener is bound, before the
	// first request is served (vosd uses it for sd_notify and VOS-READY).
	Ready func(addr net.Addr)
}

type route struct {
	method, path string
	access       Access
	h            http.HandlerFunc
}

type Server struct {
	opts Options
	mux  *http.ServeMux

	mu          sync.RWMutex
	routes      []route
	code        string // setup code, "" when none is needed
	onCode      func(code string)
	allowPublic func() bool
	onActivity  func()
	setupMu     sync.Mutex // serialises POST /auth/setup

	lastActivity atomic.Int64 // unix nanoseconds

	sessions   *sessionStore
	loginLimit *limiter
	setupLimit *limiter
	hosts      hostCache

	// Seams for tests.
	now       func() time.Time
	localIPs  func() []string
	hub       *events.Hub
	heartbeat time.Duration
}

func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = ":80"
	}
	s := &Server{
		opts:      opts,
		mux:       http.NewServeMux(),
		now:       time.Now,
		localIPs:  sysd.LocalIPs,
		hub:       events.Default,
		heartbeat: 15 * time.Second,
	}
	now := func() time.Time { return s.now() }
	s.sessions = newSessionStore(config.SessionsPath(), now)
	s.loginLimit = newLimiter(now)
	s.setupLimit = newLimiter(now)
	s.registerCore()
	return s
}

func (s *Server) Options() Options { return s.opts }

// Handle registers a JSON route. path is relative to Prefix and may use
// ServeMux wildcards ("/sunshine/clients/{uuid}", read with r.PathValue).
func (s *Server) Handle(method, path string, access Access, h http.HandlerFunc) {
	s.mu.Lock()
	s.routes = append(s.routes, route{method, path, access, h})
	s.mu.Unlock()
	s.mux.HandleFunc(method+" "+Prefix+path, s.guard(access, false, h))
}

// HandleRaw registers a non-API handler (pages, static assets) with a
// ServeMux pattern such as "GET /static/". An unauthenticated GET of an
// Authed page is redirected to /login instead of getting a JSON 401.
func (s *Server) HandleRaw(pattern string, access Access, h http.Handler) {
	s.mux.Handle(pattern, s.guard(access, true, h.ServeHTTP))
}

// SetupCode is the code shown on the welcome screen and serial console that
// unlocks Setup routes; "" when none is needed.
func (s *Server) SetupCode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.code
}

// SetSetupCode is called by the daemon at startup. The api itself clears the
// code once first-run setup has created the admin password; the hook set
// with OnSetupCodeChange hears about every change.
func (s *Server) SetSetupCode(code string) {
	s.mu.Lock()
	changed := s.code != code
	s.code = code
	hook := s.onCode
	s.mu.Unlock()
	if changed && hook != nil {
		hook(code)
	}
}

// OnSetupCodeChange registers f to run (outside any lock) whenever the setup
// code changes, so the welcome screen and serial line can follow.
func (s *Server) OnSetupCodeChange(f func(code string)) {
	s.mu.Lock()
	s.onCode = f
	s.mu.Unlock()
}

// SetAllowPublic installs the web.allow_public lookup. It runs on every
// request from a non-local address, so config edits apply without a restart;
// it must be cheap. Without it, public addresses are always refused.
func (s *Server) SetAllowPublic(f func() bool) {
	s.mu.Lock()
	s.allowPublic = f
	s.mu.Unlock()
}

// OnActivity registers f to run on every request made by a logged-in admin or
// the setup wizard (vosd hands it to the idle-shutdown policy). Public
// requests such as /ping or the welcome screen's polling do not count.
func (s *Server) OnActivity(f func()) {
	s.mu.Lock()
	s.onActivity = f
	s.mu.Unlock()
}

// LastActivity is when the web UI was last used (see OnActivity); the zero
// time if never since vosd started.
func (s *Server) LastActivity() time.Time {
	n := s.lastActivity.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func (s *Server) touch() {
	s.lastActivity.Store(s.now().UnixNano())
	s.mu.RLock()
	f := s.onActivity
	s.mu.RUnlock()
	if f != nil {
		f()
	}
}

func (s *Server) publicAllowed() bool {
	s.mu.RLock()
	f := s.allowPublic
	s.mu.RUnlock()
	return f != nil && f()
}

// guard applies per-route access control (see middleware.go) before h.
func (s *Server) guard(access Access, raw bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r, ok := s.allow(w, r, access, raw)
		if !ok {
			return
		}
		h(w, r)
	}
}

// Handler is the full server: request-wide middleware around the routes.
func (s *Server) Handler() http.Handler { return s.wrap(s.mux) }

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx is cancelled, then shuts down gracefully.
// Request contexts derive from ctx, so long-lived event streams end with it
// instead of holding shutdown open.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var fresh freshConns
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ConnState:         fresh.track,
	}
	if s.opts.Ready != nil {
		s.opts.Ready(ln.Addr())
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shut := make(chan error, 1)
	go func() { shut <- srv.Shutdown(shutCtx) }()
	// Browsers and HTTP clients open spare connections that never carry a
	// request, and Shutdown only counts such a connection as idle once it
	// is 5 s old. Nothing is in flight on them, so close them now.
	fresh.closeAll()
	if err := <-shut; err != nil {
		srv.Close()
	}
	<-errc
	return nil
}

// freshConns tracks connections that have not started a request yet.
type freshConns struct {
	mu sync.Mutex
	m  map[net.Conn]struct{}
}

func (f *freshConns) track(c net.Conn, st http.ConnState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st == http.StateNew {
		if f.m == nil {
			f.m = map[net.Conn]struct{}{}
		}
		f.m[c] = struct{}{}
		return
	}
	delete(f.m, c)
}

func (f *freshConns) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.m {
		c.Close()
	}
}

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: writing response: %v", err)
	}
}

// ReadJSON decodes a request body of at most 1 MiB into v.
func ReadJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

// Error writes {"error": msg}.
func Error(w http.ResponseWriter, status int, format string, args ...any) {
	WriteJSON(w, status, map[string]string{"error": strings.TrimSpace(fmt.Sprintf(format, args...))})
}

// OK writes {}.
func OK(w http.ResponseWriter) { WriteJSON(w, http.StatusOK, struct{}{}) }
