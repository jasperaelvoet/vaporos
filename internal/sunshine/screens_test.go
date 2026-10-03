package sunshine

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeScreens records what the Service tells the display manager.
type fakeScreens struct {
	mu      sync.Mutex
	resumes int
	hints   []pairHint
	kinds   map[string]string // BrowserKind's answers, by address
	shared  map[string]bool   // SharedAddr's answers, by address
}

type pairHint struct{ remote, kind, ua string }

func (f *fakeScreens) NoteResume() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
}

func (f *fakeScreens) PairHint(remote net.IP, kind, ua string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hints = append(f.hints, pairHint{remote.String(), kind, ua})
}

func (f *fakeScreens) BrowserKind(remote net.IP) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kinds[remote.String()]
}

func (f *fakeScreens) SharedAddr(remote net.IP) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shared[remote.String()]
}

func (f *fakeScreens) resumed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resumes
}

func (f *fakeScreens) pairHints() []pairHint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pairHint(nil), f.hints...)
}

var (
	_ browserKinds = (*fakeScreens)(nil)
	_ sharedAddrs  = (*fakeScreens)(nil)
)

// plainScreens is a display manager that keeps no browser kinds.
type plainScreens struct{ f *fakeScreens }

func (p plainScreens) NoteResume()                             { p.f.NoteResume() }
func (p plainScreens) PairHint(remote net.IP, kind, ua string) { p.f.PairHint(remote, kind, ua) }

const (
	uaIPhone      = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	uaDesktopMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
	uaLinux       = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"
	uaCurl        = "curl/8.9.1"
	kindMustBe    = "kind must be phone, handheld, tablet, laptop, monitor or tv"
	waitingID     = "abababababababababababababababab"
	waitingAddr   = "192.168.1.40"
	waitingSource = waitingAddr + ":51234"
)

// pairFrom posts body to the pair handler as a browser at from.
func pairFrom(t *testing.T, h *harness, from, ua, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/sunshine/pair", strings.NewReader(body))
	r.RemoteAddr = from
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	w := httptest.NewRecorder()
	h.s.handlePair(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	msg, _ := out["error"].(string)
	return w.Code, msg
}

// sentName is the name the last PIN reached Sunshine with.
func (f *fakeSunshine) sentName() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.bodies) - 1; i >= 0; i-- {
		if pin, ok := f.bodies[i]["pin"]; ok && pin != "" {
			return f.bodies[i]["name"], true
		}
	}
	return "", false
}

