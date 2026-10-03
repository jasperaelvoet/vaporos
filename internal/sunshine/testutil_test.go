package sunshine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		log.SetOutput(io.Discard)
	}
	os.Exit(m.Run())
}

// isolate points every config path this package uses into temp dirs.
func isolate(t *testing.T) {
	t.Helper()
	old := []string{config.StateDir, config.RunDir, config.ShareDir, config.GamerHome, config.ImageInfoPath, config.ProcCmdline}
	t.Cleanup(func() {
		config.StateDir, config.RunDir, config.ShareDir, config.GamerHome, config.ImageInfoPath, config.ProcCmdline =
			old[0], old[1], old[2], old[3], old[4], old[5]
	})
	config.StateDir = t.TempDir()
	config.RunDir = t.TempDir()
	config.ShareDir = t.TempDir()
	config.GamerHome = t.TempDir()
	config.ImageInfoPath = filepath.Join(t.TempDir(), "image.json")
	config.ProcCmdline = filepath.Join(t.TempDir(), "cmdline")
}

// fakeSunshine emulates the parts of Sunshine vosd uses: the HTTPS config
// API (with Basic auth and Sunshine's status conventions) and the plain
// HTTP serverinfo.
type fakeSunshine struct {
	mu        sync.Mutex
	user      string
	pass      string
	noCreds   bool // Sunshine without any web UI user: redirects to /welcome
	oldAPI    bool // Sunshine from before pairing ids
	pairings  []Pairing
	clients   []PairedClient
	requests  []string
	bodies    []map[string]string
	badHeader []string
	closed    int
	busy      bool
	logs      string
	pinDelay  time.Duration
}

