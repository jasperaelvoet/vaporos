package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHandler struct {
	mu     sync.Mutex
	begins []Request
	ends   int
	block  chan struct{} // Begin waits on it when non-nil
}

func (f *fakeHandler) Begin(ctx context.Context, req Request) Response {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Response{Message: "cancelled"}
		}
	}
	f.mu.Lock()
	f.begins = append(f.begins, req)
	f.mu.Unlock()
	return Response{OK: true, Mode: "2560x1440@120", HDR: req.HDR, Message: "ready"}
}

func (f *fakeHandler) End(ctx context.Context) {
	f.mu.Lock()
	f.ends++
	f.mu.Unlock()
}

// sockPath returns a short socket path: macOS limits them to 104 bytes.
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "run", "session.sock")
}

func serve(t *testing.T, h Handler) (string, context.CancelFunc, chan error) {
	t.Helper()
	path := sockPath(t)
	// A stale socket from a previous run must not stop us.
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, nil, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, h) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never came up")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return path, cancel, done
}

func TestRoundTrip(t *testing.T) {
	h := &fakeHandler{}
	path, cancel, done := serve(t, h)

	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %v, %v", fi.Mode(), err)
	}

	ctx := context.Background()
	req := Request{Op: "begin", Client: "Jasper's iPhone", App: "Steam", Width: 2556, Height: 1179, FPS: 120, HDR: true, Audio: "5.1"}
	resp, err := Call(ctx, path, req)
	if err != nil || !resp.OK || resp.Mode != "2560x1440@120" || !resp.HDR || resp.Message != "ready" {
		t.Fatalf("begin = %+v, %v", resp, err)
	}
	if len(h.begins) != 1 || h.begins[0] != req {
		t.Errorf("handler saw %+v", h.begins)
	}
	resp, err = Call(ctx, path, Request{Op: "end"})
	if err != nil || !resp.OK || h.ends != 1 {
		t.Fatalf("end = %+v, %v (ends %d)", resp, err, h.ends)
	}
	resp, err = Call(ctx, path, Request{Op: "reboot"})
	if err != nil || resp.OK || !strings.Contains(resp.Message, "unknown op") {
		t.Errorf("unknown op = %+v, %v", resp, err)
	}

	// Raw protocol: one JSON line in, one JSON line out; junk is refused.
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("this is not json\n"))
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if !strings.HasPrefix(line, `{"ok":false,"message":"bad request`) {
		t.Errorf("junk reply = %q", line)
	}
	// The handler only ever sees a known audio layout.
	c, _ = net.Dial("unix", path)
	c.Write([]byte(`{"op":"begin","width":1920,"height":1080,"audio":"9.9"}` + "\n"))
	bufio.NewReader(c).ReadString('\n')
	c.Close()
	h.mu.Lock()
	got := h.begins[len(h.begins)-1]
	h.mu.Unlock()
	if got.Audio != "" || got.Width != 1920 {
		t.Errorf("raw begin reached the handler as %+v", got)
	}
	// A request without a trailing newline still works.
	c, _ = net.Dial("unix", path)
	c.Write([]byte(`{"op":"end"}`))
	c.(*net.UnixConn).CloseWrite()
	line, _ = bufio.NewReader(c).ReadString('\n')
	c.Close()
	if line != "{\"ok\":true}\n" {
		t.Errorf("reply = %q", line)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("socket left behind")
	}
}

