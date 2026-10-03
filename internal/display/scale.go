package display

// The scaler (docs/CONTRACTS.md, Display policy, Scaling). Begin works out
// the screen of the device that streams (who it is, what kind of screen it
// shows the stream on, the scale and DPI that suit it) and hands it over;
// one goroutine, watchScale, then holds that scale in Steam through Steam's
// debugger (internal/display/steamui) and Xft.dpi on the games' X display.
// It is the only code that does either, and Begin never talks to Steam.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/display/steamui"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// steamUI is Steam's side of scaling (a *steamui.Client); tests fake it.
type steamUI interface {
	Read(ctx context.Context) (steamui.State, error)
	Set(ctx context.Context, gen uint64, scale float64) (float64, error)
	Auto(ctx context.Context, gen uint64) error
	Views(ctx context.Context) ([]steamui.View, error)
	Close()
}

// GET /display steam_ui.
const (
	steamUIOK          = "ok"
	steamUIIdle        = "idle"
	steamUIStarting    = "starting"
	steamUIResumed     = "resumed"
	steamUINoDebugger  = "no-debugger"
	steamUIUnsupported = "unsupported"
	steamUIOff         = "off"
)

const (
	// scaleTolerance: Steam keeps its factor to about 0.01.
	scaleTolerance = 0.02
	// viewTolerance is how far a laid-out view's devicePixelRatio may be
	// from the scale.
	viewTolerance = 0.01
	// A change in Steam is the user's once vosd's last set is adoptQuiet
	// old and two reads adoptConfirm apart agree on it.
	adoptQuiet   = 10 * time.Second
	adoptConfirm = 5 * time.Second
	// Steam's late views are looked at every round for viewsEarly after a
	// set, then every viewsEvery.
	viewsEarly   = 60 * time.Second
	viewsEvery   = 30 * time.Second
	viewsTimeout = 5 * time.Second
	// reresolveWindow is how long after Begin an ambiguous client may be
	// told apart by Sunshine's RTSP connections.
	reresolveWindow = 60 * time.Second
	// Steam's debugger is gone after debuggerFails rounds in a row whose
	// first read failed, once Steam has been up for debuggerGrace.
	debuggerFails = 6
	debuggerGrace = 3 * time.Minute
	// genCeiling is the highest marker in Steam vosd's generations follow:
	// far above any UnixMilli they start from, far below the largest
	// steamui sends, so a marker above it is not VaporOS's.
	genCeiling = 1 << 52
	// resourceManager is the root window property Xlib takes Xft.dpi from.
	resourceManager = "RESOURCE_MANAGER"
)

// screenPlan is a session's screen: the device, the kind of screen it has,
// and the user's size for it.
type screenPlan struct {
	// id is "" when the device cannot be told apart (inference only).
	id, name, mac, ip string
	// sig is what the inference saw, the user's pick aside.
	sig        Signals
	kind       Kind
	from       Source
	guess      Kind
	guessFrom  Source
	vetoed     bool // kind is this session's veto, never the user's
	size       float64
	steamAuto  bool
	panel      Panel
	asked      edid.Mode // the mode the client asked for
	ambiguous  bool      // Begin saw no single client address
	lastSeen   time.Time
	savedScale float64 // ui_scale and game_dpi stored before this session
	savedDPI   int
}

// infer works out the kind in effect, with you as the user's pick.
func (p *screenPlan) infer(you Kind) {
	sig := p.sig
	sig.You = you
	inf := InferKind(sig)
	p.kind, p.from, p.guess, p.guessFrom, p.vetoed = inf.Kind, inf.From, inf.Guess, inf.GuessFrom, inf.Vetoed
}

// scale is the Steam scale for the screen on the scanout mode scan.
func (p screenPlan) scale(scan edid.Mode) float64 { return ScaleFor(p.kind, scan, p.size, p.panel) }

// ref is the screen as session.begin carries it, with what vosd aims for
// on scan (ui_scale 0 on Steam's own automatic scale).
func (p screenPlan) ref(scan edid.Mode) ScreenRef {
	s := p.scale(scan)
	dpi := GameDPI(s, scan)
	if p.steamAuto {
		s, dpi = 0, GameDPI(steamAutoScale(scan), scan)
	}
	return ScreenRef{ID: p.id, Name: p.name, Kind: p.kind, KindFrom: p.from, UIScale: s, GameDPI: dpi}
}

// view is the screen as GET /display lists it while it streams on mode,
// with what the scaler applied in this session (else what it applied
// before) and the sizes within which its scale moves there.
func (p screenPlan) view(uiScale float64, gameDPI int, mode edid.Mode) ScreenView {
	if uiScale == 0 && gameDPI == 0 {
		uiScale, gameDPI = p.savedScale, p.savedDPI
	}
	v := ScreenView{
		ID: p.id, Name: p.name, Kind: p.kind, KindFrom: p.from, Guess: p.guess,
		Size: p.size, SteamAuto: p.steamAuto, Mode: p.asked.String(), LastSeen: p.lastSeen,
		UIScale: uiScale, GameDPI: gameDPI, Savable: p.id != "",
	}
	v.SizeMin, v.SizeMax = SizeRange(p.kind, mode, p.panel)
	return v
}

// scanMode is the mode the session's Begin ended with, else the one its
// device asked for.
func (s *sessionInfo) scanMode() edid.Mode {
	if md, err := edid.ParseMode(s.Mode); err == nil {
		return md
	}
	if s.plan != nil {
		return s.plan.asked
	}
	return edid.Mode{}
}

