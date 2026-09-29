package welcome

import (
	"context"
	"fmt"
	"image"
	"sort"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display/drm"
)

// Timing of the main loop: the state file is polled every second (vosd
// rewrites it atomically), sysfs connector status every two.
const (
	statePoll = time.Second
	scanPoll  = 2 * time.Second
)

// kmsDevice is the part of a DRM card the runner uses; *drm.Card behind
// realKMS in production, a fake in tests.
type kmsDevice interface {
	SetMaster() error
	DropMaster() error
	Close() error
	Resources() (*drm.Resources, error)
	Connector(id uint32, probe bool) (*drm.Connector, error)
	Encoder(id uint32) (*drm.Encoder, error)
	SetCRTC(crtc, fb uint32, connectors []uint32, mode *drm.ModeInfo) error
	DirtyFB(fb uint32) error
	NewBuffer(w, h int) (scanoutBuffer, error)
}

// scanoutBuffer is a CPU-writable framebuffer.
type scanoutBuffer interface {
	FBID() uint32
	Dims() (w, h int)
	RowPitch() int
	Mem() []byte
	Destroy()
}

type realKMS struct{ *drm.Card }

func (k realKMS) NewBuffer(w, h int) (scanoutBuffer, error) {
	fb, err := k.Card.NewFramebuffer(w, h)
	if err != nil {
		return nil, err
	}
	return realBuffer{fb}, nil
}

type realBuffer struct{ fb *drm.Framebuffer }

func (b realBuffer) FBID() uint32     { return b.fb.ID }
func (b realBuffer) Dims() (int, int) { return b.fb.Width, b.fb.Height }
func (b realBuffer) RowPitch() int    { return b.fb.Pitch }
func (b realBuffer) Mem() []byte      { return b.fb.Pixels }
func (b realBuffer) Destroy()         { b.fb.Destroy() }

// Run owns DRM master on every card with connectors and shows the welcome
// screen on each connected connector until ctx ends. It never reads input,
// and while it runs the VT keyboard is off and VT switching locked.
func Run(ctx context.Context, opts Options) error {
	if !platformSupported {
		return drm.ErrUnsupported
	}
	releaseTTY := setGraphics(opts.TTY, &opts)
	defer releaseTTY()

	r := newRunner(&opts)
	defer r.shutdown()

	r.state, _ = stateChanged(opts.StatePath, State{}, false)
	hot, err := drm.WatchHotplug(ctx)
	if err != nil {
		opts.logf("vos welcome: no hotplug events (%v); polling only", err)
	}
	r.scan(true)

	stateTick := time.NewTicker(statePoll)
	defer stateTick.Stop()
	scanTick := time.NewTicker(scanPoll)
	defer scanTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-hot:
			if !ok {
				hot = nil
				continue
			}
			r.scan(true)
		case <-scanTick.C:
			r.scan(r.signature() != r.lastSig)
		case <-stateTick.C:
			if st, changed := stateChanged(opts.StatePath, r.state, true); changed {
				r.state = st
				r.redraw()
			}
		}
	}
}

type runner struct {
	opts    *Options
	open    func(dev string) (kmsDevice, error)
	sysCard func() ([]drm.SysCard, error)
	sysConn func(card string) ([]drm.SysConnector, error)
	cards   map[string]*cardState
	state   State
	lastSig string
}

func newRunner(opts *Options) *runner {
	return &runner{
		opts: opts,
		open: func(dev string) (kmsDevice, error) {
			c, err := drm.Open(dev, true)
			if err != nil {
				return nil, err
			}
			return realKMS{c}, nil
		},
		sysCard: drm.Cards,
		sysConn: drm.Connectors,
		cards:   map[string]*cardState{},
	}
}

type cardState struct {
	name   string
	dev    kmsDevice
	master bool
	outs   map[uint32]*output // by connector id
	noCRTC map[uint32]bool    // connectors already reported as unlit
}

type output struct {
	conn uint32
	name string
	crtc uint32
	mode drm.ModeInfo
	buf  scanoutBuffer
}

// signature summarises connector status and mode lists from sysfs; a
// change means something was plugged or unplugged even if no uevent
// reached us.
func (r *runner) signature() string {
	conns, err := r.sysConn("")
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range conns {
		fmt.Fprintf(&sb, "%s-%s=%s:%s;", c.Card, c.Name, c.Status, strings.Join(c.Modes, ","))
	}
	return sb.String()
}

