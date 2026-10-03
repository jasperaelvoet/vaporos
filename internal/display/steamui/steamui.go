// Package steamui is vosd's client for Steam's CEF remote debugger, the
// only way to read and change gamepadui's display scale from outside
// Steam (docs/CONTRACTS.md, Display policy, "Scaling" and "Steam's
// debugger"). Steam binds it on 127.0.0.1:DevtoolsPort, and only root can
// connect (the firewall), but anything of the gaming user could listen
// there instead, so nothing on the port is trusted: its listener must be
// a steamwebhelper of the gaming user, redirects are refused, every answer
// and message is capped, the websocket URL is built here from a checked
// target id, and every value is decoded typed and clamped. It only ever
// sends Runtime.evaluate of the fixed expressions in js.go.
package steamui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// DevtoolsPort is the port vos-gamescope.service gives Steam
// (`-devtools-port 31911`), below the ephemeral range and reserved.
const DevtoolsPort = 31911

const (
	maxBody     = 1 << 20 // a /json/list answer
	maxMessage  = 1 << 20 // a websocket message
	listTimeout = 2 * time.Second
	dialTimeout = 2 * time.Second
	// evalTimeout bounds one evaluation when the caller's context does not
	// end sooner: none of the expressions waits.
	evalTimeout = 5 * time.Second
	// maxFactor is above any scale Steam allows (its maximum is about 3.4
	// on a phone-sized display); larger values read from Steam are clamped.
	maxFactor = 8
	maxGen    = 1<<53 - 1 // the largest generation JavaScript holds exactly
	maxName   = 256
)

var (
	// ErrNoDebugger: nothing listens on the port, or what does is not
	// Steam's steamwebhelper (then the error is a *ListenerError).
	ErrNoDebugger = errors.New("steamui: Steam's debugger is not there")
	// ErrNoTarget: the debugger answers but has no SharedJSContext yet
	// (Steam is starting).
	ErrNoTarget = errors.New("steamui: Steam has no SharedJSContext")
	// ErrUnsupported: Steam lacks SetGamepadUIAutoDisplayScale or
	// SetGamepadUIManualDisplayScaleFactor (Valve changed them).
	ErrUnsupported = errors.New("steamui: Steam has no display scale setters")
	// ErrNotReady: Steam's display settings are not loaded yet, or name
	// no display.
	ErrNotReady = errors.New("steamui: Steam's display settings are not ready")
	// ErrStale: Steam already holds a newer generation than the one a set
	// or the automatic scale was for, which therefore did nothing.
	ErrStale = errors.New("steamui: Steam holds a newer generation")
)

// EvalError is an exception an expression threw inside Steam.
type EvalError struct{ Text string }

func (e *EvalError) Error() string { return "steamui: Steam threw: " + e.Text }

// State is Steam's display scale as its SharedJSContext holds it, every
// value checked: a factor is 0 when Steam has none and never above 8.
type State struct {
	Name      string  // Steam's display identity, e.g. `External: VaporOS 27"|||Windowed`, at most 256 bytes
	External  bool    // Steam counts the display as external
	Auto      bool    // Steam's automatic scale is on
	Current   float64 // the factor in use
	AutoValue float64 // the factor of Steam's automatic scale
	Min, Max  float64 // the bounds of Steam's slider (0.5 and 2.5 when Steam names none, as the slider does)
	Gen       uint64  // the generation in VaporOS's marker (window.__vosScale), 0 without one
	Value     float64 // the factor that generation set, 0 for Steam's automatic scale
}

// View is one of Steam's views that it lays out late (Quick Access, the
// main menu), with the scale it is laid out at.
type View struct {
	ID, Title string
	DPR       float64 // devicePixelRatio
	Height    int     // innerHeight: 1 or less until Steam first shows it
}

// Client talks to Steam's debugger. It keeps one connection to
// SharedJSContext open across calls and opens a new one (with a fresh
// target list and listener check) when that one fails. The zero value
// works on the running system; a Client must not be copied.
type Client struct {
	Port    int    // DevtoolsPort when 0
	ProcDir string // /proc when ""

	mu     sync.Mutex // one evaluation on shared at a time
	shared *Conn

	hcOnce sync.Once
	hc     *http.Client

	ownMu  sync.Mutex
	owners map[uint64]Owner // listener inode → its verified owner
}

func (c *Client) port() int {
	if c.Port == 0 {
		return DevtoolsPort
	}
	return c.Port
}

func (c *Client) procDir() string {
	if c.ProcDir == "" {
		return "/proc"
	}
	return c.ProcDir
}

func (c *Client) addr() string { return "127.0.0.1:" + strconv.Itoa(c.port()) }

// Read returns Steam's display scale. With ErrNotReady or ErrUnsupported
// it also returns what Steam has so far. Any other error means the
// debugger did not answer.
func (c *Client) Read(ctx context.Context) (State, error) {
	var r readResult
	if err := c.evalShared(ctx, readJS, &r); err != nil {
		return State{}, err
	}
	return r.decode()
}

// Set turns Steam's automatic scale off and sets its manual factor to
// scale, clamped to Steam's bounds, for generation gen (1 or more), and
// returns the factor Steam got. It does nothing and returns ErrStale when
// Steam already holds a newer generation; the same generation again sets
// again (Steam's late views pick the scale up that way).
func (c *Client) Set(ctx context.Context, gen uint64, scale float64) (float64, error) {
	if err := checkGen(gen); err != nil {
		return 0, err
	}
	if !(scale > 0 && scale <= maxFactor) {
		return 0, fmt.Errorf("steamui: scale %v out of range", scale)
	}
	var r applyResult
	if err := c.evalShared(ctx, call(setJS, gen, scale), &r); err != nil {
		return 0, err
	}
	return r.result()
}