// screenView is the session's screen as GET /display lists it.
func (s *sessionInfo) screenView() ScreenView {
	return s.plan.view(s.uiScale, s.gameDPI, s.scanMode())
}

// scaleWant is what the scaler should hold: the screen of sess (its plan)
// on the mode Begin ended with.
type scaleWant struct {
	gen   uint64
	sess  *sessionInfo
	mode  edid.Mode
	since time.Time // when Begin handed it over
	// For an ambiguous client: who was in Moonlight's /launch all through
	// Begin, and the connections on Sunshine's RTSP port when it handed
	// over, which the client, still in /launch, has not made yet.
	cands      peerSet
	rtspBefore connSet
	// resumed: another device may have taken the stream over without a
	// begin; hold nothing and adopt nothing until the next one.
	resumed bool
}

// scaleState is the scaler's memory (guarded by scaleMu).
type scaleState struct {
	held heldScale
	// Steam's process: since when vosd has seen it, its debugger's failed
	// reads in a row, whether a restart was asked for it, and the last
	// stranger on the debugger's port that was logged.
	steamPID   int
	steamSince time.Time
	fails      int
	askedPID   int
	ownerLog   string
	// The games' X display of Steam's process gamePID, and the last foreign
	// RESOURCE_MANAGER that was logged.
	gamePID    int
	gameDisp   string
	foreignLog string
	// markerLog is the last scale marker in Steam that was not VaporOS's
	// and was logged.
	markerLog uint64
}

// heldScale is what the scaler holds in Steam for one generation.
type heldScale struct {
	gen uint64
	// auto: Steam's automatic scale, else value. adopted: the user chose it
	// in Steam during this generation, and it is no longer recomputed.
	auto    bool
	value   float64
	adopted bool
	// What vosd last sent for this generation (sentValue as Steam clamped
	// it), and when.
	sent      bool
	sentAuto  bool
	sentFor   float64
	sentValue float64
	lastSet   time.Time
	// settled: Steam showed what was sent; ours is its factor then (0 on
	// its automatic scale), with its display name and process.
	settled   bool
	settledAt time.Time
	ours      float64
	name      string
	pid       int
	viewsAt   time.Time
	// viewsSent: the views vosd set its value again for, once each.
	viewsSent map[string]bool
	// seen is a change in Steam first seen, until a second read agrees.
	seen *steamSeen
	// warned: Steam ignoring a set was logged.
	warned bool
}

// steamSeen is Steam's scale as one read saw it.
type steamSeen struct {
	auto  bool
	value float64
	at    time.Time
}

func (s steamSeen) same(o steamSeen) bool {
	return s.auto == o.auto && (s.auto || math.Abs(s.value-o.value) <= 0.005)
}

// needsSet: Steam does not hold what vosd sent for gen, or vosd holds
// something else now (Steam restarted and lost VaporOS's marker, or the
// scanout changed the value), or Steam's bounds clamped the value when they
// were still the last mode's and allow more now.
func (h *heldScale) needsSet(st steamui.State) bool {
	if st.Gen != h.gen || !h.sent || h.sentAuto != h.auto || (!h.auto && h.sentFor != h.value) {
		return true
	}
	if h.auto || h.adopted || !(st.Min > 0 && st.Max >= st.Min) {
		return false
	}
	return math.Abs(min(max(h.value, st.Min), st.Max)-h.sentValue) > scaleTolerance
}

// shows: Steam has taken what vosd sent.
func (h *heldScale) shows(st steamui.State) bool {
	if h.sentAuto {
		return st.Auto
	}
	return !st.Auto && math.Abs(st.Current-h.sentValue) <= scaleTolerance
}

// holdsOurs: Steam still shows what it settled on.
func (h *heldScale) holdsOurs(st steamui.State) bool {
	if h.auto {
		return st.Auto
	}
	return !st.Auto && math.Abs(st.Current-h.ours) <= scaleTolerance
}

// kickScale wakes the scaler.
func (m *Manager) kickScale() {
	select {
	case m.scaleKick <- struct{}{}:
	default:
	}
}

// dropWantLocked leaves the scaler nothing to hold, and drops what it is
// doing for an older generation.
func (m *Manager) dropWantLocked() {
	m.scaleGen++
	m.want = nil
}

// renewWantLocked starts a new generation for what the scaler holds, so it
// applies it again.
func (m *Manager) renewWantLocked() {
	m.scaleGen++
	if m.want != nil {
		w := *m.want
		w.gen = m.scaleGen
		m.want = &w
	}
}

// wantCurrent reports whether w is still what the scaler should hold.
func (m *Manager) wantCurrent(w scaleWant) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.want != nil && m.want.gen == w.gen && m.session == w.sess
}

// samplePeers is who is in Moonlight's /launch now, among prev when there
// was an earlier sample.
func (m *Manager) samplePeers(prev peerSet) peerSet {
	p, err := m.h.Peers()
	if err != nil || p == nil {
		p = peerSet{}
	}
	if prev == nil {
		return p
	}
	return prev.and(p)
}

// screensLocked is screens.json in memory, read on first use. Callers hold
// screensMu.
func (m *Manager) screensLocked() *Screens {
	if m.screens == nil {
		s, err := LoadScreens(config.ScreensPath())
		if err != nil {
			log.Printf("display: %v", err)
		}
		m.screens = s
	}
	return m.screens
}

