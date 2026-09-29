package sunshine

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// Routes registers /sunshine/*.
func (s *Service) Routes(srv *api.Server) {
	srv.Handle("GET", "/sunshine", api.Authed, s.handleStatus)
	srv.Handle("POST", "/sunshine/pair", api.Authed, s.handlePair)
	srv.Handle("GET", "/sunshine/clients", api.Authed, s.handleClients)
	srv.Handle("DELETE", "/sunshine/clients/{uuid}", api.Authed, s.handleUnpair)
	srv.Handle("GET", "/sunshine/settings", api.Authed, s.handleGetSettings)
	srv.Handle("PUT", "/sunshine/settings", api.Authed, s.handlePutSettings)
	srv.Handle("GET", "/sunshine/logs", api.Authed, s.handleLogs)
	srv.Handle("POST", "/sunshine/restart", api.Authed, s.handleRestart)
}

// apiError answers for a failed Sunshine API call.
func (s *Service) apiError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnauthorized) {
		// The poll loop meets the same rejection within pollInterval and
		// re-applies the credentials (restarting Sunshine) from there.
		api.Error(w, http.StatusServiceUnavailable, "Sunshine is getting new credentials from VaporOS; try again in a few seconds")
		return
	}
	api.Error(w, http.StatusBadGateway, "Sunshine is not answering: %v", err)
}

func (s *Service) requireAPI(w http.ResponseWriter) *Client {
	cl := s.api()
	if cl == nil {
		api.Error(w, http.StatusServiceUnavailable, "Sunshine is still being set up; try again in a few seconds")
	}
	return cl
}

type statusResponse struct {
	Running        bool         `json:"running"`
	Version        string       `json:"version"`
	Streaming      bool         `json:"streaming"`
	Session        *sessionInfo `json:"session"`
	PendingPairing bool         `json:"pending_pairing"`
	Pairings       []Pairing    `json:"pairings"` // who is waiting, for choosing one in POST /sunshine/pair
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := statusResponse{Running: s.unitActive(ctx), Streaming: s.streaming(ctx), Pairings: []Pairing{}}
	s.mu.Lock()
	cached, version, session := s.pairings, s.version, s.session
	s.mu.Unlock()
	if cl := s.api(); cl != nil && st.Running {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if ps, err := cl.PendingPairings(pctx); err == nil {
			cached = ps
		}
		if version == "" {
			if v, err := cl.Version(pctx); err == nil {
				version = v
				s.mu.Lock()
				s.version = v
				s.mu.Unlock()
			}
		}
		cancel()
	}
	if cached != nil {
		st.Pairings = cached
	}
	st.PendingPairing = len(st.Pairings) > 0
	if version == "" {
		version = s.installedVersion() // Sunshine is down or not ours to ask yet
	}
	st.Version = version
	if st.Streaming && session != nil {
		sess := *session
		st.Session = &sess
	}
	api.WriteJSON(w, http.StatusOK, st)
}

var pinRe = regexp.MustCompile(`^[0-9]{4}$`)

