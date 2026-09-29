package brand

import (
	"strconv"
	"strings"
)

// Mode is a display mode: width × height at a refresh rate.
type Mode struct {
	W, H int
	Hz   float64
}

// ParseMode parses the API's "WxH@R" mode ("2560x1440@120",
// "1920x1080@59.94"), ignoring surrounding space. It accepts exactly what
// the control center's fmt.js parseMode accepts: 2 to 5 digits per side and
// a rate of 1 to 3 digits with an optional fraction.
func ParseMode(s string) (Mode, bool) {
	s = strings.TrimSpace(s)
	wh, rate, ok := strings.Cut(s, "@")
	if !ok {
		return Mode{}, false
	}
	ws, hs, ok := strings.Cut(wh, "x")
	if !ok || !digits(ws, 2, 5) || !digits(hs, 2, 5) {
		return Mode{}, false
	}
	whole, frac, dotted := strings.Cut(rate, ".")
	if !digits(whole, 1, 3) || dotted && !digits(frac, 1, 64) {
		return Mode{}, false
	}
	w, _ := strconv.Atoi(ws)
	h, _ := strconv.Atoi(hs)
	hz, err := strconv.ParseFloat(rate, 64)
	if err != nil {
		return Mode{}, false
	}
	return Mode{W: w, H: h, Hz: hz}, true
}

func digits(s string, lo, hi int) bool {
	if len(s) < lo || len(s) > hi {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Label is the readout the screen shape shows: "2560 × 1440 · 120 Hz".
func (m Mode) Label() string {
	return strconv.Itoa(m.W) + " × " + strconv.Itoa(m.H) + " · " + strconv.FormatFloat(m.Hz, 'f', -1, 64) + " Hz"
}

// ModeLabel formats an API mode for people, adding " · HDR" when hdr is set:
// "3840x2160@120" with HDR reads "3840 × 2160 · 120 Hz · HDR". A string that
// is not a mode comes back unchanged, without the HDR suffix.
func ModeLabel(mode string, hdr bool) string {
	m, ok := ParseMode(mode)
	if !ok {
		return mode
	}
	if hdr {
		return m.Label() + " · HDR"
	}
	return m.Label()
}

// Screen-shape aspect limits: the shape keeps w:h inside its box, clamped so
// a 32:9 or portrait mode still reads as a screen.
const (
	ShapeAspectMin = 0.4
	ShapeAspectMax = 3.6
)

// ShapeAspect is the w:h the screen shape draws: clamped to
// [ShapeAspectMin, ShapeAspectMax], and 16:9 when the size is unknown.
func ShapeAspect(w, h int) float64 {
	if w <= 0 || h <= 0 {
		return 16.0 / 9
	}
	return min(max(float64(w)/float64(h), ShapeAspectMin), ShapeAspectMax)
}