// saveScreensLocked writes screens.json. Callers hold screensMu.
func (m *Manager) saveScreensLocked() error {
	err := m.screensLocked().Save(config.ScreensPath())
	if err != nil {
		log.Printf("display: saving screens: %v", err)
	}
	return err
}

// resolveScreen works out the screen of a session's device: its key, from
// the client's name and its address when peers names one; then, from
// screens.json, the device's browser hint and the mode it asked for, its
// kind and size. A device with a key is recorded there (mode, time, guess),
// and a kind picked when it paired becomes its screen's own pick.
func (m *Manager) resolveScreen(name, audio string, asked edid.Mode, peers peerSet) screenPlan {
	p := screenPlan{name: name, asked: asked, size: 1}
	addr, ok := peers.one()
	p.ambiguous = !ok
	if ok {
		p.ip, p.mac = addr.String(), m.h.NeighbourMAC(addr)
	}
	now := m.now().UTC()

	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	s := m.screensLocked()
	p.id = ScreenID(s.KeyFor(name, p.mac, p.ip, now))
	var prev Screen
	known := false
	if p.id != "" {
		prev, known = s.Get(p.id)
	}
	var you Kind
	if known {
		you, p.size, p.steamAuto = prev.Kind, normSize(prev.Size), prev.SteamAuto
		p.savedScale, p.savedDPI = prev.UIScale, prev.GameDPI
	}
	// Taken before the hint is read, so neither this session's guess nor
	// a later one sees the pick as the browser's.
	picked := false
	if p.id != "" {
		if k, ok := s.takePick(p.mac, p.ip, now); ok {
			if k != you {
				p.size = 1 // as PUT: a new kind resets the size
			}
			you, p.steamAuto, picked = k, false, true
		}
	}
	p.sig = Signals{Name: name, Hint: s.HintFor(p.mac, p.ip, now), Mode: asked, History: prev.History(), Audio: audio}
	p.panel = PanelFor(p.sig.Hint, asked, p.sig.History)
	p.lastSeen = now
	p.infer(you)
	if p.id != "" {
		s.Touch(p.id, name, p.mac, p.ip, asked, now)
		s.Update(p.id, func(sc *Screen) {
			sc.Guess, sc.GuessFrom = p.guess, p.guessFrom
			if picked {
				sc.Kind, sc.Size, sc.SteamAuto = you, p.size, false
			}
		})
		m.saveScreensLocked()
	}
	return p
}

// beginScreen resolves the screen of a session that Begin starts, from
// the client addresses sampled so far and once more now, and puts it on
// the session, so every session.begin carries it. It returns the plan it
// put there.
func (m *Manager) beginScreen(sess *sessionInfo, req session.Request, asked, mode edid.Mode, peers *peerSet) *screenPlan {
	*peers = m.samplePeers(*peers)
	plan := m.resolveScreen(sess.Client, req.Audio, asked, *peers)
	on := m.displayConfig().UIScaling
	m.mu.Lock()
	sess.plan = &plan
	if on {
		ref := plan.ref(mode)
		sess.screen = &ref
	}
	m.mu.Unlock()
	return &plan
}

// handOver gives the scaler what the session wants, once Begin is done
// with the mode, unless a newer Begin came meanwhile: the screen again
// when a last sample of the client addresses only now tells the client,
// and the session.begin values for the mode Begin ends with. A screen
// that a PUT or DELETE changed while Begin switched (the session's plan is
// no longer orig) stays as they left it. For a client still ambiguous it
// keeps who was in /launch all along and the RTSP connections there are
// now, which the client has not made yet: only new ones can tell it.
func (m *Manager) handOver(seq uint64, sess *sessionInfo, req session.Request, orig *screenPlan, mode edid.Mode, peers peerSet) {
	plan, fresh := *orig, false
	final := m.samplePeers(peers)
	if plan.ambiguous {
		if _, ok := final.one(); ok {
			plan, fresh = m.resolveScreen(sess.Client, req.Audio, plan.asked, final), true
		}
	}
	var rtsp connSet // nil (none taken): no RTSP connection tells the client
	if plan.ambiguous {
		if c, err := m.h.RTSPConns(); err == nil {
			rtsp = c
		}
	}
	on := m.displayConfig().UIScaling
	m.mu.Lock()
	defer m.mu.Unlock()
	if fresh || sess.plan == orig || sess.plan == nil {
		sess.plan = &plan
	}
	if on {
		ref := sess.plan.ref(mode)
		sess.screen = &ref
	}
	if m.session != sess || m.beginSeq != seq {
		return
	}
	m.scaleGen++
	m.want = &scaleWant{gen: m.scaleGen, sess: sess, mode: mode, since: m.now(), cands: final, rtspBefore: rtsp}
}

// watchScale runs the scaler until ctx ends: a round whenever it is kicked
// and every scaleEvery.
func (m *Manager) watchScale(ctx context.Context) {
	if m.scaleEvery <= 0 {
		return
	}
	defer m.h.SteamUI().Close()
	t := time.NewTicker(m.scaleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.scaleKick:
		}
		m.scaleRound(ctx)
	}
}