// scan brings every card's outputs in line with the connected connectors.
// probe asks the kernel to re-detect sinks (only after a hotplug hint).
func (r *runner) scan(probe bool) {
	r.lastSig = r.signature()
	cards, err := r.sysCard()
	if err != nil {
		r.opts.logf("vos welcome: %v", err)
		return
	}
	virtual := ""
	if r.opts.VirtualConnector != nil {
		virtual = r.opts.VirtualConnector()
	}
	seen := map[string]bool{}
	for _, sc := range cards {
		conns, _ := r.sysConn(sc.Name)
		if len(conns) == 0 {
			continue // render-only or display-less GPU
		}
		seen[sc.Name] = true
		cs := r.cards[sc.Name]
		if cs == nil {
			dev, err := r.open(sc.Dev)
			if err != nil {
				r.opts.logf("vos welcome: %s: %v", sc.Dev, err)
				continue
			}
			cs = &cardState{name: sc.Name, dev: dev, outs: map[uint32]*output{}, noCRTC: map[uint32]bool{}}
			r.cards[sc.Name] = cs
		}
		cardProbe := probe
		if !cs.master {
			// The first opener of a master-less card is made master by the
			// kernel, and SET_MASTER on a master fd is a no-op; otherwise
			// someone else (gamescope still stopping) holds it: retry later.
			if err := cs.dev.SetMaster(); err != nil {
				r.opts.logf("vos welcome: waiting for DRM master on %s: %v", sc.Dev, err)
				continue
			}
			cs.master = true
			cardProbe = true
		}
		r.syncCard(cs, cardProbe, virtual)
	}
	for name, cs := range r.cards {
		if !seen[name] {
			cs.release()
			delete(r.cards, name)
		}
	}
}

// want describes one connector we should light.
type want struct {
	conn *drm.Connector
	mode drm.ModeInfo
}

func (r *runner) syncCard(cs *cardState, probe bool, virtual string) {
	res, err := cs.dev.Resources()
	if err != nil {
		r.opts.logf("vos welcome: %s: %v", cs.name, err)
		return
	}
	var wants []want
	for _, id := range res.Connectors {
		conn, err := cs.dev.Connector(id, probe)
		if err != nil || !conn.Connected() || len(conn.Modes) == 0 {
			continue
		}
		var mode drm.ModeInfo
		ok := false
		if conn.Name == virtual {
			// Sunshine probes its encoder on the virtual connector before
			// prep-cmd runs, so give it a plain 1080p60 splash.
			mode, ok = conn.FindMode(1920, 1080, 60)
		}
		if !ok {
			mode, ok = welcomeMode(conn)
		}
		if ok {
			wants = append(wants, want{conn, mode})
		}
	}
	// The virtual connector first, onto the lowest CRTC it can use: its
	// primary plane then comes first in plane order, which is how Sunshine's
	// KMS capture numbers outputs when it probes the encoder at startup.
	sort.SliceStable(wants, func(i, j int) bool {
		return wants[i].conn.Name == virtual && wants[j].conn.Name != virtual
	})

	keep := map[uint32]bool{}
	used := map[uint32]bool{}
	for _, w := range wants {
		if o := cs.outs[w.conn.ID]; o != nil && o.mode == w.mode && crtcPossible(cs.dev, res, w.conn, o.crtc) {
			keep[w.conn.ID] = true
			used[o.crtc] = true
		}
	}
	// Tear down outputs that went away or need a new mode.
	for id, o := range cs.outs {
		if !keep[id] {
			cs.dev.SetCRTC(o.crtc, 0, nil, nil)
			if o.buf != nil {
				o.buf.Destroy()
			}
			delete(cs.outs, id)
		}
	}
	for _, w := range wants {
		if keep[w.conn.ID] {
			continue
		}
		crtc := pickCRTC(cs.dev, res, w.conn, used, w.conn.Name != virtual)
		if crtc == 0 {
			if !cs.noCRTC[w.conn.ID] {
				r.opts.logf("vos welcome: no free CRTC for %s", w.conn.Name)
				cs.noCRTC[w.conn.ID] = true
			}
			continue
		}
		delete(cs.noCRTC, w.conn.ID)
		o := &output{conn: w.conn.ID, name: w.conn.Name, crtc: crtc, mode: w.mode}
		if err := r.light(cs, o); err != nil {
			r.opts.logf("vos welcome: %s: %v", w.conn.Name, err)
			continue
		}
		used[crtc] = true
		cs.outs[w.conn.ID] = o
		r.opts.logf("vos welcome: %s on %s at %s", w.conn.Name, cs.name, w.mode.String())
	}
}

// maxWelcomeArea caps the mode of a physical connector at 4K. An 8K
// preferred mode would need a 132 MB dumb buffer and four times the render
// work for a still that looks the same.
const maxWelcomeArea = 3840 * 2160

// welcomeMode is the mode a physical connector shows the welcome screen in:
// its preferred mode, unless that is larger than 4K. Then it is the largest
// progressive mode at or under 4K with the same aspect ratio (within 1%),
// at the refresh rate closest to the preferred one (the higher on a tie);
// the preferred mode if there is none.
func welcomeMode(conn *drm.Connector) (drm.ModeInfo, bool) {
	pref, ok := conn.PreferredMode()
	if !ok || modeArea(&pref) <= maxWelcomeArea {
		return pref, ok
	}
	aspect := float64(pref.HDisplay) / float64(pref.VDisplay)
	var best *drm.ModeInfo
	for i := range conn.Modes {
		m := &conn.Modes[i]
		a := modeArea(m)
		if a == 0 || a > maxWelcomeArea || m.Flags&(1<<4) != 0 { // DRM_MODE_FLAG_INTERLACE
			continue
		}
		if r := float64(m.HDisplay) / float64(m.VDisplay) / aspect; r < 0.99 || r > 1.01 {
			continue
		}
		if best == nil || a > modeArea(best) || a == modeArea(best) && closerRefresh(m, best, pref.Refresh()) {
			best = m
		}
	}
	if best == nil {
		return pref, true
	}
	return *best, true
}