func (f *fakeSunshine) api(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	for _, h := range []string{"Origin", "Referer"} {
		if r.Header.Get(h) != "" {
			f.badHeader = append(f.badHeader, h)
		}
	}
	if f.noCreds {
		http.Redirect(w, r, "/welcome", http.StatusTemporaryRedirect)
		return
	}
	if u, p, ok := r.BasicAuth(); !ok || u != f.user || p != f.pass {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"status_code":401,"error":"Unauthorized"}`)
		return
	}
	var body map[string]string
	if r.Method == "POST" && r.URL.Path != "/api/apps/close" && r.URL.Path != "/api/restart" {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"status_code":400,"status":false,"error":"Content type mismatch"}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	switch r.Method + " " + r.URL.Path {
	case "GET /api/pin":
		if f.oldAPI {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"status_code": 404, "error": "Not Found"})
			return
		}
		reply(map[string]any{"pairings": f.pairings})
	case "POST /api/pin":
		if f.pinDelay > 0 {
			f.mu.Unlock()
			time.Sleep(f.pinDelay)
			f.mu.Lock()
		}
		if !f.oldAPI && len(body["pairing_id"]) != 32 {
			w.WriteHeader(http.StatusBadRequest)
			reply(map[string]any{"status": false, "error": "pairing_id must contain exactly 32 hexadecimal characters"})
			return
		}
		reply(map[string]any{"status": body["pin"] == "1234"})
	case "GET /api/clients/list":
		certs := []map[string]any{}
		for _, c := range f.clients {
			certs = append(certs, map[string]any{"name": c.Name, "uuid": c.UUID, "enabled": c.Enabled})
		}
		reply(map[string]any{"named_certs": certs, "status": true})
	case "POST /api/clients/unpair":
		found := false
		for i, c := range f.clients {
			if c.UUID == body["uuid"] {
				f.clients = append(f.clients[:i], f.clients[i+1:]...)
				found = true
				break
			}
		}
		reply(map[string]any{"status": found})
	case "GET /api/config":
		reply(map[string]any{"status": true, "platform": "linux", "version": "2026.928.101500", "encoder": "vulkan"})
	case "GET /api/logs":
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, f.logs)
	case "POST /api/apps/close":
		f.closed++
		reply(map[string]any{"status": true})
	case "POST /api/restart":
		reply(map[string]any{"status": true})
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"status_code": 404, "error": "Not Found"})
	}
}

func (f *fakeSunshine) serverinfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	state := "SUNSHINE_SERVER_FREE"
	if f.busy {
		state = "SUNSHINE_SERVER_BUSY"
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, serverInfoXML, state)
}

func (f *fakeSunshine) setBusy(b bool) {
	f.mu.Lock()
	f.busy = b
	f.mu.Unlock()
}

func (f *fakeSunshine) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// serverInfoXML is Sunshine's serverinfo answer to an unpaired client.
const serverInfoXML = `<?xml version="1.0" encoding="utf-8"?>
<root status_code="200"><hostname>vapor</hostname><appversion>7.1.431.-1</appversion><GfeVersion>3.23.0.74</GfeVersion><uniqueid>0123456789ABCDEF</uniqueid><HttpsPort>47984</HttpsPort><ExternalPort>47989</ExternalPort><MaxLumaPixelsHEVC>1869449984</MaxLumaPixelsHEVC><mac>a8:a1:59:00:00:01</mac><LocalIP>192.168.1.50</LocalIP><ServerCodecModeSupport>259</ServerCodecModeSupport><PairStatus>0</PairStatus><currentgame>0</currentgame><state>%s</state></root>`

// recorder collects what the Service does to the system.
type recorder struct {
	mu        sync.Mutex
	systemctl []string
	asGamer   [][]string
	events    []events.Event
	failCtl   error
}

func (r *recorder) ctl(_ context.Context, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.systemctl = append(r.systemctl, strings.Join(args, " "))
	return r.failCtl
}

func (r *recorder) gamer(_ context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asGamer = append(r.asGamer, append([]string{name}, args...))
	return "", nil
}

func (r *recorder) publish(topic string, data any) {
	b, _ := json.Marshal(data)
	r.mu.Lock()
	r.events = append(r.events, events.Event{Topic: topic, Data: b})
	r.mu.Unlock()
}

func (r *recorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.systemctl...)
}

func (r *recorder) topics() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Topic)
	}
	return out
}

type harness struct {
	s   *Service
	f   *fakeSunshine
	rec *recorder
	api *httptest.Server
}

// newHarness builds a Service wired to a fake Sunshine, with the fake's
// TLS certificate in place as Sunshine's cacert.pem.
func newHarness(t *testing.T) *harness {
	t.Helper()
	isolate(t)
	f := &fakeSunshine{user: "vosd", pass: "0123456789abcdef0123", logs: "line 1\nline 2\n"}
	apiSrv := httptest.NewTLSServer(http.HandlerFunc(f.api))
	t.Cleanup(apiSrv.Close)
	infoSrv := httptest.NewServer(http.HandlerFunc(f.serverinfo))
	t.Cleanup(infoSrv.Close)
	writePEM(t, certPath(), apiSrv.Certificate().Raw)

	cfg := config.Defaults()
	cfg.Display.VirtualConnector = "DP-1"
	rec := &recorder{}
	s := NewService(cfg)
	s.apiBase = apiSrv.URL
	s.infoURL = infoSrv.URL + "/serverinfo"
	s.sysDRM = t.TempDir()
	s.pacmanDB = t.TempDir()
	s.publish = rec.publish
	s.userSystemctl = rec.ctl
	s.asGamer = rec.gamer
	s.unitActive = func(context.Context) bool { return true }
	s.gpuSupported = func() bool { return true }
	s.games = func() []steam.App { return nil }
	s.follow = func(context.Context) (<-chan string, error) { return nil, fmt.Errorf("no journal in tests") }
	s.journalTail = func(context.Context, int) (string, error) { return "", fmt.Errorf("no journal in tests") }
	s.neighbourMAC = func(netip.Addr) string { return "" }
	s.setCreds(apiCreds{User: f.user, Password: f.pass})
	return &harness{s: s, f: f, rec: rec, api: apiSrv}
}

func writePEM(t *testing.T, path string, der []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// otherCert returns a self-signed certificate that is not the fake's.
func otherCert(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Sunshine Gamestream Host"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