// scaleRound is one round of the scaler. It never acts while a Begin (or
// the policy) switches units; Begin kicks it once it is done.
func (m *Manager) scaleRound(ctx context.Context) {
	m.scaleMu.Lock()
	defer m.scaleMu.Unlock()
	if !m.op.TryLock() {
		return
	}
	m.op.Unlock()

	if !m.displayConfig().UIScaling {
		if m.handbackPending() {
			m.handback(ctx)
		}
		m.setSteamUI(steamUIOff)
		return
	}
	m.mu.Lock()
	var w scaleWant
	var plan screenPlan
	active := m.want != nil && m.want.sess == m.session && m.session != nil && m.session.plan != nil &&
		m.state == StateGaming && m.canGameLocked()
	if active {
		w, plan = *m.want, *m.want.sess.plan
	}
	card := m.gpu.Card
	m.mu.Unlock()
	if !active {
		m.setSteamUI(steamUIIdle)
		return
	}
	if m.reresolve(w, plan) {
		m.kickScale()
		return
	}
	if m.sc.held.gen != w.gen {
		m.sc.held = heldScale{gen: w.gen}
	}
	h := &m.sc.held
	scan := w.mode
	if cur, on, err := m.h.Scanout(card, m.virtual()); err == nil && on {
		scan = cur
	}
	if !h.adopted {
		h.auto, h.value = plan.steamAuto, plan.scale(scan)
	}
	pid := m.h.SteamPID()
	st, err := m.readSteam(ctx, pid)
	if !m.wantCurrent(w) {
		return
	}
	status := steamUIStatus(err)
	if err == nil && st.Gen >= genCeiling {
		status = steamUINoDebugger // someone else's marker blocks every set (passStale)
	}
	m.setSteamUI(status)
	if w.resumed {
		return
	}
	m.syncGameDPI(ctx, w, plan, pid, m.dpiFor(st, err == nil, scan))
	if err != nil || !m.wantCurrent(w) {
		return
	}
	switch {
	case h.needsSet(st):
		m.applyHeld(ctx, w, plan, pid)
	case !h.settled && h.shows(st):
		m.settleHeld(w, plan, st, pid)
	case !h.settled:
		m.applyHeld(ctx, w, plan, pid)
	default:
		m.watchSteam(ctx, w, plan, st, pid, scan)
		// Only while Steam shows vosd's own value: a view at another scale
		// then was laid out late. Otherwise the scale moved, and
		// watchSteam decides whose doing that was.
		if h.seen == nil && h.holdsOurs(st) {
			m.checkViews(ctx, w)
		}
	}
}

// steamUIStatus is steam_ui for what a call to Steam's debugger returned.
func steamUIStatus(err error) string {
	var thrown *steamui.EvalError
	switch {
	case err == nil:
		return steamUIOK
	case errors.Is(err, steamui.ErrNotReady), errors.Is(err, steamui.ErrNoTarget), errors.As(err, &thrown):
		return steamUIStarting
	case errors.Is(err, steamui.ErrUnsupported):
		return steamUIUnsupported
	}
	return steamUINoDebugger
}

// setSteamUI records steam_ui and publishes display.changed when it moved.
func (m *Manager) setSteamUI(s string) {
	m.mu.Lock()
	changed := m.steamUI != s
	m.steamUI = s
	m.mu.Unlock()
	if changed {
		m.hub.Publish("display.changed", struct{}{})
	}
}

// steamUIView is steam_ui as GET /display reports it now.
func (m *Manager) steamUIView(on bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !on:
		return steamUIOff
	case m.session == nil || m.state != StateGaming || !m.canGameLocked():
		return steamUIIdle
	case m.want != nil && m.want.sess == m.session && m.want.resumed:
		return steamUIResumed
	}
	switch m.steamUI {
	case steamUIOK, steamUINoDebugger, steamUIUnsupported:
		return m.steamUI
	}
	return steamUIStarting
}

// steamRead reads Steam's display scale within scaleCall.
func (m *Manager) steamRead(ctx context.Context) (steamui.State, error) {
	rctx, cancel := context.WithTimeout(ctx, m.scaleCall)
	defer cancel()
	return m.h.SteamUI().Read(rctx)
}

// readSteam is a round's first read, the one that counts the debugger's
// failures: a round's later reads come moments after it, and a blip there
// would count as many rounds.
func (m *Manager) readSteam(ctx context.Context, pid int) (steamui.State, error) {
	st, err := m.steamRead(ctx)
	m.noteDebugger(err, pid)
	return st, err
}

// noteDebugger counts the debugger's failed reads, one per round. Steam's
// own "not yet" and an exception inside it are answers. Once Steam has
// been up debuggerGrace and failed debuggerFails reads in a row, its
// debugger is gone (Steam kept an old socket, or something else holds the port): vosd
// asks for a Steam restart, once per Steam process.
func (m *Manager) noteDebugger(err error, pid int) {
	sc := &m.sc
	now := m.now()
	if pid != sc.steamPID {
		sc.steamPID, sc.steamSince, sc.fails = pid, now, 0
	}
	var thrown *steamui.EvalError
	if err == nil || errors.Is(err, steamui.ErrNotReady) || errors.Is(err, steamui.ErrUnsupported) || errors.As(err, &thrown) {
		sc.fails = 0
		return
	}
	sc.fails++
	var stranger *steamui.ListenerError
	if errors.As(err, &stranger) && stranger.Error() != sc.ownerLog {
		sc.ownerLog = stranger.Error()
		log.Printf("display: %v", stranger)
	}
	if pid == 0 || sc.fails < debuggerFails || now.Sub(sc.steamSince) <= debuggerGrace || sc.askedPID == pid {
		return
	}
	sc.askedPID = pid
	log.Printf("display: Steam (pid %d) failed %d reads of its debugger in a row (%v); asking for a Steam restart", pid, sc.fails, err)
	m.RestartSteam("Steam's debugger is gone", false)
}

