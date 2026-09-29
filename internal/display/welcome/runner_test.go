package welcome

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/display/drm"
)

// fakeKMS is a tiny KMS device: CRTCs, connectors with one encoder each,
// dumb buffers in plain memory, and a log of SETCRTC calls.
type fakeKMS struct {
	crtcs     []uint32
	conns     map[uint32]*drm.Connector
	encoders  map[uint32]*drm.Encoder
	masterErr error
	master    bool
	closed    bool
	nextFB    uint32
	live      map[uint32]*fakeBuf // fb id → buffer
	setcrtc   []string
	scanout   map[uint32]uint32 // crtc → fb
	dirty     []uint32          // fbs flushed with DirtyFB
}

type fakeBuf struct {
	k         *fakeKMS
	id        uint32
	w, h      int
	pitch     int
	mem       []byte
	destroyed bool
}

func (b *fakeBuf) FBID() uint32     { return b.id }
func (b *fakeBuf) Dims() (int, int) { return b.w, b.h }
func (b *fakeBuf) RowPitch() int    { return b.pitch }
func (b *fakeBuf) Mem() []byte      { return b.mem }
func (b *fakeBuf) Destroy() {
	b.destroyed = true
	delete(b.k.live, b.id)
	for c, fb := range b.k.scanout {
		if fb == b.id {
			delete(b.k.scanout, c) // RMFB switches the CRTC off
		}
	}
}

func mode(w, h, r int, preferred bool) drm.ModeInfo {
	m := drm.ModeInfo{HDisplay: uint16(w), VDisplay: uint16(h), HTotal: uint16(w + 80), VTotal: uint16(h + 40)}
	m.Clock = uint32(r * int(m.HTotal) * int(m.VTotal) / 1000)
	if preferred {
		m.Type = drm.ModeTypePreferred
	}
	return m
}

func newFakeKMS() *fakeKMS {
	k := &fakeKMS{
		crtcs:    []uint32{10, 11, 12},
		conns:    map[uint32]*drm.Connector{},
		encoders: map[uint32]*drm.Encoder{},
		nextFB:   100,
		live:     map[uint32]*fakeBuf{},
		scanout:  map[uint32]uint32{},
	}
	add := func(id uint32, name string, connected bool, crtc uint32, modes ...drm.ModeInfo) {
		enc := id + 10
		k.encoders[enc] = &drm.Encoder{ID: enc, CRTCID: crtc, PossibleCRTCs: 0b111}
		c := &drm.Connector{ID: id, Name: name, Encoders: []uint32{enc}, Modes: modes, Connection: drm.Disconnected}
		if connected {
			c.Connection = drm.Connected
			c.EncoderID = enc
		}
		k.conns[id] = c
	}
	// fbcon left the monitor on CRTC 10 and the virtual display on 11.
	add(20, "DP-1", true, 11, mode(3840, 2160, 60, false), mode(1920, 1080, 60, true), mode(1920, 1080, 120, false))
	add(21, "HDMI-A-1", true, 10, mode(3840, 2160, 60, true), mode(1920, 1080, 60, false))
	add(22, "DP-2", false, 0)
	return k
}

func (k *fakeKMS) SetMaster() error {
	if k.masterErr != nil {
		return k.masterErr
	}
	k.master = true
	return nil
}
func (k *fakeKMS) DropMaster() error { k.master = false; return nil }
func (k *fakeKMS) DirtyFB(fb uint32) error {
	k.dirty = append(k.dirty, fb)
	return nil
}
func (k *fakeKMS) Close() error { k.closed = true; return nil }
func (k *fakeKMS) Resources() (*drm.Resources, error) {
	var ids []uint32
	for id := range k.conns {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return &drm.Resources{CRTCs: k.crtcs, Connectors: ids}, nil
}
func (k *fakeKMS) Connector(id uint32, probe bool) (*drm.Connector, error) {
	c := *k.conns[id]
	return &c, nil
}
func (k *fakeKMS) Encoder(id uint32) (*drm.Encoder, error) {
	if e, ok := k.encoders[id]; ok {
		e2 := *e
		return &e2, nil
	}
	return nil, errors.New("no encoder")
}
func (k *fakeKMS) SetCRTC(crtc, fb uint32, conns []uint32, m *drm.ModeInfo) error {
	if !k.master {
		return errors.New("EACCES: not master")
	}
	if fb == 0 {
		k.setcrtc = append(k.setcrtc, fmt.Sprintf("off %d", crtc))
		delete(k.scanout, crtc)
		return nil
	}
	k.setcrtc = append(k.setcrtc, fmt.Sprintf("%d:%s@%s", crtc, k.conns[conns[0]].Name, m.String()))
	k.scanout[crtc] = fb
	for _, id := range conns { // the kernel rebinds the connector
		k.encoders[k.conns[id].Encoders[0]].CRTCID = crtc
	}
	return nil
}
func (k *fakeKMS) NewBuffer(w, h int) (scanoutBuffer, error) {
	k.nextFB++
	b := &fakeBuf{k: k, id: k.nextFB, w: w, h: h, pitch: w*4 + 64}
	b.mem = make([]byte, b.pitch*h)
	k.live[b.id] = b
	return b, nil
}

func newTestRunner(k *fakeKMS, cards *[]drm.SysCard) (*runner, *[]string) {
	var logs []string
	opts := &Options{
		VirtualConnector: func() string { return "DP-1" },
		Logf:             func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
	}
	r := &runner{
		opts:    opts,
		open:    func(dev string) (kmsDevice, error) { return k, nil },
		sysCard: func() ([]drm.SysCard, error) { return *cards, nil },
		sysConn: func(card string) ([]drm.SysConnector, error) {
			var out []drm.SysConnector
			for _, c := range *cards {
				if card == "" || card == c.Name {
					for _, kc := range k.conns {
						status := "disconnected"
						if kc.Connection == drm.Connected {
							status = "connected"
						}
						out = append(out, drm.SysConnector{Card: c.Name, Name: kc.Name, Status: status})
					}
				}
			}
			return out, nil
		},
		cards: map[string]*cardState{},
		state: State{Status: "Ready to stream", URL: "http://vapor.local", QR: "http://192.168.1.50/"},
	}
	return r, &logs
}

func nonZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return true
		}
	}
	return false
}

