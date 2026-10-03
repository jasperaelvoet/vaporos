package steamui

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---- a /proc tree

// fakeProc is a /proc with net/tcp, net/tcp6 and processes whose fd
// links name sockets.
type fakeProc struct {
	t         *testing.T
	dir       string
	tcp, tcp6 []string
}

// newFakeProc is a box where pid 700, a steamwebhelper of the gaming
// user, listens on 127.0.0.1:port (socket 52114).
func newFakeProc(t *testing.T, port int) *fakeProc {
	p := &fakeProc{t: t, dir: t.TempDir()}
	p.process(700, "1000\t1000\t1000\t1000", "steamwebhelper", 41000, 52114)
	p.listen("127.0.0.1", port, 52114)
	p.write()
	return p
}

// process adds pid with that Uid line and comm, holding the sockets.
func (p *fakeProc) process(pid int, uids, comm string, inodes ...uint64) {
	p.t.Helper()
	dir := filepath.Join(p.dir, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		p.t.Fatal(err)
	}
	mustWrite(p.t, filepath.Join(dir, "comm"), comm+"\n")
	mustWrite(p.t, filepath.Join(dir, "status"), "Name:\t"+comm+"\nUmask:\t0022\nState:\tS (sleeping)\nUid:\t"+uids+"\nGid:\t1000\t1000\t1000\t1000\n")
	os.Symlink("/dev/null", filepath.Join(dir, "fd", "0"))
	os.Symlink("pipe:[777]", filepath.Join(dir, "fd", "1"))
	for i, ino := range inodes {
		if err := os.Symlink(fmt.Sprintf("socket:[%d]", ino), filepath.Join(dir, "fd", strconv.Itoa(i+3))); err != nil {
			p.t.Fatal(err)
		}
	}
}

// listen adds a LISTEN socket on ip:port, to tcp or tcp6 by the address.
func (p *fakeProc) listen(ip string, port int, inode uint64) {
	p.socket(ip, port, "0.0.0.0", 0, tcpListen, inode)
}

func (p *fakeProc) socket(ip string, port int, rip string, rport, state int, inode uint64) {
	a, r := netip.MustParseAddr(ip), netip.MustParseAddr(rip)
	if a.Is4() {
		p.tcp = append(p.tcp, tcpLine(len(p.tcp), a, port, r, rport, state, inode))
	} else {
		p.tcp6 = append(p.tcp6, tcpLine(len(p.tcp6), a, port, r, rport, state, inode))
	}
}

// tcpLine is a line as the kernel prints it: the address as its
// in-memory words in host order.
func tcpLine(sl int, a netip.Addr, port int, r netip.Addr, rport, state int, inode uint64) string {
	return fmt.Sprintf("%4d: %s:%04X %s:%04X %02X 00000000:00000000 00:00000000 00000000  1000        0 %d 1 0000000000000000 100 0 0 10 0",
		sl, hexAddr(a), port, hexAddr(r), rport, state, inode)
}

func hexAddr(a netip.Addr) string {
	b := a.AsSlice()
	var s strings.Builder
	for i := 0; i < len(b); i += 4 {
		fmt.Fprintf(&s, "%08X", binary.NativeEndian.Uint32(b[i:]))
	}
	return s.String()
}

func (p *fakeProc) reset() { p.tcp, p.tcp6 = nil, nil }

func (p *fakeProc) write() {
	p.t.Helper()
	const head4 = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	const head6 = "  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	mustWrite(p.t, filepath.Join(p.dir, "net", "tcp"), head4+lines(p.tcp))
	mustWrite(p.t, filepath.Join(p.dir, "net", "tcp6"), head6+lines(p.tcp6))
}

func lines(ls []string) string {
	if len(ls) == 0 {
		return ""
	}
	return strings.Join(ls, "\n") + "\n"
}

func mustWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- Steam's side of the expressions

// fakeUI is a Go copy of what the expressions in js.go do in Steam
// (TestFakeMatchesJS holds it to the real JavaScript in Node). Its
// setters also change the settings, as Steam's do.
type fakeUI struct {
	settings map[string]any // nil: settingsStore.settings is missing
	client   string         // "api", "bare" (SteamClient without the setters) or "none"
	marker   any            // window.__vosScale, nil when unset
	dpr, h   any            // a view's devicePixelRatio and innerHeight, nil when undefined
	calls    []string
}

func readyUI() *fakeUI {
	return &fakeUI{client: "api", settings: map[string]any{
		"strDisplayName":              `External: VaporOS 27"|||Windowed`,
		"bDisplayIsExternal":          true,
		"bDisplayIsUsingAutoScale":    true,
		"flCurrentDisplayScaleFactor": 1.71,
		"flAutoDisplayScaleFactor":    1.71,
		"flMinDisplayScaleFactor":     0.71,
		"flMaxDisplayScaleFactor":     3.41,
	}}
}