// applyHeld sets what the scaler holds in Steam, then reads Steam back
// every scaleStep until it shows it, setting it once more after
// scaleResend, for up to scaleReadback of real time, so slow reads do not
// stretch it. What Steam then shows is vosd's own.
func (m *Manager) applyHeld(ctx context.Context, w scaleWant, plan screenPlan, pid int) {
	if !m.sendHeld(ctx, w) {
		return
	}
	h := &m.sc.held
	step := max(m.scaleStep, time.Millisecond)
	start := time.Now()
	resent := false
	for {
		if !sleepCtx(ctx, step) || !m.wantCurrent(w) {
			return
		}
		st, err := m.steamRead(ctx)
		if !m.wantCurrent(w) {
			return
		}
		if err == nil && h.shows(st) {
			m.settleHeld(w, plan, st, pid)
			return
		}
		// The second set always gets a read back.
		elapsed := time.Since(start)
		if !resent && elapsed >= m.scaleResend {
			resent = true
			if !m.sendHeld(ctx, w) {
				return
			}
			continue
		}
		if elapsed >= m.scaleReadback {
			break
		}
	}
	if !h.warned {
		h.warned = true
		log.Printf("display: Steam did not take %s within %s; trying again", h.describe(), m.scaleReadback)
	}
}

func (h *heldScale) describe() string {
	if h.auto {
		return "its automatic scale"
	}
	return "scale " + strconv.FormatFloat(h.value, 'f', 2, 64)
}

// sendHeld sends what the scaler holds to Steam, for w's generation. It
// reports whether Steam took the call.
func (m *Manager) sendHeld(ctx context.Context, w scaleWant) bool {
	h := &m.sc.held
	if !h.auto && h.value <= 0 {
		return false
	}
	ui := m.h.SteamUI()
	sctx, cancel := context.WithTimeout(ctx, m.scaleCall)
	var v float64
	var err error
	if h.auto {
		err = ui.Auto(sctx, w.gen)
	} else {
		v, err = ui.Set(sctx, w.gen, h.value)
	}
	cancel()
	if !m.wantCurrent(w) {
		return false
	}
	if err != nil {
		if errors.Is(err, steamui.ErrStale) {
			m.passStale(ctx)
		} else {
			m.setSteamUI(steamUIStatus(err))
		}
		return false
	}
	h.sent, h.sentAuto, h.sentFor, h.sentValue = true, h.auto, h.value, v
	h.settled, h.lastSet = false, m.now()
	return true
}

// passStale: Steam holds a generation newer than vosd's, which only a
// clock that went back across a restart of vosd explains. Generations
// continue above Steam's, up to genCeiling: a marker above it is not
// VaporOS's, and following it would take vosd's generations past what
// steamui sends, for good. Such a marker blocks every set until Steam
// loses it (a restart); vosd's generations are fine for after that.
func (m *Manager) passStale(ctx context.Context) {
	st, err := m.steamRead(ctx)
	if err != nil {
		return
	}
	if st.Gen >= genCeiling {
		if st.Gen != m.sc.markerLog {
			m.sc.markerLog = st.Gen
			log.Printf("display: Steam holds a scale marker that is not VaporOS's (generation %d); Steam's scale stays as it is until Steam restarts", st.Gen)
		}
		return
	}
	m.mu.Lock()
	if m.scaleGen <= st.Gen {
		m.scaleGen = st.Gen
		m.renewWantLocked()
	}
	m.mu.Unlock()
	m.kickScale()
}

// settleHeld records that Steam shows what was sent: its factor is vosd's own
// from now on.
func (m *Manager) settleHeld(w scaleWant, plan screenPlan, st steamui.State, pid int) {
	h := &m.sc.held
	h.settled, h.settledAt, h.seen = true, m.now(), nil
	h.ours = 0
	if !st.Auto {
		h.ours = st.Current
	}
	h.name, h.pid = st.Name, pid
	ui := h.ours
	m.noteApplied(w, plan, &ui, nil)
}

// watchSteam looks at what Steam shows once it settled. A move away from
// vosd's own value is Steam's doing when Steam restarted, names another
// display, the scanout is not the session's mode or vosd set it moments
// ago: then it sets its value again. Otherwise, once two reads
// adoptConfirm apart agree, it is the user's, made with Steam's own
// setting, and adopted.
func (m *Manager) watchSteam(ctx context.Context, w scaleWant, plan screenPlan, st steamui.State, pid int, scan edid.Mode) {
	h := &m.sc.held
	if h.holdsOurs(st) {
		h.seen = nil
		return
	}
	now := m.now()
	if st.Name != h.name || pid != h.pid || scan != w.mode || now.Sub(h.lastSet) < adoptQuiet {
		h.seen = nil
		m.applyHeld(ctx, w, plan, pid)
		return
	}
	seen := steamSeen{auto: st.Auto, value: st.Current, at: now}
	if h.seen == nil || !h.seen.same(seen) {
		h.seen = &seen
		return
	}
	if now.Sub(h.seen.at) < adoptConfirm {
		return
	}
	m.adopt(w, plan, st, pid, scan)
}