func TestPairNamesTheDeviceAfterItsBrowser(t *testing.T) {
	const none = "-"
	// One phone at 192.168.1.40 and fd00::40; 192.168.1.41 is another
	// device behind a bridge that gives it the phone's MAC.
	macs := map[string]string{
		"192.168.1.40": "aa:bb:cc:dd:ee:01",
		"192.168.1.41": "aa:bb:cc:dd:ee:01",
		"fd00::40":     "aa:bb:cc:dd:ee:01",
		"fd00::41":     "aa:bb:cc:dd:ee:02",
	}
	cases := []struct {
		name     string
		waiting  string // Sunshine's name for the waiting device ("roth"; none for "")
		addr     string // its address (waitingAddr)
		from, ua string // the browser (waitingSource, uaIPhone; ua none for no header)
		body     string // {"pin":"1234"}
		paired   []string
		kinds    map[string]string // the browser kinds the display manager keeps
		shared   map[string]bool   // the addresses it saw several devices at
		plain    bool              // a display manager without browser kinds
		want     string
		hint     *pairHint
	}{
		{name: "same address", want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "IPv4-mapped source", from: "[::ffff:192.168.1.40]:51234", want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "IPv4-mapped waiting address", addr: "::ffff:192.168.1.40", want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "names taken", paired: []string{"iPhone", "iphone 2", "iPad"}, want: "iPhone 3", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "another device", from: "192.168.1.77:5000", want: "roth"},
		{name: "another IPv4 address behind the same MAC", from: "192.168.1.41:5000", want: "roth"},
		{name: "same MAC over IPv6", from: "[fd00::40]:5000", want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "another MAC over IPv6", from: "[fd00::41]:5000", want: "roth"},
		{name: "IPv6 without a MAC", from: "[fd00::99]:5000", want: "roth"},
		{name: "loopback", from: "127.0.0.1:5000", addr: "127.0.0.1", want: "roth"},
		{name: "typed name", body: `{"pin":"1234","name":"Living room"}`, want: "Living room", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "named in Moonlight", waiting: "Jasper-Deck", ua: uaLinux, want: "Jasper-Deck", hint: &pairHint{waitingAddr, "", uaLinux}},
		{name: "generic name in another case", waiting: "Moonlight", want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "Sunshine has no name", waiting: none, want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "a browser that says nothing", ua: uaCurl, want: "roth", hint: &pairHint{waitingAddr, "", uaCurl}},
		{name: "no User-Agent", ua: none, want: "roth"},
		{name: "no User-Agent, no name", ua: none, waiting: none, want: "Moonlight"},
		{name: "iPad on desktop sites, from its hint", ua: uaDesktopMac, kinds: map[string]string{waitingAddr: "tablet"},
			want: "iPad", hint: &pairHint{waitingAddr, "", uaDesktopMac}},
		{name: "iPad on desktop sites, from the pick", ua: uaDesktopMac, plain: true, body: `{"pin":"1234","kind":"tablet"}`,
			want: "iPad", hint: &pairHint{waitingAddr, "tablet", uaDesktopMac}},
		{name: "a Mac", ua: uaDesktopMac, plain: true, want: "Mac", hint: &pairHint{waitingAddr, "", uaDesktopMac}},
		{name: "the hint before the pick", ua: uaDesktopMac, kinds: map[string]string{waitingAddr: "laptop"}, body: `{"pin":"1234","kind":"tablet"}`,
			want: "Mac", hint: &pairHint{waitingAddr, "tablet", uaDesktopMac}},
		{name: "the hint seen over IPv6", from: "[fd00::40]:5000", ua: uaDesktopMac, kinds: map[string]string{"fd00::40": "tablet"},
			want: "iPad", hint: &pairHint{waitingAddr, "", uaDesktopMac}},
		{name: "Steam Deck", ua: uaLinux, kinds: map[string]string{waitingAddr: "handheld"}, want: "Steam Deck", hint: &pairHint{waitingAddr, "", uaLinux}},
		{name: "Linux PC", ua: uaLinux, want: "Linux PC", hint: &pairHint{waitingAddr, "", uaLinux}},
		{name: "kind picked on another device", from: "192.168.1.77:5000", body: `{"pin":"1234","kind":"tv"}`,
			want: "roth", hint: &pairHint{waitingAddr, "tv", ""}},
		{name: "kind picked on the device", body: `{"pin":"1234","kind":"phone"}`, want: "iPhone", hint: &pairHint{waitingAddr, "phone", uaIPhone}},
		// Behind a router that NATs several devices, the browser and the
		// device share the router's address and MAC, and may be two.
		{name: "an address several devices share", shared: map[string]bool{waitingAddr: true}, want: "roth"},
		{name: "a pick at such an address", shared: map[string]bool{waitingAddr: true}, body: `{"pin":"1234","kind":"tv"}`,
			want: "roth", hint: &pairHint{waitingAddr, "tv", ""}},
		{name: "such an address and the same MAC over IPv6", from: "[fd00::40]:5000", shared: map[string]bool{waitingAddr: true}, want: "roth"},
		{name: "a display manager that cannot tell", shared: map[string]bool{waitingAddr: true}, plain: true, want: "iPhone",
			hint: &pairHint{waitingAddr, "", uaIPhone}},
		{name: "chosen by id", body: `{"pin":"1234","pairing_id":"` + strings.ToUpper(waitingID) + `"}`, want: "iPhone", hint: &pairHint{waitingAddr, "", uaIPhone}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			def := func(v *string, d string) {
				switch *v {
				case "":
					*v = d
				case none:
					*v = ""
				}
			}
			def(&c.waiting, "roth")
			def(&c.addr, waitingAddr)
			def(&c.from, waitingSource)
			def(&c.ua, uaIPhone)
			def(&c.body, `{"pin":"1234"}`)
			h.f.pairings = []Pairing{
				{ID: waitingID, Name: c.waiting, Address: c.addr},
				{ID: strings.Repeat("cd", 16), Name: "roth", Address: "192.168.1.77"},
			}
			if !strings.Contains(c.body, "pairing_id") {
				h.f.pairings = h.f.pairings[:1]
			}
			for i, n := range c.paired {
				h.f.clients = append(h.f.clients, PairedClient{UUID: string(rune('A' + i)), Name: n, Enabled: true})
			}
			h.s.neighbourMAC = func(a netip.Addr) string { return macs[a.String()] }
			fs := &fakeScreens{kinds: c.kinds, shared: c.shared}
			if c.plain {
				h.s.SetScreens(plainScreens{fs})
			} else {
				h.s.SetScreens(fs)
			}

			if code, msg := pairFrom(t, h, c.from, c.ua, c.body); code != http.StatusOK {
				t.Fatalf("pair: %d %q", code, msg)
			}
			if got, ok := h.f.sentName(); !ok || got != c.want {
				t.Errorf("named %q, want %q", got, c.want)
			}
			var want []pairHint
			if c.hint != nil {
				want = []pairHint{*c.hint}
			}
			if got := fs.pairHints(); !reflect.DeepEqual(got, want) {
				t.Errorf("PairHint calls = %+v, want %+v", got, want)
			}
		})
	}
}

