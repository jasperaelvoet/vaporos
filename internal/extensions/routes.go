package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// ccState is the control center's side of the service: the API's changes,
// the document's publisher and the auto-restart.
type ccState struct {
	change sync.Mutex    // one change through the API at a time
	dirty  chan struct{} // the document may have changed

	descMu sync.Mutex
	descs  map[string]*descriptor.Descriptor // shipped descriptors, nil for none

	// Under Service.mu.
	notes   map[string]string // by id: why its helper did not finish setting it up or removing it
	tries   map[string]int    // by id: its helper's installs this boot
	helpers int               // helper installs under way

	auto autoState

	// Seams; NewService wires the real ones.
	publish   func(topic string, data any)
	reauth    func(w http.ResponseWriter, r *http.Request, password string) bool
	systemctl func(ctx context.Context, user bool, args ...string) error
	asGamer   func(ctx context.Context, name string, args ...string) (string, error)
}

func newCCState(publish func(string, any)) ccState {
	return ccState{
		dirty:   make(chan struct{}, 1),
		notes:   map[string]string{},
		tries:   map[string]int{},
		publish: publish,
		systemctl: func(ctx context.Context, user bool, args ...string) error {
			if user {
				return sysd.UserSystemctl(ctx, args...)
			}
			return sysd.Systemctl(ctx, args...)
		},
		asGamer: sysd.AsGamer,
		auto:    newAutoState(),
	}
}

// helperTimeout bounds a helper's Install, Remove or Action. A variable for
// tests.
var helperTimeout = 10 * time.Minute

// Routes registers /extensions* (docs/CONTRACTS.md "HTTP API").
func (s *Service) Routes(srv *api.Server) {
	s.cc.reauth = srv.Reauth
	srv.Handle("GET", "/extensions", api.Authed, s.handleGet)
	srv.Handle("POST", "/extensions/skip-once", api.Authed, s.handleSkipOnce)
	srv.Handle("POST", "/extensions/{id}", api.Authed, s.handleInstall)
	srv.Handle("DELETE", "/extensions/{id}", api.Authed, s.handleRemove)
	srv.Handle("PUT", "/extensions/{id}/settings", api.Authed, s.handleSettings)
	srv.Handle("POST", "/extensions/{id}/actions/{name}", api.Authed, s.handleAction)
	srv.Handle("POST", "/extensions/{id}/retry", api.Authed, s.handleRetry)
}

// refusal is a change the API refuses, with its status and words.
type refusal struct {
	code int
	msg  string
}

func (e *refusal) Error() string { return e.msg }

func refuse(code int, msg string) error { return &refusal{code, msg} }

// errAnswered means the password check already answered the request.
var errAnswered = errors.New("answered")

// answer writes the document after a change, or why it was refused.
func (s *Service) answer(w http.ResponseWriter, r *http.Request, err error) {
	var ref *refusal
	switch {
	case err == nil:
		s.changed()
		api.WriteJSON(w, http.StatusOK, s.Document(r.Context()))
	case errors.Is(err, errAnswered):
	case errors.As(err, &ref):
		api.Error(w, ref.code, "%s", ref.msg)
	default:
		log.Printf("extensions: %s %s: %v", r.Method, r.URL.Path, err)
		api.Error(w, http.StatusInternalServerError, "%v", err)
	}
}

// readBody decodes an optional JSON body: none is {}.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.ReadJSON(r, v); err != nil && !errors.Is(err, io.EOF) {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return false
	}
	return true
}

// password returns the check a change runs when it needs the admin
// password: none given is 403, a wrong one 403 under the login limit.
func (s *Service) password(w http.ResponseWriter, r *http.Request, given, what string) func() error {
	return func() error {
		if given == "" {
			return refuse(http.StatusForbidden, what+" needs the admin password")
		}
		if s.cc.reauth == nil || !s.cc.reauth(w, r, given) {
			if s.cc.reauth == nil {
				return refuse(http.StatusForbidden, "the password is wrong")
			}
			return errAnswered
		}
		return nil
	}
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, s.Document(r.Context()))
}

func (s *Service) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options  map[string]any `json:"options"`
		Password string         `json:"password"`
	}
	if !readBody(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	s.answer(w, r, s.Install(r.Context(), id, req.Options, s.password(w, r, req.Password, "Adding "+s.name(id))))
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	purge := false
	switch r.URL.Query().Get("purge") {
	case "", "0":
	case "1":
		purge = true
	default:
		api.Error(w, http.StatusBadRequest, "purge must be 0 or 1")
		return
	}
	s.answer(w, r, s.Remove(r.Context(), r.PathValue("id"), purge))
}

func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Settings map[string]any `json:"settings"`
		Password string         `json:"password"`
	}
	if !readBody(w, r, &req) {
		return
	}
	s.answer(w, r, s.SetSettings(r.Context(), r.PathValue("id"), req.Settings,
		s.password(w, r, req.Password, "Changing this setting")))
}

func (s *Service) handleAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Args json.RawMessage `json:"args"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if len(req.Args) > 0 && req.Args[0] != '{' {
		api.Error(w, http.StatusBadRequest, "args must be an object")
		return
	}
	// The action outlives a page that goes away; helperTimeout bounds it.
	ctx := context.WithoutCancel(r.Context())
	s.answer(w, r, s.Action(ctx, r.PathValue("id"), r.PathValue("name"), req.Args))
}

func (s *Service) handleRetry(w http.ResponseWriter, r *http.Request) {
	s.answer(w, r, s.Retry(r.Context(), r.PathValue("id")))
}

// handleSkipOnce makes the next boot start without extensions; the
// initramfs removes the flag.
func (s *Service) handleSkipOnce(w http.ResponseWriter, r *http.Request) {
	if err := config.WriteFileAtomic(config.ExtSkipOncePath(), []byte("1\n"), 0o644); err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	log.Printf("extensions: the next start mounts no extension (skip-once)")
	api.OK(w)
}

// name is id's name for messages: its descriptor's, else the id.
func (s *Service) name(id string) string {
	if d := s.desc(id); d != nil {
		return d.Name
	}
	return id
}