// adopt takes what the user set in Steam as the screen's own: Steam's
// automatic scale as steam_auto, a factor as the size that gives it back,
// with the kind in effect pinned as the user's, unless that kind is this
// session's veto (never stored): then the size alone is kept, for whatever
// kind a session infers. vosd sets nothing more in this generation. A
// device that cannot be told apart keeps it for this session only.
func (m *Manager) adopt(w scaleWant, plan screenPlan, st steamui.State, pid int, scan edid.Mode) {
	h := &m.sc.held
	h.adopted, h.seen = true, nil
	h.auto, h.value = st.Auto, 0
	if !st.Auto {
		h.value = st.Current
	}
	h.sent, h.sentAuto, h.sentFor, h.sentValue = true, h.auto, h.value, h.value
	h.ours, h.name, h.pid = h.value, st.Name, pid
	ui := h.ours
	if plan.id == "" {
		log.Printf("display: %s's interface was changed in Steam; this device cannot be told apart, so that lasts for this session only", plan.name)
		m.noteApplied(w, plan, &ui, nil)
		return
	}
	np := plan
	what := "Steam's own automatic size"
	if st.Auto {
		np.steamAuto = true
	} else {
		np.size = AdoptSize(st.Current, plan.kind, scan, plan.panel)
		np.steamAuto = false
		if !plan.vetoed {
			np.from = FromYou
		}
		what = fmt.Sprintf("scale %.2f (size %.2f as a %s)", st.Current, np.size, np.kind)
	}
	m.screensMu.Lock()
	m.screensLocked().Update(np.id, func(sc *Screen) {
		sc.SteamAuto = np.steamAuto
		if !st.Auto {
			sc.Size = np.size
			if !plan.vetoed {
				sc.Kind = np.kind
			}
		}
	})
	m.saveScreensLocked()
	m.screensMu.Unlock()
	log.Printf("display: %s's interface was set to %s in Steam; keeping it for that screen", plan.name, what)
	m.mu.Lock()
	if m.want != nil && m.want.gen == w.gen && m.session == w.sess {
		w.sess.plan = &np
	}
	m.mu.Unlock()
	m.noteApplied(w, np, &ui, nil)
	m.hub.Publish("display.changed", struct{}{})
}

// checkViews sets vosd's value again when one of Steam's late views
// (Quick Access, the main menu), laid out by now, shows another scale:
// Steam lays them out when first shown, at the scale of that moment. Every
// round for viewsEarly after Steam settled, then every viewsEvery, and
// only while Steam shows vosd's value (the caller saw it, and a last read
// right before the set still does), so it never undoes a change of the
// user's. Each view gets it once per generation: one that stays off after
// that is not one Steam lays out (a page can take such a title). The
// value is the one Steam shows already, so it is no set that would make
// a change in Steam look like Steam's own doing (lastSet stays).
func (m *Manager) checkViews(ctx context.Context, w scaleWant) {
	h := &m.sc.held
	if !h.settled || h.auto || h.ours <= 0 || !m.wantCurrent(w) {
		return
	}
	now := m.now()
	if now.Sub(h.settledAt) > viewsEarly && now.Sub(h.viewsAt) < viewsEvery {
		return
	}
	h.viewsAt = now
	ui := m.h.SteamUI()
	vctx, cancel := context.WithTimeout(ctx, viewsTimeout)
	views, err := ui.Views(vctx)
	cancel()
	if err != nil || !m.wantCurrent(w) {
		return
	}
	var off []string
	for _, v := range views {
		if v.Height > 1 && math.Abs(v.DPR-h.ours) > viewTolerance && !h.viewsSent[v.ID] {
			off = append(off, v.ID)
		}
	}
	if len(off) == 0 {
		return
	}
	if st, err := m.steamRead(ctx); err != nil || !m.wantCurrent(w) || !h.holdsOurs(st) {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, m.scaleCall)
	_, err = ui.Set(sctx, w.gen, h.ours)
	cancel()
	if err != nil || !m.wantCurrent(w) {
		return
	}
	if h.viewsSent == nil {
		h.viewsSent = map[string]bool{}
	}
	for _, id := range off {
		h.viewsSent[id] = true
	}
}

// dpiFor is the game DPI that goes with what Steam holds: vosd's value
// once Steam showed it, Steam's own automatic value on its automatic
// scale, else what vosd computed.
func (m *Manager) dpiFor(st steamui.State, readOK bool, scan edid.Mode) int {
	h := &m.sc.held
	s := h.value
	switch {
	case h.auto && readOK && st.AutoValue > 0:
		s = st.AutoValue
	case h.auto:
		s = steamAutoScale(scan)
	case h.settled:
		s = h.ours
	}
	return GameDPI(s, scan)
}

// noteApplied stores what the scaler applied (nil: unchanged) on the
// session and its screen, and publishes display.changed when it moved.
func (m *Manager) noteApplied(w scaleWant, plan screenPlan, ui *float64, dpi *int) {
	changed := false
	m.mu.Lock()
	if m.session == w.sess {
		if ui != nil && w.sess.uiScale != *ui {
			w.sess.uiScale, changed = *ui, true
		}
		if dpi != nil && w.sess.gameDPI != *dpi {
			w.sess.gameDPI, changed = *dpi, true
		}
	}
	m.mu.Unlock()
	if plan.id != "" {
		m.screensMu.Lock()
		stored := false
		m.screensLocked().Update(plan.id, func(sc *Screen) {
			if ui != nil && sc.UIScale != *ui {
				sc.UIScale, stored = *ui, true
			}
			if dpi != nil && sc.GameDPI != *dpi {
				sc.GameDPI, stored = *dpi, true
			}
		})
		if stored {
			m.saveScreensLocked()
		}
		m.screensMu.Unlock()
		changed = changed || stored
	}
	if changed {
		m.hub.Publish("display.changed", struct{}{})
	}
}

