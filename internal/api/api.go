// Package api is vosd's HTTP server: routing, access control (sessions,
// CSRF, setup codes, Host/source-IP checks) and JSON helpers. Domain
// packages register their routes with Handle; the web package registers
// pages and static files with HandleRaw. See docs/CONTRACTS.md.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Access is who may call a route.
type Access int

const (
	Public Access = iota // anyone allowed by the source-IP/Host checks
	Authed               // a logged-in admin session (+ X-VOS-CSRF on non-GET)
	Setup                // the installer / first-run setup code
	Local                // loopback only
)

// Prefix is where every JSON route lives.
const Prefix = "/api/v1"

type Options struct {
	Addr      string // ":80"
	Installer bool   // live ISO: installer mode
}

type route struct {
	method, path string
	access       Access
	h            http.HandlerFunc
}

type Server struct {
	opts   Options
	mux    *http.ServeMux
	routes []route
	code   string // setup code, "" when none is needed
}

func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = ":80"
	}
	return &Server{opts: opts, mux: http.NewServeMux()}
}

func (s *Server) Options() Options { return s.opts }

// Handle registers a JSON route. path is relative to Prefix and may use
// ServeMux wildcards ("/sunshine/clients/{uuid}", read with r.PathValue).
func (s *Server) Handle(method, path string, access Access, h http.HandlerFunc) {
	s.routes = append(s.routes, route{method, path, access, h})
	s.mux.HandleFunc(method+" "+Prefix+path, s.guard(access, h))
}

// HandleRaw registers a non-API handler (pages, static assets) with a
// ServeMux pattern such as "GET /static/".
func (s *Server) HandleRaw(pattern string, access Access, h http.Handler) {
	s.mux.Handle(pattern, s.guard(access, h.ServeHTTP))
}

// SetupCode is the code shown on the welcome screen and serial console that
// unlocks Setup routes; "" when none is needed.
func (s *Server) SetupCode() string { return s.code }

// SetSetupCode is called by the daemon at startup.
func (s *Server) SetSetupCode(code string) { s.code = code }

// guard applies access control. The full implementation (sessions, CSRF,
// Host allowlist, source-IP policy, rate limits) lives in middleware.go.
func (s *Server) guard(access Access, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.allow(w, r, access) {
			return
		}
		h(w, r)
	}
}

func (s *Server) Handler() http.Handler { return s.wrap(s.mux) }

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.opts.Addr, Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
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
