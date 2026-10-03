package steamui

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A minimal RFC 6455 client, enough for the DevTools protocol: one text
// message out, messages in until the answer, pings answered. No
// extensions, no Origin header (Chromium checks only an Origin that is
// sent), and a client frame is always masked.

const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA

	wsGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxHandshake = 16 << 10 // the server's 101 answer
)

var errConnClosed = errors.New("steamui: connection closed")

// Conn is a websocket connection to one target of Steam's debugger.
type Conn struct {
	mu     sync.Mutex // one evaluation at a time
	nc     net.Conn
	br     *bufio.Reader
	next   int64 // the last message id
	broken error // why the connection can no longer be used
}

// Dial opens a websocket to target t, after checking who listens on the
// port. The URL is ws://127.0.0.1:<port>/devtools/page/<id>, whatever
// the debugger named.
func (c *Client) Dial(ctx context.Context, t Target) (*Conn, error) {
	if !validID(t.ID) {
		return nil, fmt.Errorf("steamui: target id %q refused", clampString(t.ID, 80))
	}
	if err := c.checkListener(); err != nil {
		return nil, err
	}
	return dial(ctx, c.addr(), t.ID)
}

func dial(ctx context.Context, addr, id string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDebugger, err)
	}
	stop := guard(ctx, nc, dialTimeout)
	conn, err := handshake(nc, addr, id)
	if !stop() && err == nil {
		err = ctxErr(ctx)
	}
	if err != nil {
		nc.Close()
		if cerr := ctxErr(ctx); cerr != nil {
			return nil, cerr
		}
		return nil, err
	}
	return conn, nil
}

func handshake(nc net.Conn, addr, id string) (*Conn, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	req := "GET /devtools/page/" + id + " HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(nc, req); err != nil {
		return nil, err
	}
	capped := &capReader{r: nc, n: maxHandshake}
	br := bufio.NewReader(capped)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("steamui: websocket handshake: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("steamui: websocket handshake answered %d", resp.StatusCode)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") ||
		!headerHas(resp.Header, "Connection", "upgrade") ||
		resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return nil, errors.New("steamui: websocket handshake refused")
	}
	capped.n = -1 // frames have their own bounds
	return &Conn{nc: nc, br: br}, nil
}

func headerHas(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// capReader reads at most n bytes, or without bound when n is negative.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n < 0 {
		return c.r.Read(p)
	}
	if c.n == 0 {
		return 0, errors.New("answer too long")
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	n, err := c.r.Read(p)
	c.n -= int64(n)
	return n, err
}

// guard bounds a call's I/O on nc by ctx and by limit, whichever ends
// first. stop clears the deadline again and reports whether ctx left the
// connection alone; when it did not, nc's deadline may be in the past.
func guard(ctx context.Context, nc net.Conn, limit time.Duration) (stop func() bool) {
	dl := time.Now().Add(limit)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	nc.SetDeadline(dl)
	release := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	return func() bool {
		if !release() {
			return false
		}
		nc.SetDeadline(time.Time{})
		return true
	}
}

// ctxErr is ctx's error, also in the moment between its deadline passing
// (which the connection's deadline is) and ctx noticing.
func ctxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		return context.DeadlineExceeded
	}
	return nil
}

type evalRequest struct {
	ID     int64      `json:"id"`
	Method string     `json:"method"`
	Params evalParams `json:"params"`
}

type evalParams struct {
	Expression    string `json:"expression"`
	ReturnByValue bool   `json:"returnByValue"`
	AwaitPromise  bool   `json:"awaitPromise"`
}