var (
	// gameDisplayName is what STEAM_GAME_DISPLAY_0 may be.
	gameDisplayName = regexp.MustCompile(`^:[0-9]{1,3}$`)
	// steamDisplayName is Steam's DISPLAY, its display number in [1].
	steamDisplayName = regexp.MustCompile(`^[^:]*:([0-9]{1,3})(\.[0-9]+)?$`)
	// dpiLine is VaporOS's whole RESOURCE_MANAGER.
	dpiLine = regexp.MustCompile(`^Xft\.dpi:\t[0-9]{1,4}\n$`)
)

// gameDisplay is the X display gamescope gives games, from the
// environment of Steam's process pid (cached per process): never Steam's
// own DISPLAY, where CEF would scale twice. "" when unknown.
func (m *Manager) gameDisplay(pid int) string {
	sc := &m.sc
	if pid == 0 {
		return ""
	}
	if sc.gamePID == pid && sc.gameDisp != "" {
		return sc.gameDisp
	}
	env := m.h.SteamEnv(pid, "STEAM_GAME_DISPLAY_0", "DISPLAY")
	game, own := env["STEAM_GAME_DISPLAY_0"], steamDisplayName.FindStringSubmatch(env["DISPLAY"])
	if !gameDisplayName.MatchString(game) || own == nil {
		return ""
	}
	gn, _ := strconv.Atoi(game[1:])
	if sn, _ := strconv.Atoi(own[1]); gn == sn {
		return ""
	}
	sc.gamePID, sc.gameDisp = pid, game
	return game
}

// syncGameDPI keeps Xft.dpi at dpi on the games' X display. It writes the
// root window's RESOURCE_MANAGER only when it is unset or holds VaporOS's
// Xft.dpi line alone; anything else there is someone else's and stays.
// Programs read it when they start.
func (m *Manager) syncGameDPI(ctx context.Context, w scaleWant, plan screenPlan, pid, dpi int) {
	disp := m.gameDisplay(pid)
	if disp == "" {
		return
	}
	out, err := m.h.XpropOn(ctx, disp, "-root", resourceManager)
	if err != nil {
		return
	}
	cur, set, ok := parseXpropString(out, resourceManager)
	line := fmt.Sprintf("Xft.dpi:\t%d\n", dpi)
	switch {
	case set && ok && cur == line:
	case !set || (ok && dpiLine.MatchString(cur)):
		if _, err := m.h.XpropOn(ctx, disp, "-root", "-f", resourceManager, "8s", "-set", resourceManager, line); err != nil {
			log.Printf("display: setting Xft.dpi %d on %s: %v", dpi, disp, err)
			return
		}
	default:
		if key := fmt.Sprintf("%d %s %q", pid, disp, cur); key != m.sc.foreignLog {
			m.sc.foreignLog = key
			log.Printf("display: leaving %s's %s alone (not VaporOS's): %q", disp, resourceManager, cur)
		}
		return
	}
	m.noteApplied(w, plan, nil, &dpi)
}

// removeGameDPI takes VaporOS's Xft.dpi off the games' X display. It
// reports whether none of VaporOS's is left there, as far as it can tell.
func (m *Manager) removeGameDPI(ctx context.Context, pid int) bool {
	disp := m.gameDisplay(pid)
	if disp == "" {
		return true
	}
	out, err := m.h.XpropOn(ctx, disp, "-root", resourceManager)
	if err != nil {
		return false
	}
	if cur, set, ok := parseXpropString(out, resourceManager); !set || !ok || !dpiLine.MatchString(cur) {
		return true
	}
	_, err = m.h.XpropOn(ctx, disp, "-root", "-remove", resourceManager)
	return err == nil
}

// handbackPending reports whether Steam still has to get its automatic
// scale back.
func (m *Manager) handbackPending() bool {
	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	return m.screensLocked().HandbackPending
}

// handback gives Steam its automatic scale back and takes VaporOS's
// Xft.dpi away, after display.ui_scaling was turned off; it stays pending
// (in screens.json, across restarts) until Steam answered.
func (m *Manager) handback(ctx context.Context) {
	m.mu.Lock()
	gen := m.scaleGen
	m.mu.Unlock()
	actx, cancel := context.WithTimeout(ctx, m.scaleCall)
	err := m.h.SteamUI().Auto(actx, gen)
	cancel()
	if errors.Is(err, steamui.ErrStale) {
		m.passStale(ctx)
		return
	}
	if err != nil || !m.removeGameDPI(ctx, m.h.SteamPID()) {
		return
	}
	m.screensMu.Lock()
	s := m.screensLocked()
	s.HandbackPending = false
	m.saveScreensLocked()
	m.screensMu.Unlock()
	log.Printf("display: Steam has its own automatic interface size back")
}