// eval answers one of the expressions; false for any other.
func (u *fakeUI) eval(expr string) (any, bool) {
	switch expr {
	case readJS:
		return u.read(), true
	case viewJS:
		return map[string]any{"dpr": numOr(u.dpr, 0), "h": numOr(u.h, 0)}, true
	}
	if a, ok := callArgs(expr, setJS); ok && len(a) == 2 {
		return u.set(a[0], a[1]), true
	}
	if a, ok := callArgs(expr, autoJS); ok && len(a) == 1 {
		return u.auto(a[0]), true
	}
	return nil, false
}

func callArgs(expr, fn string) ([]float64, bool) {
	rest, ok := strings.CutPrefix(expr, "("+fn+")(")
	if !ok {
		return nil, false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	var args []float64
	if !ok || json.Unmarshal([]byte("["+rest+"]"), &args) != nil {
		return nil, false
	}
	return args, true
}

// num is JavaScript's num(): a finite number or null.
func num(v any) any {
	if f, ok := v.(float64); ok && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return f
	}
	return nil
}

func numOr(v any, d float64) float64 {
	if f, ok := num(v).(float64); ok {
		return f
	}
	return d
}

type uiState struct {
	name    string
	sc, api bool
	ready   bool
	lo, hi  float64
	mgen    float64
	mk      map[string]any
}

func (u *fakeUI) state() uiState {
	s := uiState{sc: u.client != "none", api: u.client == "api"}
	s.name, _ = u.settings["strDisplayName"].(string)
	shown := strings.Replace(strings.Replace(s.name, "Internal: ", "", 1), "External: ", "", 1)
	if k := strings.LastIndex(shown, "|||"); k > 0 {
		shown = shown[:k]
	}
	_, loaded := u.settings["bDisplayIsUsingAutoScale"]
	s.ready = u.settings != nil && loaded && shown != "" && !strings.HasPrefix(strings.ToLower(shown), "xwayland") && s.api
	s.lo = numOr(u.settings["flMinDisplayScaleFactor"], 0.5)
	s.hi = numOr(u.settings["flMaxDisplayScaleFactor"], 2.5)
	mk, _ := u.marker.(map[string]any)
	if g, ok := mk["gen"].(float64); ok && g == math.Trunc(g) && g > 0 && g <= maxGen {
		s.mgen = g
	}
	s.mk = mk
	return s
}

func (u *fakeUI) refuse(s uiState, gen float64) map[string]any {
	switch {
	case s.mgen > gen:
		return map[string]any{"status": "stale", "gen": s.mgen}
	case !s.ready && s.sc && !s.api:
		return map[string]any{"status": "unsupported"}
	case !s.ready:
		return map[string]any{"status": "notready"}
	}
	return nil
}

func (u *fakeUI) read() map[string]any {
	s := u.state()
	name := s.name
	if len(name) > 256 {
		name = name[:256]
	}
	var value any
	if s.mgen > 0 {
		value = num(s.mk["value"])
	}
	return map[string]any{
		"ready": s.ready, "client": s.sc, "api": s.api, "name": name,
		"external":  u.settings["bDisplayIsExternal"] == true,
		"auto":      u.settings["bDisplayIsUsingAutoScale"] == true,
		"current":   num(u.settings["flCurrentDisplayScaleFactor"]),
		"autoValue": num(u.settings["flAutoDisplayScaleFactor"]),
		"min":       s.lo, "max": s.hi, "gen": s.mgen, "value": value,
	}
}

func (u *fakeUI) set(gen, scale float64) map[string]any {
	s := u.state()
	if no := u.refuse(s, gen); no != nil {
		return no
	}
	v := math.Min(s.hi, math.Max(s.lo, scale))
	u.calls = append(u.calls, "auto(false)", "manual("+strconv.FormatFloat(v, 'f', -1, 64)+")")
	u.settings["bDisplayIsUsingAutoScale"] = false
	u.settings["flCurrentDisplayScaleFactor"] = v
	u.marker = map[string]any{"gen": gen, "value": v}
	return map[string]any{"status": "ok", "value": v}
}

func (u *fakeUI) auto(gen float64) map[string]any {
	s := u.state()
	if no := u.refuse(s, gen); no != nil {
		return no
	}
	u.calls = append(u.calls, "auto(true)")
	u.settings["bDisplayIsUsingAutoScale"] = true
	u.settings["flCurrentDisplayScaleFactor"] = u.settings["flAutoDisplayScaleFactor"]
	u.marker = map[string]any{"gen": gen, "value": 0.0}
	return map[string]any{"status": "ok", "value": 0.0}
}

