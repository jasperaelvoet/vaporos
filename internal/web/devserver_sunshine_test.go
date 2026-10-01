package web

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
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
	if f.d != nil {
		// vosd publishes pairing.state once its first poll of Sunshine is
		// done (3 s after start), so every event stream replays who waits,
		// an empty list included.
		go func() {
			time.Sleep(time.Second)
			f.publishFirstPairingState()
		}()
	}
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
		f.emitLocked("sunshine.state", map[string]any{"running": true})
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

// publishFirstPairingState is the first poll's pairing.state, unless the
// preset (or anything else) published one already.
func (f *devFake) publishFirstPairingState() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.hub.Last() {
		if ev.Topic == "pairing.state" {
			return
		}
	}
	f.emitLocked("pairing.state", map[string]any{"pairings": f.sunshineAnswerLocked()["pairings"]})
}

// sunshineAnswerLocked is GET /sunshine: the session only while streaming,
// and nobody waiting while Sunshine is stopped (its API cannot say, and
// vosd's poll counts that as an empty list).
func (f *devFake) sunshineAnswerLocked() map[string]any {
	s := cloneDoc(f.doc("sunshine"))
	ps := asList(s["pairings"])
	if ps == nil || s["running"] == false {
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

// The fake's pairing.state follows vosd's: published after the first poll
// with whoever waits (an empty list too), never over a preset's own, and
// empty while Sunshine is stopped.
func TestFakePairingStateAtStart(t *testing.T) {
	fx, err := loadFixtures(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	last := func(f *devFake) (string, bool) {
		for _, ev := range f.hub.Last() {
			if ev.Topic == "pairing.state" {
				var v struct {
					Pairings []struct{ Name string } `json:"pairings"`
				}
				if err := json.Unmarshal(ev.Data, &v); err != nil || v.Pairings == nil {
					t.Fatalf("pairing.state %s: want {pairings:[…]}", ev.Data)
				}
				names := []string{}
				for _, p := range v.Pairings {
					names = append(names, p.Name)
				}
				return strings.Join(names, ","), true
			}
		}
		return "", false
	}
	world := func(name string) *devFake {
		p := fx.presets[name]
		docs, err := fx.docs(p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		f := newDevFake(nil, nil, p, docs, false)
		for _, ev := range p.Events {
			f.publishRaw(ev)
		}
		return f
	}
	for _, c := range []struct{ preset, want string }{
		{"idle", ""},
		{"pairing-2", "Steam Deck,Pixel 9"},
		{"sunshine-stopped", ""},
	} {
		f := world(c.preset)
		f.publishFirstPairingState()
		got, ok := last(f)
		if !ok || got != c.want {
			t.Errorf("%s: replayed pairing.state = %q (%v), want %q", c.preset, got, ok, c.want)
		}
	}
	// A preset's own list wins over the first poll's.
	f := world("pairing-1")
	f.mu.Lock()
	f.doc("sunshine")["pairings"] = []any{}
	f.mu.Unlock()
	f.publishFirstPairingState()
	if got, _ := last(f); got != "Steam Deck" {
		t.Errorf("pairing-1: the first poll replaced the preset's pairing.state with %q", got)
	}
	// Stopped, GET /sunshine says nobody waits, whatever the document holds.
	f = world("sunshine-stopped")
	f.mu.Lock()
	f.doc("sunshine")["pairings"] = []any{map[string]any{"id": "2e91a88f8967a1bd56be84a93e1fe055", "name": "Steam Deck"}}
	ps := asList(f.sunshineAnswerLocked()["pairings"])
	f.mu.Unlock()
	if len(ps) != 0 {
		t.Errorf("stopped: GET /sunshine lists %d waiting, want none", len(ps))
	}
}
