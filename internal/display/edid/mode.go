// Package edid generates and decodes the EDID of VaporOS's virtual display.
//
// The kernel forces a spare connector on (`video=DP-1:e`) and loads this
// EDID for it (`drm.edid_firmware=DP-1:edid/vaporos.bin`). On such a forced
// connector amdgpu offers exactly the EDID's modes, and gamescope can only
// switch to a mode the connector offers, so this file decides which client
// resolutions can be streamed natively. Every timing is CVT reduced
// blanking v2 (the leanest standard blanking, so high modes fit the link),
// spread over the base block, a CTA-861 extension (which also carries the
// HDR static metadata and BT.2020 colorimetry) and as many DisplayID
// extensions as needed.
package edid

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Mode is a video mode as clients and gamescope name it: size and an
// integer refresh rate in Hz.
type Mode struct {
	W, H, Refresh int
}

// String formats "WxH@R", the form used in config.json, the API and
// gamescope's modes.cfg.
func (m Mode) String() string { return fmt.Sprintf("%dx%d@%d", m.W, m.H, m.Refresh) }

// Size formats "WxH".
func (m Mode) Size() string { return fmt.Sprintf("%dx%d", m.W, m.H) }

// Area is W*H.
func (m Mode) Area() int { return m.W * m.H }

// ParseMode parses "WxH@R" (also accepting "WxH" as 60 Hz and "@R.xx"
// rounded to the nearest Hz).
func ParseMode(s string) (Mode, error) {
	s = strings.TrimSpace(s)
	size, rate, hasRate := strings.Cut(s, "@")
	ws, hs, ok := strings.Cut(strings.ToLower(size), "x")
	if !ok {
		return Mode{}, fmt.Errorf("mode %q: want WxH@R", s)
	}
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return Mode{}, fmt.Errorf("mode %q: bad size", s)
	}
	r := 60
	if hasRate {
		f, err := strconv.ParseFloat(rate, 64)
		if err != nil || f <= 0 || f > 1000 {
			return Mode{}, fmt.Errorf("mode %q: bad refresh rate", s)
		}
		r = int(math.Round(f))
	}
	return Mode{W: w, H: h, Refresh: r}, nil
}

// SameAspect reports whether two sizes have the same aspect ratio within 1%,
// so near-identical phone panels (2796x1290 vs 2556x1179) count as one shape.
func SameAspect(a, b Mode) bool {
	if a.H == 0 || b.H == 0 {
		return false
	}
	ra := float64(a.W) / float64(a.H)
	rb := float64(b.W) / float64(b.H)
	return math.Abs(ra-rb)/rb < 0.01
}

// Preferred is the EDID's preferred mode: what the virtual display shows
// before any client asked for something else.
var Preferred = Mode{1920, 1080, 60}

// Catalogue is the built-in list of modes every VaporOS EDID carries: the
// common desktop, laptop, handheld, ultrawide, phone and tablet panels that
// Moonlight clients ask for. It is in priority order: the first entries get
// the classic detailed-timing slots of the base and CTA blocks, which every
// EDID parser understands; the rest follow in DisplayID blocks.
var Catalogue = []Mode{
	{1920, 1080, 60}, // preferred
	{3840, 2160, 60},
	{2560, 1440, 60},
	{1920, 1080, 120},
	{2560, 1600, 60},
	{1280, 800, 60},
	{1920, 1200, 60},
	{1280, 720, 60},
	{2560, 1440, 120},
	{2560, 1600, 120},
	{1920, 1080, 144},
	{1920, 1200, 120},
	{1280, 800, 90},
	{1280, 720, 120},
	{3440, 1440, 60}, {3440, 1440, 100},
	{3840, 2160, 30},
	{2796, 1290, 60}, {2796, 1290, 120},
	{2556, 1179, 60}, {2556, 1179, 120},
	{2732, 2048, 60},
}

// MaxExtra caps learned and configured modes on top of the catalogue, which
// keeps the EDID to a handful of extension blocks.
const MaxExtra = 30

// Limits advertised in the range-limits descriptor and enforced on every
// generated timing.
const (
	MinVRate     = 24
	MaxVRate     = 240
	MinHRateKHz  = 15
	MaxHRateKHz  = 400
	MaxClockKHz  = 600000 // what we generate; the descriptor says 700 MHz
	RangeClockMH = 700
)

// Check reports why a mode cannot go into the EDID, or nil.
func Check(m Mode) error {
	switch {
	case m.W == 4096 && m.H == 2160:
		// gamescope blocklists this size (it hides DCI 4K certification modes).
		return fmt.Errorf("%s: 4096x2160 is never offered", m)
	case m.W < 320 || m.H < 200:
		return fmt.Errorf("%s: too small", m)
	case m.W > 8192 || m.H > 8192:
		return fmt.Errorf("%s: too large", m)
	case m.Refresh < MinVRate || m.Refresh > MaxVRate:
		return fmt.Errorf("%s: refresh outside %d-%d Hz", m, MinVRate, MaxVRate)
	}
	t := CVTRB2(m)
	if t.ClockKHz > MaxClockKHz {
		return fmt.Errorf("%s: needs a %.1f MHz pixel clock (max %d MHz)", m, float64(t.ClockKHz)/1000, MaxClockKHz/1000)
	}
	if h := t.LineRateKHz(); h < MinHRateKHz || h > MaxHRateKHz {
		return fmt.Errorf("%s: line rate %.1f kHz outside %d-%d kHz", m, h, MinHRateKHz, MaxHRateKHz)
	}
	return nil
}