// Auto turns Steam's automatic scale on for generation gen (1 or more),
// with ErrStale as for Set.
func (c *Client) Auto(ctx context.Context, gen uint64) error {
	if err := checkGen(gen); err != nil {
		return err
	}
	var r applyResult
	if err := c.evalShared(ctx, call(autoJS, gen), &r); err != nil {
		return err
	}
	_, err := r.result()
	return err
}

// Views returns the QuickAccess_ and MainMenu_ views and their scale,
// each through a connection of its own that is closed again. A view that
// does not answer is left out.
func (c *Client) Views(ctx context.Context) ([]View, error) {
	ts, err := c.Targets(ctx)
	if err != nil {
		return nil, err
	}
	out := []View{}
	for _, t := range Views(ts) {
		v, err := c.view(ctx, t)
		if err != nil {
			if cerr := ctxErr(ctx); cerr != nil {
				return out, cerr
			}
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (c *Client) view(ctx context.Context, t Target) (View, error) {
	conn, err := c.Dial(ctx, t)
	if err != nil {
		return View{}, err
	}
	defer conn.Close()
	raw, err := conn.Eval(ctx, viewJS)
	if err != nil {
		return View{}, err
	}
	var r viewResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return View{}, fmt.Errorf("steamui: view: %v", err)
	}
	return r.view(t), nil
}

// Close drops the open connection; the next call opens a new one.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shared != nil {
		c.shared.Close()
		c.shared = nil
	}
}

// evalShared evaluates expr on SharedJSContext and decodes its value into
// out. A connection kept from an earlier call that turns out dead (Steam
// restarted) is replaced once within the same call; every expression is
// safe to send twice.
func (c *Client) evalShared(ctx context.Context, expr string, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for retried := false; ; retried = true {
		reused := c.shared != nil
		if !reused {
			conn, err := c.connectShared(ctx)
			if err != nil {
				return err
			}
			c.shared = conn
		}
		raw, err := c.shared.Eval(ctx, expr)
		if err == nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("steamui: unexpected answer: %v", err)
			}
			return nil
		}
		if c.shared.alive() {
			return err // Steam answered, with an exception or a protocol error
		}
		c.shared.Close()
		c.shared = nil
		if !reused || retried || ctxErr(ctx) != nil {
			return err
		}
	}
}

func (c *Client) connectShared(ctx context.Context) (*Conn, error) {
	ts, err := c.Targets(ctx)
	if err != nil {
		return nil, err
	}
	t, ok := Shared(ts)
	if !ok {
		return nil, ErrNoTarget
	}
	return c.Dial(ctx, t)
}

func checkGen(gen uint64) error {
	if gen == 0 || gen > maxGen {
		return fmt.Errorf("steamui: generation %d out of range", gen)
	}
	return nil
}

// readResult is what readJS returns.
type readResult struct {
	Ready     bool     `json:"ready"`
	Client    bool     `json:"client"`
	API       bool     `json:"api"`
	Name      string   `json:"name"`
	External  bool     `json:"external"`
	Auto      bool     `json:"auto"`
	Current   *float64 `json:"current"`
	AutoValue *float64 `json:"autoValue"`
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	Gen       *float64 `json:"gen"`
	Value     *float64 `json:"value"`
}

// decode is the State in r, with ErrUnsupported or ErrNotReady when
// Steam is not ready.
func (r readResult) decode() (State, error) {
	st := r.state()
	switch {
	case r.Client && !r.API:
		return st, ErrUnsupported
	case !r.Ready:
		return st, ErrNotReady
	}
	return st, nil
}

func (r readResult) state() State {
	st := State{
		Name:      clampString(r.Name, maxName),
		External:  r.External,
		Auto:      r.Auto,
		Current:   clampFloat(r.Current, maxFactor),
		AutoValue: clampFloat(r.AutoValue, maxFactor),
		Min:       clampFloat(r.Min, maxFactor),
		Max:       clampFloat(r.Max, maxFactor),
		Gen:       genOf(r.Gen),
	}
	if st.Gen > 0 {
		st.Value = clampFloat(r.Value, maxFactor)
	}
	return st
}

// applyResult is what setJS and autoJS return.
type applyResult struct {
	Status string   `json:"status"`
	Value  *float64 `json:"value"`
	Gen    *float64 `json:"gen"`
}

func (r applyResult) result() (float64, error) {
	switch r.Status {
	case "ok":
		return clampFloat(r.Value, maxFactor), nil
	case "stale":
		return 0, fmt.Errorf("%w (%d)", ErrStale, genOf(r.Gen))
	case "notready":
		return 0, ErrNotReady
	case "unsupported":
		return 0, ErrUnsupported
	}
	return 0, fmt.Errorf("steamui: unexpected status %q", clampString(r.Status, 32))
}

// viewResult is what viewJS returns.
type viewResult struct {
	DPR *float64 `json:"dpr"`
	H   *float64 `json:"h"`
}

func (r viewResult) view(t Target) View {
	return View{ID: t.ID, Title: t.Title, DPR: clampFloat(r.DPR, 16), Height: int(clampFloat(r.H, 1<<20))}
}

// clampFloat is *p within [0, hi], 0 when p is nil.
func clampFloat(p *float64, hi float64) float64 {
	if p == nil || !(*p > 0) {
		return 0
	}
	return math.Min(*p, hi)
}

// genOf is a marker's generation, 0 unless a whole number in range.
func genOf(p *float64) uint64 {
	if p == nil || !(*p >= 1 && *p <= maxGen) || *p != math.Trunc(*p) {
		return 0
	}
	return uint64(*p)
}

// clampString is s as valid UTF-8 and at most n bytes, cut at a rune.
func clampString(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