// scalingChanged follows display.ui_scaling turned on or off: off hands
// Steam its automatic scale back (pending until it answers), on sizes the
// running session again. The session in progress carries its screen only
// while scaling is on, as its session.begin would.
func (m *Manager) scalingChanged(on bool) {
	m.screensMu.Lock()
	s := m.screensLocked()
	if s.HandbackPending != !on {
		s.HandbackPending = !on
		m.saveScreensLocked()
	}
	m.screensMu.Unlock()
	m.mu.Lock()
	if sess := m.session; sess != nil && sess.plan != nil {
		sess.screen = nil
		if on {
			ref := sess.plan.ref(sess.scanMode())
			sess.screen = &ref
		}
	}
	m.renewWantLocked()
	m.mu.Unlock()
	m.kickScale()
}

// reresolve tells the client of an ambiguous begin apart, within
// reresolveWindow, by the connections on Sunshine's RTSP port, which only
// the device whose stream starts makes right after /launch: the ones that
// were not there when Begin handed over (another device's, in TIME_WAIT
// after its own stream, or held open), from an address that was in
// /launch all through Begin. When that names one, the screen is resolved
// again and applied once more. It reports whether it did.
func (m *Manager) reresolve(w scaleWant, plan screenPlan) bool {
	if !plan.ambiguous || w.resumed || w.rtspBefore == nil || m.now().Sub(w.since) > reresolveWindow {
		return false
	}
	conns, err := m.h.RTSPConns()
	if err != nil {
		return false
	}
	peers := conns.addrs(w.rtspBefore)
	if len(w.cands) > 0 {
		peers = peers.and(w.cands)
	}
	if _, ok := peers.one(); !ok {
		return false
	}
	np := m.resolveScreen(plan.name, plan.sig.Audio, plan.asked, peers)
	on := m.displayConfig().UIScaling
	m.mu.Lock()
	if m.want == nil || m.want.gen != w.gen || m.session != w.sess {
		m.mu.Unlock()
		return false
	}
	w.sess.plan = &np
	if on {
		ref := np.ref(w.mode)
		w.sess.screen = &ref
	}
	m.renewWantLocked()
	m.mu.Unlock()
	log.Printf("display: session of %s is %s's (%s), from Sunshine's RTSP connection", plan.name, np.ip, np.kind)
	m.hub.Publish("display.changed", struct{}{})
	return true
}

// NoteResume is Sunshine seeing a stream resumed with no begin: the
// device may be another than the one that began it, so until the next
// begin the scaler neither holds Steam's scale nor adopts a change, and
// steam_ui says so: a change to the screen applies from its next begin.
func (m *Manager) NoteResume() {
	m.mu.Lock()
	changed := m.want != nil && !m.want.resumed
	if changed {
		m.renewWantLocked()
		m.want.resumed = true
	}
	m.mu.Unlock()
	if changed {
		m.hub.Publish("display.changed", struct{}{})
	}
	m.kickScale()
}

// PairHint stores what pairing learned about the device at remote: the
// kind its user picked ("" for none, the kinds of PUT
// /display/screens/{id} but auto), else, when the device has no hint yet,
// what its browser's User-Agent alone tells (a phone, tablet, handheld or
// TV; a computer's screen needs POST /display/hint). It is kept under the
// device's MAC, else its IPv4 address; at an address several devices share
// (see SharedAddr) nothing is kept.
func (m *Manager) PairHint(remote net.IP, kind string, userAgent string) {
	a, ok := remoteAddr(remote)
	if !ok {
		return
	}
	mac, ip, now := m.h.NeighbourMAC(a), a.String(), m.now().UTC()
	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	s := m.screensLocked()
	stored := false
	if k, ok := UserKind(kind); ok {
		stored = s.PutPick(mac, ip, k, now)
	} else if kind == "" && s.HintFor(mac, ip, now) == nil {
		switch k := ClassifyBrowser(userAgent, 0, 0, 0, 0); k {
		case KindPhone, KindTablet, KindHandheld, KindTV:
			stored = s.PutHint(mac, ip, Hint{Kind: k, At: now})
		}
	}
	if stored {
		m.saveScreensLocked()
	}
}

// BrowserKind is the kind of the hint kept for the device at remote (what
// its browser said, or the kind picked at pairing), "" for none. Pairing
// names a device after its browser with it.
func (m *Manager) BrowserKind(remote net.IP) string {
	a, ok := remoteAddr(remote)
	if !ok {
		return ""
	}
	mac := m.h.NeighbourMAC(a)
	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	h := m.screensLocked().HintFor(mac, a.String(), m.now().UTC())
	if h == nil || !h.Kind.Valid() || h.Kind == KindUnknown {
		return ""
	}
	return string(h.Kind)
}

// SharedAddr reports whether remote's address is one several devices were
// seen at lately (behind a router that NATs them): a browser there may be
// any of them, so pairing does not take it for the device that pairs.
func (m *Manager) SharedAddr(remote net.IP) bool {
	a, ok := remoteAddr(remote)
	if !ok {
		return false
	}
	mac := m.h.NeighbourMAC(a)
	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	return m.screensLocked().sharedAddr(mac, a.String(), m.now().UTC())
}

// remoteAddr is a device's address as pairing reports it, unmapped; false
// for none, loopback and the unspecified address.
func remoteAddr(remote net.IP) (netip.Addr, bool) {
	a, ok := netip.AddrFromSlice(remote)
	if !ok {
		return netip.Addr{}, false
	}
	a = a.Unmap()
	return a, !a.IsLoopback() && !a.IsUnspecified()
}