func TestRunnerLightsConnectors(t *testing.T) {
	k := newFakeKMS()
	cards := []drm.SysCard{{Name: "card1", Dev: "/dev/dri/card1"}}
	r, logs := newTestRunner(k, &cards)
	r.scan(false)

	// Virtual display first, on the lowest CRTC, at 1080p60; the monitor
	// at its preferred mode on the next free CRTC.
	want := []string{"10:DP-1@1920x1080@60", "11:HDMI-A-1@3840x2160@60"}
	if !slices.Equal(k.setcrtc, want) {
		t.Fatalf("setcrtc = %v, want %v (logs %v)", k.setcrtc, want, *logs)
	}
	if len(k.live) != 2 || len(k.scanout) != 2 {
		t.Fatalf("buffers %d, scanouts %d", len(k.live), len(k.scanout))
	}
	for _, b := range k.live {
		if !nonZero(b.mem) {
			t.Errorf("buffer %dx%d never drawn", b.w, b.h)
		}
		// Pitch padding stays untouched.
		if nonZero(b.mem[b.w*4 : b.pitch]) {
			t.Error("drew into pitch padding")
		}
	}

	// Nothing changed: no modeset.
	r.scan(false)
	if len(k.setcrtc) != 2 {
		t.Errorf("rescan modeset again: %v", k.setcrtc)
	}

	// New state: buffers are redrawn in place.
	var before []byte
	for _, b := range k.live {
		if b.w == 1920 {
			before = slices.Clone(b.mem)
		}
	}
	r.state.Status = "A device wants to pair"
	r.redraw()
	for _, b := range k.live {
		if b.w == 1920 && slices.Equal(before, b.mem) {
			t.Error("redraw did not change the picture")
		}
	}
	// Every redrawn buffer is flushed, or shadow-plane drivers (bochs,
	// virtio-gpu) never show the new picture.
	if len(k.dirty) != len(k.live) {
		t.Errorf("redraw flushed %v, want all %d buffers", k.dirty, len(k.live))
	}

	// Monitor unplugged: its CRTC goes off and its buffer is freed.
	k.conns[21].Connection = drm.Disconnected
	r.scan(true)
	if last := k.setcrtc[len(k.setcrtc)-1]; last != "off 11" || len(k.live) != 1 {
		t.Errorf("after unplug: %v, %d live buffers", k.setcrtc, len(k.live))
	}
	// Plugged back in: lit again on a free CRTC.
	k.conns[21].Connection = drm.Connected
	r.scan(true)
	if last := k.setcrtc[len(k.setcrtc)-1]; !strings.HasSuffix(last, ":HDMI-A-1@3840x2160@60") {
		t.Errorf("after replug: %v", k.setcrtc)
	}

	// SIGTERM: everything freed, master dropped, card closed.
	r.shutdown()
	if len(k.live) != 0 || len(k.scanout) != 0 || k.master || !k.closed {
		t.Errorf("shutdown left live=%d scanout=%v master=%v closed=%v", len(k.live), k.scanout, k.master, k.closed)
	}
}