// ---- Steam's debugger

type fakeTarget struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
	WS    string `json:"webSocketDebuggerUrl"`
}

// fakeSteam is Steam's debugger as this package uses it: /json/list and
// a websocket per target answering Runtime.evaluate from each target's
// fakeUI, with knobs for how it frames answers.
type fakeSteam struct {
	t    *testing.T
	srv  *httptest.Server
	port int
	proc *fakeProc

	mu      sync.Mutex
	targets []fakeTarget
	windows map[string]*fakeUI // by target id
	list    http.HandlerFunc   // replaces /json/list's answer
	// head replaces the websocket handshake's answer (status line and
	// headers, without the blank line).
	head func(accept string) string
	// hook may answer a request itself, true when it did.
	hook       func(p *wsPeer, id int64, expr string) bool
	fragments  int  // every answer in this many frames
	ping       bool // a ping before every answer's last frame
	event      bool // an event before every answer
	closeAfter int  // drop a websocket after this many answers

	origins, hosts, paths, methods []string
	pongs                          []string
	closes                         []int // the close codes clients sent
	lists, dials                   int

	closed bool
	conns  map[net.Conn]bool
	wg     sync.WaitGroup
}

func newFakeSteam(t *testing.T) *fakeSteam {
	f := &fakeSteam{t: t, conns: map[net.Conn]bool{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.close)
	u, _ := url.Parse(f.srv.URL)
	f.port, _ = strconv.Atoi(u.Port())
	f.proc = newFakeProc(t, f.port)
	f.boot("1")
	return f
}

// boot is a (re)started Steam: its targets get new ids ending in n.
func (f *fakeSteam) boot(n string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws := "ws://192.0.2.1:9/devtools/page/"
	f.targets = []fakeTarget{
		{ID: "BP-" + n, Type: "page", Title: "Steam Big Picture Mode", URL: "about:blank?createflags=274", WS: ws + "BP-" + n},
		{ID: "SHARED-" + n, Type: "page", Title: "SharedJSContext", URL: "https://steamloopback.host/index.html", WS: ws + "SHARED-" + n},
		{ID: "QA-" + n, Type: "page", Title: "QuickAccess_uid2", URL: "about:blank?browserviewpopup=1", WS: ws + "QA-" + n},
		{ID: "MM-" + n, Type: "page", Title: "MainMenu_uid2", URL: "about:blank?browserviewpopup=1", WS: ws + "MM-" + n},
	}
	f.windows = map[string]*fakeUI{
		"SHARED-" + n: readyUI(),
		"BP-" + n:     {dpr: 1.71, h: 1263.0},
		"QA-" + n:     {dpr: 1.5, h: 1.0},
		"MM-" + n:     {dpr: 1.71, h: 1263.0},
	}
}

func (f *fakeSteam) client() *Client { return &Client{Port: f.port, ProcDir: f.proc.dir} }

func (f *fakeSteam) addr() string { return "127.0.0.1:" + strconv.Itoa(f.port) }

// ui is the SharedJSContext's window.
func (f *fakeSteam) ui() *fakeUI {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.targets {
		if t.Title == "SharedJSContext" {
			return f.windows[t.ID]
		}
	}
	return nil
}

func (f *fakeSteam) set(fn func(f *fakeSteam)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// drop closes every websocket without a close frame, as a Steam that
// died.
func (f *fakeSteam) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.conns {
		c.Close()
	}
}

func (f *fakeSteam) close() {
	f.srv.Close()
	f.mu.Lock()
	f.closed = true
	for c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

func (f *fakeSteam) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.origins = append(f.origins, r.Header.Values("Origin")...)
	f.hosts = append(f.hosts, r.Host)
	f.paths = append(f.paths, r.URL.Path)
	list := f.list
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/json/list":
		f.mu.Lock()
		f.lists++
		b, _ := json.Marshal(f.targets)
		f.mu.Unlock()
		if list != nil {
			list(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.Write(b)
	case strings.HasPrefix(r.URL.Path, "/devtools/page/"):
		f.websocket(w, r, strings.TrimPrefix(r.URL.Path, "/devtools/page/"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSteam) websocket(w http.ResponseWriter, r *http.Request, id string) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHas(r.Header, "Connection", "upgrade") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		http.Error(w, "not a websocket", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	ui := f.windows[id]
	head := f.head
	f.mu.Unlock()
	if ui == nil {
		http.NotFound(w, r)
		return
	}
	nc, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Error(err)
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		nc.Close()
		return
	}
	f.conns[nc] = true
	f.dials++
	f.wg.Add(1)
	f.mu.Unlock()
	defer f.wg.Done()
	defer nc.Close()
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	h := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept
	if head != nil {
		h = head(accept)
	}
	brw.WriteString(h + "\r\n\r\n")
	if brw.Flush() != nil {
		return
	}
	(&wsPeer{f: f, nc: nc, br: brw.Reader, ui: ui}).serve()
}

// wsPeer is the server's end of one websocket.
type wsPeer struct {
	f  *fakeSteam
	nc net.Conn
	br *bufio.Reader
	ui *fakeUI
}

func (p *wsPeer) serve() {
	f := p.f
	answers := 0
	for {
		fin, op, payload, masked, err := p.read()
		if err != nil {
			return
		}
		if !masked || !fin {
			f.t.Errorf("client frame: masked %v, fin %v", masked, fin)
			return
		}
		switch op {
		case opClose:
			code := 0
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			f.set(func(f *fakeSteam) { f.closes = append(f.closes, code) })
			p.write(opClose, true, payload[:min(2, len(payload))])
			return
		case opPong:
			f.set(func(f *fakeSteam) { f.pongs = append(f.pongs, string(payload)) })
		case opText:
			var req struct {
				ID     int64
				Method string
				Params struct {
					Expression                  string
					ReturnByValue, AwaitPromise bool
				}
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				f.t.Errorf("request %q: %v", payload, err)
				return
			}
			f.mu.Lock()
			f.methods = append(f.methods, req.Method)
			hook, closeAfter := f.hook, f.closeAfter
			f.mu.Unlock()
			if req.Method != "Runtime.evaluate" || !req.Params.ReturnByValue || !req.Params.AwaitPromise {
				f.t.Errorf("request %s", payload)
			}
			if hook != nil && hook(p, req.ID, req.Params.Expression) {
				continue
			}
			p.answer(req.ID, req.Params.Expression)
			if answers++; closeAfter > 0 && answers >= closeAfter {
				return
			}
		default:
			f.t.Errorf("client sent opcode %d", op)
			return
		}
	}
}

// answer evaluates expr in the peer's window, as Chromium answers.
func (p *wsPeer) answer(id int64, expr string) {
	p.f.mu.Lock()
	v, ok := p.ui.eval(expr)
	event := p.f.event
	p.f.mu.Unlock()
	var msg any
	if ok {
		msg = map[string]any{"id": id, "result": map[string]any{"result": map[string]any{"type": "object", "value": v}}}
	} else {
		desc := "ReferenceError: " + expr + " is not defined\n    at <anonymous>:1:1"
		exc := map[string]any{"type": "object", "subtype": "error", "className": "ReferenceError", "description": desc}
		msg = map[string]any{"id": id, "result": map[string]any{"result": exc,
			"exceptionDetails": map[string]any{"exceptionId": 1, "text": "Uncaught", "lineNumber": 0, "columnNumber": 0, "exception": exc}}}
	}
	if event {
		p.message([]byte(`{"method":"Runtime.executionContextCreated","params":{"context":{"id":2,"origin":"","name":""}}}`))
	}
	b, _ := json.Marshal(msg)
	p.message(b)
}

// message writes msg as the knobs say: in fragments, with a ping.
func (p *wsPeer) message(msg []byte) {
	p.f.mu.Lock()
	k, ping := max(p.f.fragments, 1), p.f.ping
	p.f.mu.Unlock()
	size := (len(msg) + k - 1) / k
	for i := 0; i < k; i++ {
		chunk := msg[min(i*size, len(msg)):min((i+1)*size, len(msg))]
		last := i == k-1
		if ping && last {
			p.write(opPing, true, []byte("are you there"))
		}
		op := byte(opCont)
		if i == 0 {
			op = opText
		}
		p.write(op, last, chunk)
	}
}

// write writes one server frame (never masked).
func (p *wsPeer) write(op byte, fin bool, payload []byte) {
	p.raw(frame(op, fin, payload))
}

func (p *wsPeer) raw(b []byte) { p.nc.Write(b) }

func frame(op byte, fin bool, payload []byte) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	return append(frameHead(b0, uint64(len(payload))), payload...)
}

func frameHead(b0 byte, n uint64) []byte {
	b := []byte{b0}
	switch {
	case n <= 125:
		return append(b, byte(n))
	case n <= 0xFFFF:
		return binary.BigEndian.AppendUint16(append(b, 126), uint16(n))
	}
	return binary.BigEndian.AppendUint64(append(b, 127), n)
}

// read reads one client frame and unmasks it.
func (p *wsPeer) read() (fin bool, op byte, payload []byte, masked bool, err error) {
	var h [2]byte
	if _, err = io.ReadFull(p.br, h[:]); err != nil {
		return
	}
	fin, op, masked = h[0]&0x80 != 0, h[0]&0x0F, h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(p.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(p.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(p.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(p.br, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return
}
