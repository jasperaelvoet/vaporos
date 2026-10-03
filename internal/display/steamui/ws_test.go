package steamui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func dialShared(t *testing.T, f *fakeSteam) *Conn {
	t.Helper()
	conn, err := dial(context.Background(), f.addr(), "SHARED-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func evalRead(t *testing.T, conn *Conn) error {
	t.Helper()
	raw, err := conn.Eval(context.Background(), readJS)
	if err != nil {
		return err
	}
	var r readResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	if st, err := r.decode(); err != nil || st.Name != `External: VaporOS 27"|||Windowed` {
		t.Fatalf("%+v, %v", st, err)
	}
	return nil
}

func TestEvalFramedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fragments int
		ping      bool
		event     bool
	}{
		{name: "one frame"},
		{name: "fragments", fragments: 7},
		{name: "a ping inside a message", fragments: 3, ping: true},
		{name: "a ping before a message", ping: true},
		{name: "events first", fragments: 2, event: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSteam(t)
			f.set(func(f *fakeSteam) { f.fragments, f.ping, f.event = tc.fragments, tc.ping, tc.event })
			conn := dialShared(t, f)
			for i := 0; i < 3; i++ {
				if err := evalRead(t, conn); err != nil {
					t.Fatalf("eval %d: %v", i, err)
				}
			}
			// The pongs went out before the third request did.
			f.set(func(f *fakeSteam) {
				if tc.ping && (len(f.pongs) < 2 || f.pongs[0] != "are you there") || !tc.ping && len(f.pongs) > 0 {
					t.Errorf("pongs %q", f.pongs)
				}
			})
		})
	}
}

// Answers to other ids (a call given up on) and events are skipped.
func TestEvalSkipsOtherMessages(t *testing.T) {
	f := newFakeSteam(t)
	f.set(func(f *fakeSteam) {
		f.hook = func(p *wsPeer, id int64, expr string) bool {
			p.message([]byte(`{"id":` + strconv.FormatInt(id+100, 10) + `,"result":{"result":{"type":"object","value":{"ready":false}}}}`))
			p.message([]byte(`{"method":"Inspector.detached","params":{"reason":"x"}}`))
			return false
		}
	})
	if err := evalRead(t, dialShared(t, f)); err != nil {
		t.Fatal(err)
	}
}

func TestEvalResults(t *testing.T) {
	f := newFakeSteam(t)
	conn := dialShared(t, f)
	ctx := context.Background()

	_, err := conn.Eval(ctx, "boom")
	var ee *EvalError
	if !errors.As(err, &ee) || !strings.HasPrefix(ee.Text, "ReferenceError: boom is not defined") {
		t.Fatalf("exception: %v", err)
	}
	if !conn.alive() {
		t.Fatal("an exception broke the connection")
	}

	answers := map[string]string{
		"undefined":      `{"type":"undefined"}`,
		"nan":            `{"type":"number","unserializableValue":"NaN","description":"NaN"}`,
		"protocol error": ``,
		"no result":      ``,
	}
	f.set(func(f *fakeSteam) {
		f.hook = func(p *wsPeer, id int64, expr string) bool {
			n := strconv.FormatInt(id, 10)
			switch expr {
			case "protocol error":
				p.message([]byte(`{"id":` + n + `,"error":{"code":-32601,"message":"'Runtime.evaluate' wasn't found"}}`))
			case "no result":
				p.message([]byte(`{"id":` + n + `}`))
			default:
				a, ok := answers[expr]
				if !ok {
					return false
				}
				p.message([]byte(`{"id":` + n + `,"result":{"result":` + a + `}}`))
			}
			return true
		}
	})
	if raw, err := conn.Eval(ctx, "undefined"); err != nil || string(raw) != "null" {
		t.Errorf("undefined: %s, %v", raw, err)
	}
	if _, err := conn.Eval(ctx, "nan"); err == nil || !strings.Contains(err.Error(), "unserializable") {
		t.Errorf("NaN: %v", err)
	}
	if _, err := conn.Eval(ctx, "protocol error"); err == nil || !strings.Contains(err.Error(), "-32601") {
		t.Errorf("protocol error: %v", err)
	}
	if _, err := conn.Eval(ctx, "no result"); err == nil {
		t.Error("an answer without a result passed")
	}
	if err := evalRead(t, conn); err != nil {
		t.Fatalf("after the errors: %v", err)
	}
}

// Requests of every length encoding reach the server whole.
func TestEvalLongExpressions(t *testing.T) {
	f := newFakeSteam(t)
	conn := dialShared(t, f)
	for _, n := range []int{125, 126, 1000, 0xFFFF + 10, 200 << 10} {
		expr := strings.Repeat("x", n)
		_, err := conn.Eval(context.Background(), expr)
		var ee *EvalError
		if !errors.As(err, &ee) || !strings.HasPrefix(ee.Text, "ReferenceError: xxx") {
			t.Fatalf("%d bytes: %v", n, err)
		}
	}
	if _, err := conn.Eval(context.Background(), strings.Repeat("x", maxMessage)); err == nil {
		t.Fatal("a request over 1 MiB went out")
	}
}