func TestPairKind(t *testing.T) {
	h := newHarness(t)
	h.f.pairings = []Pairing{{ID: waitingID, Name: "roth", Address: waitingAddr}}
	fs := &fakeScreens{}
	h.s.SetScreens(fs)
	for _, k := range []string{"auto", "unknown", "TV", " tv", "desktop", "phone\n"} {
		body, _ := json.Marshal(map[string]string{"pin": "1234", "kind": k})
		if code, msg := pairFrom(t, h, "192.168.1.77:5000", uaIPhone, string(body)); code != http.StatusBadRequest || msg != kindMustBe {
			t.Errorf("kind %q: %d %q", k, code, msg)
		}
	}
	if code, msg := pairFrom(t, h, "192.168.1.77:5000", uaIPhone, `{"pin":"12a4","kind":"auto"}`); code != http.StatusBadRequest || !strings.Contains(msg, "PIN") {
		t.Errorf("a bad PIN is named before a bad kind: %d %q", code, msg)
	}
	if _, sent := h.f.sentName(); sent || len(fs.pairHints()) != 0 {
		t.Fatalf("a refused request reached Sunshine or the display manager: %v", fs.pairHints())
	}

	for _, k := range []string{"phone", "handheld", "tablet", "laptop", "monitor", "tv"} {
		if code, msg := pairFrom(t, h, "192.168.1.77:5000", uaIPhone, `{"pin":"1234","kind":"`+k+`"}`); code != http.StatusOK {
			t.Errorf("kind %q: %d %q", k, code, msg)
		}
	}
	if got := fs.pairHints(); len(got) != 6 || got[5] != (pairHint{waitingAddr, "tv", ""}) {
		t.Errorf("PairHint calls = %+v", got)
	}
	// An empty kind is no pick, and with nothing to say the display
	// manager hears nothing.
	if code, _ := pairFrom(t, h, "192.168.1.77:5000", uaIPhone, `{"pin":"1234","kind":""}`); code != http.StatusOK || len(fs.pairHints()) != 6 {
		t.Errorf("empty kind: %d, %+v", code, fs.pairHints())
	}
}

func TestPairHintOnlyForAPairedDevice(t *testing.T) {
	h := newHarness(t)
	h.f.pairings = []Pairing{{ID: waitingID, Name: "roth", Address: waitingAddr}}
	fs := &fakeScreens{}
	h.s.SetScreens(fs)
	if code, _ := pairFrom(t, h, waitingSource, uaIPhone, `{"pin":"0000","kind":"phone"}`); code != http.StatusBadRequest {
		t.Errorf("wrong PIN: %d", code)
	}
	if got := fs.pairHints(); len(got) != 0 {
		t.Errorf("a failed pairing left a hint: %+v", got)
	}

	// Sunshine from before pairing ids names no address: nothing to key a
	// hint on, and nothing to compare the browser with.
	h.f.oldAPI = true
	if code, _ := pairFrom(t, h, waitingSource, uaIPhone, `{"pin":"1234","kind":"phone"}`); code != http.StatusOK {
		t.Errorf("old Sunshine: %d", code)
	}
	if name, _ := h.f.sentName(); name != "Moonlight" || len(fs.pairHints()) != 0 {
		t.Errorf("old Sunshine: named %q, hints %+v", name, fs.pairHints())
	}

	// Without a display manager, pairing and naming still work.
	h.f.oldAPI = false
	h.s.SetScreens(nil)
	if code, _ := pairFrom(t, h, waitingSource, uaIPhone, `{"pin":"1234","kind":"phone"}`); code != http.StatusOK {
		t.Errorf("no display manager: %d", code)
	}
	if name, _ := h.f.sentName(); name != "iPhone" {
		t.Errorf("no display manager: named %q", name)
	}
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.40":        "192.168.1.40",
		" 192.168.1.40 ":      "192.168.1.40",
		"::ffff:192.168.1.40": "192.168.1.40",
		"[fd00::40]":          "fd00::40",
		"fe80::1%eth0":        "fe80::1",
		"":                    "invalid IP",
		"phone.local":         "invalid IP",
		"192.168.1.40:47984":  "invalid IP",
	} {
		if got := parseAddr(in).String(); got != want {
			t.Errorf("parseAddr(%q) = %s, want %s", in, got, want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	for in, want := range map[string]string{
		"[::ffff:10.0.0.2]:80": "10.0.0.2",
		"[fe80::2%wlan0]:80":   "fe80::2",
		"10.0.0.3":             "10.0.0.3",
		"@":                    "invalid IP",
	} {
		r.RemoteAddr = in
		if got := peerAddr(r).String(); got != want {
			t.Errorf("peerAddr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	paired := func(names ...string) []PairedClient {
		var out []PairedClient
		for _, n := range names {
			out = append(out, PairedClient{Name: n})
		}
		return out
	}
	for _, c := range []struct {
		paired []PairedClient
		want   string
	}{
		{nil, "iPhone"},
		{paired("iPad", "roth"), "iPhone"},
		{paired("iPhone"), "iPhone 2"},
		{paired(" IPHONE ", "iPhone 2", "iPhone 4"), "iPhone 3"},
		{paired("iPhone 2"), "iPhone"},
	} {
		if got := uniqueName("iPhone", c.paired); got != c.want {
			t.Errorf("uniqueName among %v = %q, want %q", c.paired, got, c.want)
		}
	}
}
