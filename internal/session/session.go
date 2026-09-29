// Package session is the Sunshine prep-cmd protocol: `vos session
// begin|end` (run by Sunshine as the gaming user) talks to vosd over
// /run/vos/session.sock. See docs/CONTRACTS.md "Session protocol".
// `vos session launch <steam-url>` (launch.go) starts a game in
// gamescope's Steam for Sunshine's detached app commands.
//
// The protocol is one newline-terminated JSON request and one
// newline-terminated JSON response per connection. The hook must never
// fail a stream: whatever happens, `vos session` exits 0 within Timeout.
package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Timeout is the hard ceiling for one request: the CLI gives up after it.
// Sunshine runs the CLI as a prep-cmd, and Moonlight's launch waits for it.
const Timeout = 90 * time.Second

// HandlerTimeout is the budget vosd gives each request's handler: less
// than Timeout, so an answer (even "gave up") reaches the CLI before the
// CLI gives up on it.
const HandlerTimeout = Timeout - 10*time.Second

// maxLine bounds a request or response line.
const maxLine = 64 << 10

type Request struct {
	Op     string `json:"op"` // "begin" | "end"
	Client string `json:"client,omitempty"`
	App    string `json:"app,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	FPS    int    `json:"fps,omitempty"`
	HDR    bool   `json:"hdr,omitempty"`
}

type Response struct {
	OK      bool   `json:"ok"`
	Mode    string `json:"mode,omitempty"`
	HDR     bool   `json:"hdr,omitempty"`
	Message string `json:"message,omitempty"`
}

// Handler is implemented by the display manager in vosd.
type Handler interface {
	Begin(ctx context.Context, req Request) Response
	End(ctx context.Context)
}

// Serve listens on path (mode 0660, group of the gaming user) until ctx ends.
func Serve(ctx context.Context, path string, h Handler) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// A previous vosd may have left its socket behind.
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := restrict(path); err != nil {
		ln.Close()
		os.Remove(path)
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		os.Remove(path)
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConn(ctx, conn, h)
		}()
	}
}

// restrict makes the socket reachable by root and the gaming user's group
// only (Sunshine runs as the gaming user).
func restrict(path string) error {
	if err := os.Chmod(path, 0o660); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return nil // tests and development
	}
	gid := config.GamerUID
	if g, err := user.LookupGroup(config.GamerUser); err == nil {
		if n, err := strconv.Atoi(g.Gid); err == nil {
			gid = n
		}
	}
	return os.Chown(path, 0, gid)
}

// serveConn handles one request. The handler gets HandlerTimeout; should
// it not return by then (plus a second to say why it gave up), the reply
// goes out without it: the hook must never hold a stream back.
func serveConn(ctx context.Context, conn net.Conn, h Handler) {
	serveConnWithin(ctx, conn, h, HandlerTimeout, time.Second)
}

func serveConnWithin(ctx context.Context, conn net.Conn, h Handler, budget, grace time.Duration) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	conn.SetDeadline(time.Now().Add(Timeout + 5*time.Second))

	var resp Response
	req, err := readRequest(conn)
	if err != nil {
		resp = Response{Message: "bad request: " + err.Error()}
	} else {
		resp = handleWithin(ctx, req, h, grace)
	}
	if err := writeLine(conn, resp); err != nil {
		log.Printf("session: reply: %v", err)
	}
}

// handleWithin runs the handler for req. Once ctx ends, the handler has
// grace to return its own answer; after that handleWithin answers for it
// and leaves it to finish in the background.
func handleWithin(ctx context.Context, req Request, h Handler, grace time.Duration) Response {
	done := make(chan Response, 1)
	go func() { done <- handle(ctx, req, h) }()
	select {
	case resp := <-done:
		return resp
	case <-ctx.Done():
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case resp := <-done:
		return resp
	case <-t.C:
		log.Printf("session: %s still busy after its budget; answering without it", req.Op)
		return Response{Message: fmt.Sprintf("%s did not finish in time (%v); continuing anyway", req.Op, ctx.Err())}
	}
}

// handle dispatches one request.
func handle(ctx context.Context, req Request, h Handler) Response {
	switch req.Op {
	case "begin":
		return h.Begin(ctx, req)
	case "end":
		h.End(ctx)
		return Response{OK: true}
	}
	return Response{Message: fmt.Sprintf("unknown op %q", req.Op)}
}

func readRequest(r io.Reader) (Request, error) {
	var req Request
	line, err := readLine(r)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return req, err
	}
	return req, nil
}

// readLine reads one line (without the newline); a final line without a
// newline is accepted.
func readLine(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, maxLine))
	line, err := br.ReadBytes('\n')
	if err == io.EOF && len(line) > 0 {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(line))), nil
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Call sends one request to the socket at path and returns the response.
func Call(ctx context.Context, path string, req Request) (Response, error) {
	var resp Response
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return resp, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	// Close the connection if ctx is cancelled without a deadline.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if err := writeLine(conn, req); err != nil {
		return resp, err
	}
	line, err := readLine(conn)
	if err != nil {
		return resp, err
	}
	err = json.Unmarshal(line, &resp)
	return resp, err
}

// RequestFromEnv builds a begin request from the environment Sunshine
// gives its prep commands (SUNSHINE_CLIENT_WIDTH/HEIGHT/FPS/HDR,
// SUNSHINE_CLIENT_NAME, SUNSHINE_APP_NAME).
func RequestFromEnv(getenv func(string) string) Request {
	num := func(k string) int {
		f, err := strconv.ParseFloat(strings.TrimSpace(getenv(k)), 64)
		if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
			return 0
		}
		return int(math.Round(f))
	}
	fps := num("SUNSHINE_CLIENT_FPS")
	if fps > 1000 {
		// Some Sunshine builds report millihertz (59940 for 59.94 Hz).
		fps = int(math.Round(float64(fps) / 1000))
	}
	hdr, _ := strconv.ParseBool(strings.TrimSpace(getenv("SUNSHINE_CLIENT_HDR")))
	return Request{
		Op:     "begin",
		Client: strings.TrimSpace(getenv("SUNSHINE_CLIENT_NAME")),
		App:    strings.TrimSpace(getenv("SUNSHINE_APP_NAME")),
		Width:  num("SUNSHINE_CLIENT_WIDTH"),
		Height: num("SUNSHINE_CLIENT_HEIGHT"),
		FPS:    fps,
		HDR:    hdr,
	}
}

// CLI implements `vos session begin|end` and `vos session launch
// <steam-url>`. It always exits 0: a hook failure must never keep
// Moonlight from streaming.
func CLI(args []string) int {
	if len(args) == 2 && args[0] == "launch" {
		newLauncher(os.Stderr).run(context.Background(), args[1])
		return 0
	}
	run(args, os.Getenv, os.Stderr, config.SessionSock(), Timeout)
	return 0
}

const usage = "usage: vos session begin|end|launch <steam-url>"

func run(args []string, getenv func(string) string, stderr io.Writer, sock string, timeout time.Duration) {
	logger := log.New(stderr, "vos session: ", 0)
	if len(args) != 1 || (args[0] != "begin" && args[0] != "end") {
		logger.Print(usage)
		return
	}
	req := Request{Op: args[0]}
	if req.Op == "begin" {
		req = RequestFromEnv(getenv)
		logger.Printf("begin: client %q app %q wants %dx%d@%d hdr=%v",
			req.Client, req.App, req.Width, req.Height, req.FPS, req.HDR)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := Call(ctx, sock, req)
	switch {
	case err != nil:
		logger.Printf("%s: %v (continuing anyway)", req.Op, err)
	case req.Op == "begin":
		logger.Printf("begin: ok=%v mode=%s hdr=%v %s", resp.OK, resp.Mode, resp.HDR, resp.Message)
	default:
		logger.Printf("%s: ok=%v %s", req.Op, resp.OK, resp.Message)
	}
}