type evalAnswer struct {
	Result *struct {
		Result struct {
			Type                string          `json:"type"`
			Value               json.RawMessage `json:"value"`
			UnserializableValue string          `json:"unserializableValue"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Eval evaluates expr in the target (Runtime.evaluate, by value, a
// promise awaited) and returns the value as JSON, null for undefined. An
// exception is an *EvalError. A failure of the connection itself leaves
// it unusable: every later call fails.
func (c *Conn) Eval(ctx context.Context, expr string) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return nil, c.broken
	}
	c.next++
	req, err := json.Marshal(evalRequest{ID: c.next, Method: "Runtime.evaluate",
		Params: evalParams{Expression: expr, ReturnByValue: true, AwaitPromise: true}})
	if err != nil {
		return nil, err
	}
	if len(req) > maxMessage {
		return nil, errors.New("steamui: expression too long")
	}
	stop := guard(ctx, c.nc, evalTimeout)
	msg, err := c.roundTrip(c.next, req)
	if !stop() {
		// ctx ended; the answer may still be good, the connection is not.
		c.fail(context.Cause(ctx))
	}
	if err != nil {
		c.fail(err)
		if cerr := ctxErr(ctx); cerr != nil {
			return nil, cerr
		}
		return nil, err
	}
	var a evalAnswer
	if err := json.Unmarshal(msg, &a); err != nil {
		c.fail(err)
		return nil, fmt.Errorf("steamui: unexpected message: %v", err)
	}
	switch {
	case a.Error != nil:
		return nil, fmt.Errorf("steamui: Runtime.evaluate failed (%d): %s", a.Error.Code, clampString(a.Error.Message, 200))
	case a.Result == nil:
		return nil, errors.New("steamui: Runtime.evaluate answered nothing")
	case a.Result.ExceptionDetails != nil:
		d := a.Result.ExceptionDetails
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			text = d.Exception.Description
		}
		return nil, &EvalError{Text: clampString(text, 200)}
	case a.Result.Result.UnserializableValue != "":
		return nil, fmt.Errorf("steamui: unserializable value %s", clampString(a.Result.Result.UnserializableValue, 32))
	case len(a.Result.Result.Value) == 0:
		return json.RawMessage("null"), nil
	}
	return a.Result.Result.Value, nil
}

// roundTrip sends req and returns the message that answers id; events
// and other answers are skipped.
func (c *Conn) roundTrip(id int64, req []byte) ([]byte, error) {
	if err := c.writeFrame(opText, req); err != nil {
		return nil, err
	}
	for {
		msg, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		var head struct {
			ID *int64 `json:"id"`
		}
		if err := json.Unmarshal(msg, &head); err != nil {
			return nil, fmt.Errorf("steamui: unexpected message: %v", err)
		}
		if head.ID != nil && *head.ID == id {
			return msg, nil
		}
	}
}

// alive reports whether the connection can still be used.
func (c *Conn) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.broken == nil
}

// fail marks the connection unusable and closes it.
func (c *Conn) fail(err error) {
	if c.broken == nil {
		if err == nil {
			err = errConnClosed
		}
		c.broken = err
		c.nc.Close()
	}
}

// Close says goodbye (1000) without waiting for the answer and closes the
// connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return nil
	}
	c.nc.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	c.writeFrame(opClose, []byte{0x03, 0xE8})
	c.broken = errConnClosed
	return c.nc.Close()
}

// readMessage returns the next text or binary message, put together from
// its fragments, answering pings on the way. A close from Steam is
// answered and ends the connection.
func (c *Conn) readMessage() ([]byte, error) {
	var msg []byte
	started := false
	for {
		fin, op, payload, err := c.readFrame(maxMessage - len(msg))
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			reply := []byte{}
			if len(payload) >= 2 {
				reply = payload[:2]
			}
			c.writeFrame(opClose, reply)
			code := 1005
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			return nil, fmt.Errorf("steamui: Steam closed the connection (%d)", code)
		case opText, opBinary:
			if started {
				return nil, errors.New("steamui: new message inside a fragmented one")
			}
			started = true
			msg = payload
		case opCont:
			if !started {
				return nil, errors.New("steamui: continuation without a message")
			}
			msg = append(msg, payload...)
		default:
			return nil, fmt.Errorf("steamui: unknown websocket opcode %d", op)
		}
		if fin {
			return msg, nil
		}
	}
}

// readFrame reads one frame. A data frame's payload may be at most room
// bytes; a control frame's at most 125, unfragmented.
func (c *Conn) readFrame(room int) (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0F
	if h[0]&0x70 != 0 {
		return false, 0, nil, errors.New("steamui: websocket frame with reserved bits")
	}
	if h[1]&0x80 != 0 {
		return false, 0, nil, errors.New("steamui: masked websocket frame from the server")
	}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if op >= opClose {
		if !fin || n > 125 {
			return false, 0, nil, errors.New("steamui: bad websocket control frame")
		}
	} else if n > uint64(room) {
		return false, 0, nil, errors.New("steamui: websocket message over 1 MiB")
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	return fin, op, payload, nil
}

// writeFrame writes one unfragmented, masked frame.
func (c *Conn) writeFrame(op byte, payload []byte) error {
	b := make([]byte, 0, 14+len(payload))
	b = append(b, 0x80|op)
	switch n := len(payload); {
	case n <= 125:
		b = append(b, 0x80|byte(n))
	case n <= 0xFFFF:
		b = append(b, 0x80|126)
		b = binary.BigEndian.AppendUint16(b, uint16(n))
	default:
		b = append(b, 0x80|127)
		b = binary.BigEndian.AppendUint64(b, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	b = append(b, mask[:]...)
	for i, x := range payload {
		b = append(b, x^mask[i&3])
	}
	_, err := c.nc.Write(b)
	return err
}