func TestShutdownCancelsRequests(t *testing.T) {
	h := &fakeHandler{block: make(chan struct{})}
	path, cancel, done := serve(t, h)
	got := make(chan Response, 1)
	go func() {
		resp, _ := Call(context.Background(), path, Request{Op: "begin"})
		got <- resp
	}()
	time.Sleep(50 * time.Millisecond)
	cancel() // vosd stops: the in-flight Begin sees its context end
	select {
	case resp := <-got:
		if resp.OK || resp.Message != "cancelled" {
			t.Errorf("resp = %+v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request not cancelled")
	}
	<-done
}

// deadlineHandler records the deadline its Begin was given.
type deadlineHandler struct {
	got chan time.Duration
}

func (d *deadlineHandler) Begin(ctx context.Context, req Request) Response {
	dl, ok := ctx.Deadline()
	if !ok {
		d.got <- -1
	} else {
		d.got <- time.Until(dl)
	}
	return Response{OK: true}
}

func (d *deadlineHandler) End(ctx context.Context) {}

// TestHandlerBudget: vosd gives the handler less than the hook's ceiling,
// so its answer arrives before `vos session` gives up.
func TestHandlerBudget(t *testing.T) {
	if HandlerTimeout >= Timeout || HandlerTimeout < 75*time.Second {
		t.Fatalf("HandlerTimeout %s vs Timeout %s", HandlerTimeout, Timeout)
	}
	h := &deadlineHandler{got: make(chan time.Duration, 1)}
	path, cancel, _ := serve(t, h)
	defer cancel()
	if _, err := Call(context.Background(), path, Request{Op: "begin"}); err != nil {
		t.Fatal(err)
	}
	if left := <-h.got; left <= HandlerTimeout-5*time.Second || left > HandlerTimeout {
		t.Errorf("handler deadline in %s, want about %s", left, HandlerTimeout)
	}
}

// stubbornHandler ignores its context until released.
type stubbornHandler struct{ release chan struct{} }

func (s *stubbornHandler) Begin(ctx context.Context, req Request) Response {
	<-s.release
	return Response{OK: true, Message: "late"}
}

func (s *stubbornHandler) End(ctx context.Context) { <-s.release }

// TestHandlerOverrun: a handler that outlives its budget does not hold the
// answer back; `vos session` hears "continuing anyway" in time.
func TestHandlerOverrun(t *testing.T) {
	h := &stubbornHandler{release: make(chan struct{})}
	defer close(h.release)
	for _, op := range []string{"begin", "end"} {
		server, client := net.Pipe()
		go serveConnWithin(context.Background(), server, h, 50*time.Millisecond, 20*time.Millisecond)
		start := time.Now()
		client.SetDeadline(time.Now().Add(2 * time.Second))
		if err := writeLine(client, Request{Op: op}); err != nil {
			t.Fatal(err)
		}
		line, err := readLine(client)
		client.Close()
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if !strings.Contains(string(line), op+" did not finish in time") || strings.Contains(string(line), `"ok":true`) {
			t.Errorf("%s: reply %s", op, line)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: answered after %s", op, d)
		}
	}
}

func TestCallTimeout(t *testing.T) {
	h := &fakeHandler{block: make(chan struct{})}
	path, cancel, _ := serve(t, h)
	defer cancel()
	defer close(h.block)
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := Call(ctx, path, Request{Op: "begin"}); err == nil {
		t.Error("expected a timeout")
	}
	if time.Since(start) > time.Second {
		t.Error("timeout not honoured")
	}
}

func TestRequestFromEnv(t *testing.T) {
	env := map[string]string{
		"SUNSHINE_CLIENT_NAME":                " Jasper's MacBook ",
		"SUNSHINE_APP_NAME":                   "Steam Big Picture",
		"SUNSHINE_CLIENT_WIDTH":               "2560",
		"SUNSHINE_CLIENT_HEIGHT":              "1600",
		"SUNSHINE_CLIENT_FPS":                 "59.94",
		"SUNSHINE_CLIENT_HDR":                 "true",
		"SUNSHINE_CLIENT_AUDIO_CONFIGURATION": "7.1",
	}
	req := RequestFromEnv(func(k string) string { return env[k] })
	want := Request{Op: "begin", Client: "Jasper's MacBook", App: "Steam Big Picture", Width: 2560, Height: 1600, FPS: 60, HDR: true, Audio: "7.1"}
	if req != want {
		t.Errorf("req = %+v", req)
	}
	env["SUNSHINE_CLIENT_FPS"] = "119880" // millihertz
	env["SUNSHINE_CLIENT_HDR"] = "false"
	env["SUNSHINE_CLIENT_WIDTH"] = "garbage"
	req = RequestFromEnv(func(k string) string { return env[k] })
	if req.FPS != 120 || req.HDR || req.Width != 0 {
		t.Errorf("req = %+v", req)
	}
	if req := RequestFromEnv(func(string) string { return "" }); req != (Request{Op: "begin"}) {
		t.Errorf("empty env = %+v", req)
	}
	for in, want := range map[string]string{
		"2.0": "2.0", " 5.1\n": "5.1", "7.1": "7.1",
		"": "", "stereo": "", "6": "", "7.1.4": "", "5.1 ": "5.1", "2,0": "",
	} {
		env["SUNSHINE_CLIENT_AUDIO_CONFIGURATION"] = in
		if got := RequestFromEnv(func(k string) string { return env[k] }).Audio; got != want {
			t.Errorf("audio %q = %q, want %q", in, got, want)
		}
	}
}

func TestAudioOnTheWire(t *testing.T) {
	b, _ := json.Marshal(Request{Op: "begin", Width: 1280, Height: 800})
	if strings.Contains(string(b), "audio") {
		t.Errorf("unset audio is sent: %s", b)
	}
	b, _ = json.Marshal(Request{Op: "begin", Audio: "5.1"})
	if !strings.Contains(string(b), `"audio":"5.1"`) {
		t.Errorf("audio missing: %s", b)
	}
}

func TestCLIRun(t *testing.T) {
	h := &fakeHandler{}
	path, cancel, _ := serve(t, h)
	defer cancel()
	env := map[string]string{"SUNSHINE_CLIENT_WIDTH": "1920", "SUNSHINE_CLIENT_HEIGHT": "1080", "SUNSHINE_CLIENT_FPS": "120", "SUNSHINE_CLIENT_AUDIO_CONFIGURATION": "5.1"}
	var log bytes.Buffer
	run([]string{"begin"}, func(k string) string { return env[k] }, &log, path, time.Second)
	if len(h.begins) != 1 || h.begins[0].FPS != 120 || h.begins[0].Audio != "5.1" ||
		!strings.Contains(log.String(), `audio="5.1"`) || !strings.Contains(log.String(), "begin: ok=true mode=2560x1440@120") {
		t.Errorf("begin: %+v %q", h.begins, log.String())
	}
	log.Reset()
	run([]string{"end"}, nil, &log, path, time.Second)
	if h.ends != 1 || !strings.Contains(log.String(), "end: ok=true") {
		t.Errorf("end: %d %q", h.ends, log.String())
	}
	// No daemon: logged, never fatal.
	log.Reset()
	start := time.Now()
	run([]string{"begin"}, func(string) string { return "" }, &log, filepath.Join(t.TempDir(), "none.sock"), 200*time.Millisecond)
	if !strings.Contains(log.String(), "continuing anyway") || time.Since(start) > time.Second {
		t.Errorf("no daemon: %q", log.String())
	}
	log.Reset()
	run([]string{"frobnicate"}, nil, &log, path, time.Second)
	if !strings.Contains(log.String(), "usage") {
		t.Errorf("usage: %q", log.String())
	}
	if code := CLI([]string{"bogus"}); code != 0 {
		t.Errorf("CLI exit %d, want 0", code)
	}
}
