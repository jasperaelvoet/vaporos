package api

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

// registerCore adds the routes the api package serves itself.
func (s *Server) registerCore() {
	s.Handle("GET", "/ping", Public, s.handlePing)
	s.Handle("GET", "/auth/me", Public, s.handleMe)
	s.Handle("POST", "/auth/login", Public, s.handleLogin)
	s.Handle("POST", "/auth/logout", Authed, s.handleLogout)
	s.Handle("POST", "/auth/setup", Setup, s.handleSetup)
	s.Handle("POST", "/auth/password", Authed, s.handlePassword)
	s.Handle("GET", "/events", authedOrSetup, s.handleEvents)
	// Everything else under the prefix answers in JSON, not ServeMux's text.
	s.mux.HandleFunc(Prefix+"/", s.guard(Public, false, s.handleNoRoute))
}

func (s *Server) mode() string {
	if s.opts.Installer {
		return "installer"
	}
	return "os"
}

func (s *Server) version() string {
	if s.opts.Version != "" {
		return s.opts.Version
	}
	return config.BinaryVersion
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": s.mode(), "version": s.version()})
}

// meResponse is GET /auth/me. needs_setup is true while no admin password
// exists (so also on the live ISO); the UI then goes to /setup.
type meResponse struct {
	Authenticated bool   `json:"authenticated"`
	CSRF          string `json:"csrf"`
	NeedsSetup    bool   `json:"needs_setup"`
	Installer     bool   `json:"installer"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	me := meResponse{NeedsSetup: !auth.HasAdmin(), Installer: s.opts.Installer}
	if info, ok := s.session(w, r); ok {
		me.Authenticated, me.CSRF = true, info.csrf
	}
	WriteJSON(w, http.StatusOK, me)
}

type passwordRequest struct {
	Password string `json:"password"`
}

// pwResult is the outcome of checkPassword.
type pwResult int

const (
	pwRefused pwResult = iota // not checked; the error response is written
	pwWrong
	pwRight
)

// checkPassword verifies password against the admin password under the
// login limit, which /auth/login and /auth/password share: a client gets
// one check at a time and none while locked out, and only
// maxPasswordChecks may wait for a hash across all clients. A wrong
// password counts toward the client's lockout and a right one clears it.
func (s *Server) checkPassword(w http.ResponseWriter, r *http.Request, password string) pwResult {
	key := limitKey(r)
	retry, ok := s.loginLimit.begin(key)
	if !ok {
		tooMany(w, retry)
		return pwRefused
	}
	defer s.loginLimit.end(key)
	select {
	case s.pwChecks <- struct{}{}:
		defer func() { <-s.pwChecks }()
	default:
		w.Header().Set("Retry-After", "2")
		Error(w, http.StatusServiceUnavailable, "VaporOS is busy checking other sign-ins; try again in a moment")
		return pwRefused
	}
	ok, err := s.verifyAdmin(r.Context(), password)
	switch {
	case errors.Is(err, auth.ErrNoAdmin):
		Error(w, http.StatusConflict, "no admin password is set yet; finish setup first")
		return pwRefused
	case err != nil:
		// The client went away while waiting for a hash slot; its password
		// was never checked, so there is nothing to count.
		Error(w, http.StatusServiceUnavailable, "the password was not checked: %v", err)
		return pwRefused
	case !ok:
		s.loginLimit.fail(key)
		return pwWrong
	}
	s.loginLimit.reset(key)
	return pwRight
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := ReadJSON(r, &req); err != nil {
		Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	switch s.checkPassword(w, r, req.Password) {
	case pwRefused:
		return
	case pwWrong:
		Error(w, http.StatusUnauthorized, "wrong password")
		return
	}
	// A session cookie the browser arrived with (possibly planted) does not
	// survive a login.
	if info, ok := s.session(nil, r); ok {
		s.sessions.delete(info.key)
	}
	csrf, err := s.startSession(w, r)
	if err != nil {
		Error(w, http.StatusInternalServerError, "starting session: %v", err)
		return
	}
	s.touch()
	WriteJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if info, ok := sessionFromContext(r.Context()); ok {
		s.sessions.delete(info.key)
	}
	clearCookie(w, r, sessionCookie)
	OK(w)
}

// handleSetup sets the first admin password on an installed system that has
// none (a CLI install). The live ISO refuses: its wizard sets the password
// of the machine being installed, not of the live system.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := ReadJSON(r, &req); err != nil {
		Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if s.opts.Installer {
		Error(w, http.StatusConflict, "the installer sets the admin password of the installed system")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if auth.HasAdmin() {
		Error(w, http.StatusConflict, "an admin password is already set")
		return
	}
	if err := auth.SetAdminPassword("", req.Password); err != nil {
		Error(w, http.StatusInternalServerError, "saving the password: %v", err)
		return
	}
	csrf, err := s.startSession(w, r)
	if err != nil {
		Error(w, http.StatusInternalServerError, "starting session: %v", err)
		return
	}
	clearCookie(w, r, setupCookie)
	// Setup is over: the code stops working and leaves the welcome screen.
	s.SetSetupCode("")
	log.Printf("api: admin password set by first-run setup")
	WriteJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}

type changePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := ReadJSON(r, &req); err != nil {
		Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	// A stolen session must not become a way to guess the password, so the
	// current-password check shares the login limit.
	switch s.checkPassword(w, r, req.Current) {
	case pwRefused:
		return
	case pwWrong:
		Error(w, http.StatusForbidden, "the current password is wrong")
		return
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := auth.SetAdminPassword("", req.New); err != nil {
		Error(w, http.StatusInternalServerError, "saving the password: %v", err)
		return
	}
	// Every other browser has to log in again with the new password.
	if info, ok := sessionFromContext(r.Context()); ok {
		s.sessions.deleteAllExcept(info.key)
	}
	OK(w)
}

// handleNoRoute answers unknown API paths: 405 when the path exists for
// another method, else 404.
func (s *Server) handleNoRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, Prefix)
	var allow []string
	s.mu.RLock()
	for _, rt := range s.routes {
		if matchPattern(rt.path, rest) {
			allow = append(allow, rt.method)
			if rt.method == http.MethodGet {
				allow = append(allow, http.MethodHead)
			}
		}
	}
	s.mu.RUnlock()
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		Error(w, http.StatusMethodNotAllowed, "method %s not allowed", r.Method)
		return
	}
	Error(w, http.StatusNotFound, "no such API endpoint")
}

// matchPattern reports whether path matches a ServeMux path pattern with
// {name} and {name...} wildcards (the subset Handle callers use).
func matchPattern(pattern, path string) bool {
	if exact, ok := strings.CutSuffix(pattern, "{$}"); ok {
		return matchPattern(strings.TrimSuffix(exact, "/"), strings.TrimSuffix(path, "/")) && strings.HasSuffix(path, "/")
	}
	if strings.HasSuffix(pattern, "/") && !strings.Contains(pattern, "{") {
		return strings.HasPrefix(path, pattern)
	}
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	xs := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range ps {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "...}") {
			return true
		}
		if i >= len(xs) {
			return false
		}
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			if xs[i] == "" {
				return false
			}
			continue
		}
		if p != xs[i] {
			return false
		}
	}
	return len(xs) == len(ps)
}