func TestRunnerWaitsForMaster(t *testing.T) {
	k := newFakeKMS()
	k.masterErr = errors.New("EBUSY")
	cards := []drm.SysCard{{Name: "card1", Dev: "/dev/dri/card1"}}
	r, logs := newTestRunner(k, &cards)
	r.scan(false)
	if len(k.setcrtc) != 0 || !strings.Contains(strings.Join(*logs, "\n"), "waiting for DRM master") {
		t.Fatalf("lit without master: %v %v", k.setcrtc, *logs)
	}
	k.masterErr = nil
	r.scan(false)
	if len(k.setcrtc) != 2 {
		t.Fatalf("not lit once master was free: %v", k.setcrtc)
	}
	// The card vanishes (driver unbound): released.
	cards = nil
	r.scan(false)
	if len(r.cards) != 0 || !k.closed || len(k.live) != 0 {
		t.Error("vanished card not released")
	}
}

func TestRunnerCRTCShortage(t *testing.T) {
	k := newFakeKMS()
	k.crtcs = []uint32{10}
	cards := []drm.SysCard{{Name: "card1", Dev: "/dev/dri/card1"}}
	r, logs := newTestRunner(k, &cards)
	r.scan(false)
	// The virtual display wins the only CRTC: Sunshine needs it.
	if !slices.Equal(k.setcrtc, []string{"10:DP-1@1920x1080@60"}) || !strings.Contains(strings.Join(*logs, "\n"), "no free CRTC for HDMI-A-1") {
		t.Errorf("setcrtc = %v logs = %v", k.setcrtc, *logs)
	}
	// Reported once, not on every rescan.
	n := len(*logs)
	r.scan(false)
	r.scan(false)
	if len(*logs) != n {
		t.Errorf("repeated logs: %v", (*logs)[n:])
	}
}

func TestRunnerNoVirtualConfigured(t *testing.T) {
	k := newFakeKMS()
	cards := []drm.SysCard{{Name: "card1", Dev: "/dev/dri/card1"}}
	r, _ := newTestRunner(k, &cards)
	r.opts.VirtualConnector = nil
	r.scan(false)
	// Without a virtual connector every connected output shows its preferred
	// mode, and monitors keep the CRTC they already had.
	if !slices.Contains(k.setcrtc, "10:HDMI-A-1@3840x2160@60") || !slices.Contains(k.setcrtc, "11:DP-1@1920x1080@60") {
		t.Errorf("setcrtc = %v", k.setcrtc)
	}
}

// An 8K monitor would need a 132 MB buffer for a still: it is lit at the
// largest 16:9 mode at or under 4K, at the refresh closest to its preferred
// one. The virtual display keeps its 1080p splash.
func TestRunnerCapsHugeModes(t *testing.T) {
	k := newFakeKMS()
	interlaced := mode(3840, 2160, 60, false)
	interlaced.Flags |= 1 << 4
	k.conns[21].Modes = []drm.ModeInfo{
		mode(7680, 4320, 60, true),
		mode(5120, 2880, 60, false), // larger than 4K
		mode(4096, 2160, 60, false), // DCI: another aspect ratio
		interlaced,
		mode(3840, 2160, 30, false),
		mode(3840, 2160, 120, false),
		mode(3840, 2160, 60, false),
		mode(2560, 1440, 60, false),
	}
	cards := []drm.SysCard{{Name: "card1", Dev: "/dev/dri/card1"}}
	r, _ := newTestRunner(k, &cards)
	r.scan(false)
	if !slices.Contains(k.setcrtc, "11:HDMI-A-1@3840x2160@60") || !slices.Contains(k.setcrtc, "10:DP-1@1920x1080@60") {
		t.Errorf("setcrtc = %v", k.setcrtc)
	}
	for _, b := range k.live {
		if w, h := b.Dims(); w*h > maxWelcomeArea {
			t.Errorf("buffer %dx%d is larger than 4K", w, h)
		}
	}

	for _, c := range []struct {
		modes []drm.ModeInfo
		want  string
	}{
		{[]drm.ModeInfo{mode(3840, 2160, 60, true), mode(1920, 1080, 60, false)}, "3840x2160@60"},   // 4K itself is kept
		{[]drm.ModeInfo{mode(7680, 4320, 60, true), mode(4096, 2160, 60, false)}, "7680x4320@60"},   // nothing fits: preferred
		{[]drm.ModeInfo{mode(5120, 2160, 60, true), mode(3440, 1440, 100, false)}, "3440x1440@100"}, // 21:9 within 1%
		{[]drm.ModeInfo{mode(7680, 4320, 60, true), mode(2560, 1440, 144, false), mode(1920, 1080, 60, false)}, "2560x1440@144"},
	} {
		got, ok := welcomeMode(&drm.Connector{Modes: c.modes})
		if !ok || got.String() != c.want {
			t.Errorf("welcomeMode(%v) = %s, want %s", c.modes[0].String(), got.String(), c.want)
		}
	}
	if _, ok := welcomeMode(&drm.Connector{}); ok {
		t.Error("a connector without modes has no mode")
	}
}