func modeArea(m *drm.ModeInfo) int { return int(m.HDisplay) * int(m.VDisplay) }

// closerRefresh reports whether a's refresh is nearer to want than b's, or
// as near and higher.
func closerRefresh(a, b *drm.ModeInfo, want int) bool {
	da, db := abs(a.Refresh()-want), abs(b.Refresh()-want)
	return da < db || da == db && a.Refresh() > b.Refresh()
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// light allocates a framebuffer for o, draws the current state and sets the CRTC.
func (r *runner) light(cs *cardState, o *output) error {
	buf, err := cs.dev.NewBuffer(int(o.mode.HDisplay), int(o.mode.VDisplay))
	if err != nil {
		return err
	}
	w, h := buf.Dims()
	copyXRGB(buf.Mem(), buf.RowPitch(), w, h, Render(r.state, w, h))
	mode := o.mode
	if err := cs.dev.SetCRTC(o.crtc, buf.FBID(), []uint32{o.conn}, &mode); err != nil {
		buf.Destroy()
		return err
	}
	o.buf = buf
	return nil
}

// redraw paints the current state into every lit framebuffer, rendering
// each distinct size once, then flushes each one to the screen.
func (r *runner) redraw() {
	cache := map[image.Point]*image.RGBA{}
	for _, cs := range r.cards {
		for _, o := range cs.outs {
			if o.buf == nil {
				continue
			}
			w, h := o.buf.Dims()
			img := cache[image.Pt(w, h)]
			if img == nil {
				img = Render(r.state, w, h)
				cache[image.Pt(w, h)] = img
			}
			copyXRGB(o.buf.Mem(), o.buf.RowPitch(), w, h, img)
			r.flush(cs, o)
		}
	}
}

// flush makes CPU writes to o's buffer visible. Drivers that scan out the
// dumb buffer itself need nothing, but shadow-plane drivers (bochs and
// virtio-gpu in a VM, and many simple KMS drivers) only copy damaged areas,
// so mark the whole buffer dirty; if the driver has no dirty hook either,
// set the CRTC to the same buffer again, which is a full update everywhere.
func (r *runner) flush(cs *cardState, o *output) {
	if err := cs.dev.DirtyFB(o.buf.FBID()); err == nil {
		return
	}
	mode := o.mode
	if err := cs.dev.SetCRTC(o.crtc, o.buf.FBID(), []uint32{o.conn}, &mode); err != nil {
		r.opts.logf("vos welcome: refresh crtc %d: %v", o.crtc, err)
	}
}

// possibleCRTCs is the union of the CRTC masks of conn's encoders.
func possibleCRTCs(dev kmsDevice, conn *drm.Connector) uint32 {
	var mask uint32
	for _, eid := range conn.Encoders {
		if e, err := dev.Encoder(eid); err == nil {
			mask |= e.PossibleCRTCs
		}
	}
	return mask
}

// crtcPossible reports whether crtc can drive conn.
func crtcPossible(dev kmsDevice, res *drm.Resources, conn *drm.Connector, crtc uint32) bool {
	mask := possibleCRTCs(dev, conn)
	for i, id := range res.CRTCs {
		if id == crtc {
			return mask&(1<<i) != 0
		}
	}
	return false
}

// pickCRTC returns a free CRTC for conn: with keepCurrent the one already
// driving it (no need to move it), else the lowest-numbered one any of its
// encoders supports; 0 if none is free.
func pickCRTC(dev kmsDevice, res *drm.Resources, conn *drm.Connector, used map[uint32]bool, keepCurrent bool) uint32 {
	if keepCurrent && conn.EncoderID != 0 {
		if e, err := dev.Encoder(conn.EncoderID); err == nil && e.CRTCID != 0 && !used[e.CRTCID] {
			return e.CRTCID
		}
	}
	mask := possibleCRTCs(dev, conn)
	for i, id := range res.CRTCs {
		if mask&(1<<i) != 0 && !used[id] {
			return id
		}
	}
	return 0
}

// release blanks and frees everything on the card, then gives up master.
func (cs *cardState) release() {
	for id, o := range cs.outs {
		if o.buf != nil {
			clear(o.buf.Mem())
			// Removing a framebuffer that is being scanned out switches its
			// CRTC off: the monitor goes dark rather than showing a console.
			o.buf.Destroy()
		}
		delete(cs.outs, id)
	}
	if cs.master {
		cs.dev.DropMaster()
		cs.master = false
	}
	cs.dev.Close()
}

func (r *runner) shutdown() {
	for name, cs := range r.cards {
		cs.release()
		delete(r.cards, name)
	}
}