// A close from Steam is answered with its code and ends the connection.
func TestEvalServerCloses(t *testing.T) {
	f := newFakeSteam(t)
	f.set(func(f *fakeSteam) {
		f.hook = func(p *wsPeer, id int64, expr string) bool {
			p.write(opClose, true, []byte{0x03, 0xE9, 'b', 'y', 'e'})
			return true
		}
	})
	conn := dialShared(t, f)
	_, err := conn.Eval(context.Background(), readJS)
	if err == nil || !strings.Contains(err.Error(), "(1001)") {
		t.Fatalf("got %v", err)
	}
	if conn.alive() {
		t.Error("the connection still counts as alive")
	}
	if _, err2 := conn.Eval(context.Background(), readJS); err2 == nil {
		t.Error("a closed connection evaluated")
	}
	waitFor(t, func() bool {
		ok := false
		f.set(func(f *fakeSteam) { ok = slices.Equal(f.closes, []int{1001}) })
		return ok
	})
}

func TestEvalRefusesBadFrames(t *testing.T) {
	big := make([]byte, 600<<10)
	for _, tc := range []struct {
		name  string
		frame func() []byte
	}{
		{"over 1 MiB", func() []byte { return frameHead(0x80|opText, maxMessage+1) }},
		{"over 1 MiB in fragments", func() []byte {
			return append(frame(opText, false, big), frame(opCont, true, big)...)
		}},
		{"a 64-bit length with the top bit", func() []byte { return frameHead(0x80|opText, 1<<63) }},
		{"masked", func() []byte { return []byte{0x80 | opText, 0x80 | 2, 1, 2, 3, 4, 'h', 'i'} }},
		{"reserved bits", func() []byte { return []byte{0x80 | 0x40 | opText, 2, '{', '}'} }},
		{"an unknown opcode", func() []byte { return frame(0x3, true, []byte("{}")) }},
		{"a fragmented ping", func() []byte { return frame(opPing, false, []byte("x")) }},
		{"a long ping", func() []byte { return frame(opPing, true, make([]byte, 126)) }},
		{"a continuation first", func() []byte { return frame(opCont, true, []byte("{}")) }},
		{"a message inside a message", func() []byte {
			return append(frame(opText, false, []byte(`{"id"`)), frame(opText, true, []byte("{}"))...)
		}},
		{"not JSON", func() []byte { return frame(opText, true, []byte("hello")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSteam(t)
			f.set(func(f *fakeSteam) {
				f.hook = func(p *wsPeer, id int64, expr string) bool {
					p.raw(tc.frame())
					return true
				}
			})
			conn := dialShared(t, f)
			_, err := conn.Eval(context.Background(), readJS)
			if err == nil {
				t.Fatal("accepted")
			}
			if conn.alive() {
				t.Errorf("%v left the connection alive", err)
			}
			t.Log(err)
		})
	}
}

func TestEvalDeadline(t *testing.T) {
	f := newFakeSteam(t)
	f.set(func(f *fakeSteam) {
		f.hook = func(p *wsPeer, id int64, expr string) bool { return true } // never answers
	})
	conn := dialShared(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := conn.Eval(ctx, readJS)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
	if conn.alive() {
		t.Error("a connection with an answer still due counts as alive")
	}

	conn2 := dialShared(t, f)
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	if _, err := conn2.Eval(ctx2, readJS); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestHandshake(t *testing.T) {
	for _, tc := range []struct {
		name string
		head func(accept string) string
		ok   bool
	}{
		{name: "chromium's", ok: true, head: func(a string) string {
			return "HTTP/1.1 101 WebSocket Protocol Handshake\r\nUpgrade: WebSocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + a
		}},
		{name: "a wrong accept", head: func(a string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: AAAA" + a[4:]
		}},
		{name: "no upgrade", head: func(a string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + a
		}},
		{name: "an origin refused", head: func(a string) string {
			return "HTTP/1.1 403 Forbidden\r\nContent-Length: 0"
		}},
		{name: "a redirect", head: func(a string) string {
			return "HTTP/1.1 302 Found\r\nLocation: ws://127.0.0.1:1/devtools/page/X\r\nContent-Length: 0"
		}},
		{name: "headers too long", head: func(a string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nX-Pad: " + strings.Repeat("a", 20<<10)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSteam(t)
			f.set(func(f *fakeSteam) { f.head = tc.head })
			conn, err := dial(context.Background(), f.addr(), "SHARED-1")
			if !tc.ok {
				if err == nil {
					conn.Close()
					t.Fatal("accepted")
				}
				t.Log(err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := evalRead(t, conn); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDialNobody(t *testing.T) {
	f := newFakeSteam(t)
	addr := f.addr()
	f.srv.Close()
	if _, err := dial(context.Background(), addr, "SHARED-1"); !errors.Is(err, ErrNoDebugger) {
		t.Fatalf("got %v", err)
	}
}

// Close says goodbye with 1000.
func TestCloseSendsClose(t *testing.T) {
	f := newFakeSteam(t)
	conn := dialShared(t, f)
	if err := evalRead(t, conn); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	waitFor(t, func() bool {
		ok := false
		f.set(func(f *fakeSteam) { ok = slices.Equal(f.closes, []int{1000}) })
		return ok
	})
	if _, err := conn.Eval(context.Background(), readJS); err == nil {
		t.Error("a closed connection evaluated")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
