package edid

// Timing is a complete detailed timing: active area, blanking split into
// front porch / sync / back porch, pixel clock and sync polarities.
type Timing struct {
	Mode                             // nominal size and refresh
	ClockKHz                     int // pixel clock
	HFront, HSync, HBack         int // horizontal blanking, pixels
	VFront, VSync, VBack         int // vertical blanking, lines
	HSyncPositive, VSyncPositive bool
}

func (t Timing) HBlank() int { return t.HFront + t.HSync + t.HBack }
func (t Timing) VBlank() int { return t.VFront + t.VSync + t.VBack }
func (t Timing) HTotal() int { return t.W + t.HBlank() }
func (t Timing) VTotal() int { return t.H + t.VBlank() }

// ExactRefresh is the refresh rate the timing really produces.
func (t Timing) ExactRefresh() float64 {
	return float64(t.ClockKHz) * 1000 / float64(t.HTotal()*t.VTotal())
}

// LineRateKHz is the horizontal frequency.
func (t Timing) LineRateKHz() float64 { return float64(t.ClockKHz) / float64(t.HTotal()) }

// VESA CVT 2.0, reduced blanking version 2 (identical to CVT 1.2 RB2):
// a fixed 80-pixel horizontal blank (8 front porch, 32 sync, 40 back porch),
// an 8-line vsync, a fixed 6-line back porch and a vertical front porch that
// grows until the vertical blank lasts at least 460 µs. The pixel clock is
// rounded down to a 1 kHz step.
const (
	rb2HBlank     = 80
	rb2HSync      = 32
	rb2HFront     = 8
	rb2VSync      = 8
	rb2VBack      = 6
	rb2MinVFront  = 1
	rb2MinVBlankU = 460 // µs
)

// CVTRB2 computes the CVT-RB2 timing for m (progressive, no margins, no
// 1000/1001 video-optimised rate).
func CVTRB2(m Mode) Timing {
	// H_PERIOD_EST = (1e6/V_FIELD_RATE - RB_MIN_V_BLANK) / V_LINES   [µs]
	// VBI_LINES    = floor(RB_MIN_V_BLANK / H_PERIOD_EST) + 1
	// which in exact integer arithmetic is
	// floor(460 * V_LINES * RATE / (1e6 - 460 * RATE)) + 1.
	// A refresh so high that 460 µs exceeds the frame (> 2173 Hz) has no
	// valid timing; clamp so the result is merely absurd and Check rejects it.
	den := max(1_000_000-rb2MinVBlankU*m.Refresh, 1)
	vbi := rb2MinVBlankU*m.H*m.Refresh/den + 1
	vbi = max(vbi, rb2MinVFront+rb2VSync+rb2VBack)

	htotal := m.W + rb2HBlank
	vtotal := m.H + vbi
	// ACT_PIXEL_FREQ = CLOCK_STEP * floor(V_FIELD_RATE * V_TOTAL * H_TOTAL / 1e6 / CLOCK_STEP)
	// with CLOCK_STEP = 0.001 MHz, i.e. floor to whole kHz.
	clockHz := int64(m.Refresh) * int64(vtotal) * int64(htotal)
	return Timing{
		Mode:          m,
		ClockKHz:      int(clockHz / 1000),
		HFront:        rb2HFront,
		HSync:         rb2HSync,
		HBack:         rb2HBlank - rb2HFront - rb2HSync,
		VFront:        vbi - rb2VSync - rb2VBack,
		VSync:         rb2VSync,
		VBack:         rb2VBack,
		HSyncPositive: true,
		VSyncPositive: false,
	}
}
