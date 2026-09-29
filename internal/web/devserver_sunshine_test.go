package web

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/sunshine"
)

// The dev server's fake of Sunshine (internal/sunshine): pairing with a
// PIN, paired devices, stream settings, the log, restarting and ending a
// stream. Documents: base/sunshine.json, sunshine-clients.json,
// sunshine-settings.json and sunshine-logs.json (lines). Moonlight's PIN
// is always 1234 here.

const devPIN = "1234"

// The request checks of sunshine/handlers.go and client.go.
var (
	fakePINRe       = regexp.MustCompile(`^[0-9]{4}$`)
	fakePairingIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)
	fakeClientRe    = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
)

func (f *devFake) sunshineRoutes(add fakeAdder) {
	add("GET", "/sunshine", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.sunshineAnswerLocked() })
	add("POST", "/sunshine/pair", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			PIN       string `json:"pin"`
			Name      string `json:"name"`
			PairingID string `json:"pairing_id"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		pin, name := strings.TrimSpace(req.PIN), strings.TrimSpace(req.Name)
		switch {
		case !fakePINRe.MatchString(pin):
			api.Error(w, http.StatusBadRequest, "the PIN is the 4 digits Moonlight shows")
			return nil
		case len(name) > 128 || strings.ContainsAny(name, "\x00\r\n"):
			api.Error(w, http.StatusBadRequest, "the device name must be at most 128 characters on one line")
			return nil
		case req.PairingID != "" && !fakePairingIDRe.MatchString(req.PairingID):
			api.Error(w, http.StatusBadRequest, "invalid pairing_id")
			return nil
		}
		s := f.doc("sunshine")
		ps := asList(s["pairings"])
		id := req.PairingID
		if id == "" {
			switch len(ps) {
			case 0:
				api.Error(w, http.StatusConflict, "no device is waiting to pair: start pairing in Moonlight, then enter the PIN it shows")
				return nil
			case 1:
				id = asStr(asObj(ps[0])["id"])
			default:
				api.Error(w, http.StatusConflict, "%d devices are waiting to pair; choose which one this PIN is for", len(ps))
				return nil
			}
		}
		var waiting map[string]any
		rest := []any{}
		for _, p := range ps {
			if asStr(asObj(p)["id"]) == id {
				waiting = asObj(p)
			} else {
				rest = append(rest, p)
			}
		}
		// Sunshine answers false for a wrong PIN and for a device that
		// stopped waiting alike.
		if waiting == nil || pin != devPIN {
			api.Error(w, http.StatusBadRequest, "pairing failed: check the PIN and try again")
			return nil
		}
		if name == "" {
			name = strings.TrimSpace(asStr(waiting["name"]))
		}
		if name == "" {
			name = "Moonlight"
		}
		c := f.doc("sunshine-clients")
		c["clients"] = append(asList(c["clients"]), map[string]any{"uuid": fakeUUID(), "name": name, "enabled": true})
		f.emitLocked("pairing.state", map[string]any{"pairings": rest})
		return fakeOK
	})
	add("GET", "/sunshine/clients", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		return f.doc("sunshine-clients")
	})
	add("DELETE", "/sunshine/clients/{uuid}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		uuid := r.PathValue("uuid")
		if !fakeClientRe.MatchString(uuid) {
			api.Error(w, http.StatusBadRequest, "invalid client uuid")
			return nil
		}
		c := f.doc("sunshine-clients")
		keep := []any{}
		for _, cl := range asList(c["clients"]) {
			if asStr(asObj(cl)["uuid"]) != uuid {
				keep = append(keep, cl)
			}
		}
		if len(keep) == len(asList(c["clients"])) {
			api.Error(w, http.StatusNotFound, "no paired client %s", uuid)
			return nil
		}
		c["clients"] = keep
		return fakeOK
	})
	add("GET", "/sunshine/settings", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		return f.doc("sunshine-settings")
	})
	add("PUT", "/sunshine/settings", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Encoder        *string `json:"encoder"`
			BitrateKbpsMax *int    `json:"bitrate_kbps_max"`
			AudioSink      *string `json:"audio_sink"`
			Gamepad        *string `json:"gamepad"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		doc := f.doc("sunshine-settings")
		set := sunshine.Settings{Encoder: asStr(doc["encoder"]), BitrateKbpsMax: int(asNum(doc["bitrate_kbps_max"])),
			AudioSink: asStr(doc["audio_sink"]), Gamepad: asStr(doc["gamepad"])}
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
			return nil
		}
		doc["encoder"], doc["bitrate_kbps_max"], doc["audio_sink"], doc["gamepad"] =
			set.Encoder, float64(set.BitrateKbpsMax), set.AudioSink, set.Gamepad
		return doc
	})
	add("GET", "/sunshine/logs", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var b strings.Builder
		lines := asList(f.docs["sunshine-logs"])
		if len(lines) > 2000 {
			lines = lines[len(lines)-2000:]
		}
		for _, l := range lines {
			b.WriteString(asStr(l))
			b.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(b.String()))
		return nil
	})
	add("POST", "/sunshine/restart", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		// Sunshine comes back: its API answers again.
		for k := range f.errs {
			if strings.Contains(k, " /sunshine") {
				delete(f.errs, k)
			}
		}
		f.doc("sunshine")["running"] = true
		return fakeOK
	})
	add("POST", "/sunshine/end-stream", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		if f.doc("sunshine")["streaming"] != true {
			api.Error(w, http.StatusConflict, "nothing is streaming")
			return nil
		}
		// Closing the app ends the stream; the prep-cmd undo ends the session.
		go func() {
			time.Sleep(500 * time.Millisecond)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.doc("sunshine")["streaming"] == true {
				f.emitLocked("session.end", struct{}{})
				f.emitLocked("display.changed", struct{}{})
			}
		}()
		return fakeOK
	})
}

// sunshineAnswerLocked is GET /sunshine: the session only while streaming.
func (f *devFake) sunshineAnswerLocked() map[string]any {
	s := cloneDoc(f.doc("sunshine"))
	ps := asList(s["pairings"])
	if ps == nil {
		ps = []any{}
	}
	s["pairings"], s["pending_pairing"] = ps, len(ps) > 0
	if s["streaming"] != true {
		s["session"] = nil
	}
	return s
}

// fakeUUID is a new client uuid in Sunshine's format.
func fakeUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]))
}
