package session

import (
	"bufio"
	"bytes"
	"context"
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
	req := Request{Op: "begin", Client: "Jasper's iPhone", App: "Steam", Width: 2556, Height: 1179, FPS: 120, HDR: true}
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
		"SUNSHINE_CLIENT_NAME":   " Jasper's MacBook ",
		"SUNSHINE_APP_NAME":      "Steam Big Picture",
		"SUNSHINE_CLIENT_WIDTH":  "2560",
		"SUNSHINE_CLIENT_HEIGHT": "1600",
		"SUNSHINE_CLIENT_FPS":    "59.94",
		"SUNSHINE_CLIENT_HDR":    "true",
	}
	req := RequestFromEnv(func(k string) string { return env[k] })
	want := Request{Op: "begin", Client: "Jasper's MacBook", App: "Steam Big Picture", Width: 2560, Height: 1600, FPS: 60, HDR: true}
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
}

func TestCLIRun(t *testing.T) {
	h := &fakeHandler{}
	path, cancel, _ := serve(t, h)
	defer cancel()
	env := map[string]string{"SUNSHINE_CLIENT_WIDTH": "1920", "SUNSHINE_CLIENT_HEIGHT": "1080", "SUNSHINE_CLIENT_FPS": "120"}
	var log bytes.Buffer
	run([]string{"begin"}, func(k string) string { return env[k] }, &log, path, time.Second)
	if len(h.begins) != 1 || h.begins[0].FPS != 120 || !strings.Contains(log.String(), "begin: ok=true mode=2560x1440@120") {
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