// handlePair enters the PIN Moonlight shows. Current Sunshine pairs one
// waiting client at a time by id; when the request names none, the only
// waiting client is used.
func (s *Service) handlePair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN       string `json:"pin"`
		Name      string `json:"name"`
		PairingID string `json:"pairing_id"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	pin := strings.TrimSpace(req.PIN)
	name := strings.TrimSpace(req.Name)
	switch {
	case !pinRe.MatchString(pin):
		api.Error(w, http.StatusBadRequest, "the PIN is the 4 digits Moonlight shows")
		return
	case len(name) > 128 || strings.ContainsAny(name, "\x00\r\n"):
		api.Error(w, http.StatusBadRequest, "the device name must be at most 128 characters on one line")
		return
	case req.PairingID != "" && !pairingIDRe.MatchString(req.PairingID):
		api.Error(w, http.StatusBadRequest, "invalid pairing_id")
		return
	}
	cl := s.requireAPI(w)
	if cl == nil {
		return
	}
	ctx := r.Context()
	id := req.PairingID
	if id == "" {
		ps, err := cl.PendingPairings(ctx)
		switch {
		case errors.Is(err, errNotSupported):
			// Sunshine from before pairing ids: the PIN alone pairs.
		case err != nil:
			s.apiError(w, err)
			return
		case len(ps) == 0:
			api.Error(w, http.StatusConflict, "no device is waiting to pair: start pairing in Moonlight, then enter the PIN it shows")
			return
		case len(ps) > 1:
			api.Error(w, http.StatusConflict, "%d devices are waiting to pair; choose which one this PIN is for", len(ps))
			return
		default:
			id = ps[0].ID
			if name == "" {
				name = strings.TrimSpace(ps[0].Name)
			}
		}
	}
	if name == "" {
		name = "Moonlight"
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ok, err := cl.Pair(pctx, id, pin, name)
	if err != nil {
		s.apiError(w, err)
		return
	}
	if !ok {
		api.Error(w, http.StatusBadRequest, "pairing failed: check the PIN and try again")
		return
	}
	api.OK(w)
}

func (s *Service) handleClients(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAPI(w)
	if cl == nil {
		return
	}
	clients, err := cl.Clients(r.Context())
	if err != nil {
		s.apiError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

var clientUUIDRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

func (s *Service) handleUnpair(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	if !clientUUIDRe.MatchString(uuid) {
		api.Error(w, http.StatusBadRequest, "invalid client uuid")
		return
	}
	cl := s.requireAPI(w)
	if cl == nil {
		return
	}
	ok, err := cl.Unpair(r.Context(), uuid)
	if err != nil {
		s.apiError(w, err)
		return
	}
	if !ok {
		api.Error(w, http.StatusNotFound, "no paired client %s", uuid)
		return
	}
	api.OK(w)
}

// settingsView answers GET and PUT /sunshine/settings: the settings plus
// what the web UI offers for them. PUT ignores choices.
type settingsView struct {
	Settings
	Choices settingsChoices `json:"choices"`
}

type settingsChoices struct {
	Encoder        []string `json:"encoder"`
	Gamepad        []string `json:"gamepad"`
	BitrateKbpsMax intRange `json:"bitrate_kbps_max"`
}

type intRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

func viewSettings(set Settings) settingsView {
	return settingsView{set, settingsChoices{
		Encoder:        offeredEncoders,
		Gamepad:        gamepads,
		BitrateKbpsMax: intRange{0, maxBitrateKbps},
	}}
}

func (s *Service) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, viewSettings(s.currentSettings()))
}

// handlePutSettings changes any of the whitelisted settings, rewrites
// sunshine.conf and restarts Sunshine (after the stream, if one is live).
func (s *Service) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Encoder        *string `json:"encoder"`
		BitrateKbpsMax *int    `json:"bitrate_kbps_max"`
		AudioSink      *string `json:"audio_sink"`
		Gamepad        *string `json:"gamepad"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	prev := s.currentSettings()
	set := prev
	if req.Encoder != nil {
		set.Encoder = *req.Encoder
	}
	if req.BitrateKbpsMax != nil {
		set.BitrateKbpsMax = *req.BitrateKbpsMax
	}
	if req.AudioSink != nil {
		set.AudioSink = strings.TrimSpace(*req.AudioSink)
	}
	if req.Gamepad != nil {
		set.Gamepad = *req.Gamepad
	}
	if err := set.Validate(); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.mu.Lock()
	s.settings = set
	s.mu.Unlock()
	changed, err := s.writeConf()
	if err != nil {
		s.mu.Lock()
		s.settings = prev
		s.mu.Unlock()
		api.Error(w, http.StatusInternalServerError, "cannot write %s: %v", confPath(), err)
		return
	}
	if changed {
		if err := s.requestRestart(r.Context()); err != nil {
			api.Error(w, http.StatusInternalServerError, "settings saved, but Sunshine did not restart: %v", err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, viewSettings(set))
}

// handleLogs serves Sunshine's recent log as text: from its API, else its
// log file (also when it is down), else the journal.
func (s *Service) handleLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	text, err := "", errors.New("no API client")
	if cl := s.api(); cl != nil {
		text, err = cl.Logs(ctx)
	}
	if err != nil {
		if b, ferr := readRegular(logPath()); ferr == nil {
			text, err = string(b), nil
		}
	}
	if err != nil {
		text, err = s.journalTail(ctx, logLines)
	}
	if err != nil {
		api.Error(w, http.StatusBadGateway, "no Sunshine log available: %v", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(tailLines(text, logLines)))
}

// tailLines returns the last n lines of text.
func tailLines(text string, n int) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	end := len(text)
	for i := 0; i < n; i++ {
		j := strings.LastIndexByte(text[:end], '\n')
		if j < 0 {
			return text + "\n"
		}
		end = j
	}
	return text[end+1:] + "\n"
}

func (s *Service) handleRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.restart(r.Context()); err != nil {
		api.Error(w, http.StatusInternalServerError, "cannot restart Sunshine: %v", err)
		return
	}
	api.OK(w)
}
