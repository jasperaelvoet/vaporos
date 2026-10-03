package display

import (
	"fmt"
	"math"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

func scrMode(w, h, r int) edid.Mode { return edid.Mode{W: w, H: h, Refresh: r} }

// The spec's examples (docs/CONTRACTS.md, Display policy, Scaling, Scale).
func TestScaleForSpecExamples(t *testing.T) {
	for _, c := range []struct {
		what  string
		kind  Kind
		mode  edid.Mode
		scale float64
		dpi   int
	}{
		{"Steam Deck", KindHandheld, scrMode(1280, 800, 60), 1.50, 96},
		{"Pixel 2400x1080", KindPhone, scrMode(2400, 1080, 120), 2.25, 144},
		{"iPhone 15 Pro Max", KindPhone, scrMode(2796, 1290, 120), 2.70, 168},
		{"MacBook Pro 14", KindLaptop, scrMode(3024, 1964, 120), 1.95, 144},
		{"27-inch 1440p monitor", KindMonitor, scrMode(2560, 1440, 144), 1.20, 96},
		{"27-inch 4K monitor", KindMonitor, scrMode(3840, 2160, 60), 1.80, 144},
		{"4K TV", KindTV, scrMode(3840, 2160, 60), 2.55, 192},
		{"1080p TV", KindTV, scrMode(1920, 1080, 60), 1.30, 96},
		{"1080p unknown", KindUnknown, scrMode(1920, 1080, 60), 1.30, 96},
		{"portrait phone", KindPhone, scrMode(1290, 2796, 120), 2.70, 168},
	} {
		s := ScaleFor(c.kind, c.mode, 1, Panel{})
		if s != c.scale {
			t.Errorf("%s: scale = %v, want %v", c.what, s, c.scale)
		}
		if d := GameDPI(s, c.mode); d != c.dpi {
			t.Errorf("%s: dpi = %d, want %d", c.what, d, c.dpi)
		}
	}
}

func TestScaleForRoundsHalfUpAndClamps(t *testing.T) {
	for _, c := range []struct {
		what  string
		kind  Kind
		mode  edid.Mode
		size  float64
		panel Panel
		want  float64
	}{
		// 1230/1200 = 1.025 exactly: half up.
		{"tie at 0.05", KindMonitor, scrMode(1968, 1230, 60), 1, Panel{}, 1.05},
		{"just under the tie", KindMonitor, scrMode(1968, 1229, 60), 1, Panel{}, 1.00},
		{"tie at a hundredth", KindLaptop, scrMode(1720, 1075, 60), 1, Panel{}, 1.10},
		{"size", KindTV, scrMode(1920, 1080, 60), 1.2, Panel{}, 1.55},
		{"no size is 1.0", KindTV, scrMode(1920, 1080, 60), 0, Panel{}, 1.30},
		// 0.4, then Steam's 0.5·r = 0.71 and the 0.05 step.
		{"size below its range", KindTV, scrMode(1920, 1080, 60), 0.1, Panel{}, 0.70},
		// vosd's H/400 bounds only what it infers: the user's size goes on
		// to Steam's 2.3961·r = 3.81.
		{"top clamp", KindPhone, scrMode(2400, 1080, 60), 2.5, Panel{}, 3.80},
		{"no band on the size", KindPhone, scrMode(2400, 1080, 60), 1.4, Panel{}, 3.15},
		// Steam's 0.5·r = 0.47, then the 0.05 step.
		{"bottom clamp", KindMonitor, scrMode(1280, 720, 60), 0.4, Panel{}, 0.45},
		// Steam's 2.3961·r binds on a square mode, 0.5·r on 32:9.
		{"Steam's top bound", KindPhone, scrMode(1600, 1600, 60), 2.5, Panel{}, 3.80},
		{"Steam's bottom bound", KindMonitor, scrMode(5120, 1440, 60), 0.4, Panel{}, 1.35},
		// A phone letterboxing 32:9: L_eff = 480 × 2.167/3.556 = 292, so
		// H/L_eff = 4.92; vosd's band keeps that at H/400 = 3.60, and the
		// size goes on top of it.
		{"vosd's band on the base", KindPhone, scrMode(5120, 1440, 60), 1, Panel{430, 932}, 3.60},
		{"and the size on top", KindPhone, scrMode(5120, 1440, 60), 1.5, Panel{430, 932}, 5.40},
		// A 16:10 MacBook letterboxes 1080p: L_eff = 1000 × 1.540/1.778.
		{"letterbox", KindLaptop, scrMode(1920, 1080, 60), 1, Panel{1512, 982}, 1.25},
		{"portrait panel", KindLaptop, scrMode(1920, 1080, 60), 1, Panel{982, 1512}, 1.25},
		// A phone pillarboxes 16:9: its height is all used.
		{"pillarbox", KindPhone, scrMode(1280, 720, 60), 1, Panel{430, 932}, 1.50},
		{"no mode", KindTV, scrMode(0, 0, 0), 1, Panel{}, 0},
	} {
		if got := ScaleFor(c.kind, c.mode, c.size, c.panel); got != c.want {
			t.Errorf("%s: ScaleFor = %v, want %v", c.what, got, c.want)
		}
	}
}

func TestGameDPIFloorsExactly(t *testing.T) {
	for _, c := range []struct {
		scale float64
		mode  edid.Mode
		want  int
	}{
		{1.5, scrMode(1920, 1080, 60), 120},       // 1.5/1.2 is exactly 1.25
		{3.0, scrMode(3840, 2160, 60), 240},       // exactly 2.5
		{2.55, scrMode(3840, 2160, 60), 192},      // 2.125 floors to 2
		{1.95, scrMode(3024, 1964, 60), 144},      // 1.625 floors to 1.5
		{2.5592417, scrMode(3840, 2160, 60), 192}, // Steam's own automatic value
		{2.4, scrMode(1920, 1080, 60), 144},       // H/720 caps it
		{1.2, scrMode(1920, 1080, 60), 96},
		{0.8, scrMode(1920, 1080, 60), 96}, // never below 1
		{0, scrMode(1920, 1080, 60), 96},
		{2.7, scrMode(0, 0, 0), 96},
	} {
		if got := GameDPI(c.scale, c.mode); got != c.want {
			t.Errorf("GameDPI(%v, %v) = %d, want %d", c.scale, c.mode, got, c.want)
		}
	}
}

func TestAdoptSizeReproducesSteamsValue(t *testing.T) {
	for _, c := range []struct {
		kind  Kind
		mode  edid.Mode
		panel Panel
	}{
		{KindPhone, scrMode(2400, 1080, 60), Panel{}},
		{KindHandheld, scrMode(1280, 800, 60), Panel{}},
		{KindLaptop, scrMode(1920, 1080, 60), Panel{1512, 982}},
		{KindTV, scrMode(3840, 2160, 60), Panel{}},
		{KindUnknown, scrMode(1280, 720, 60), Panel{}},
	} {
		lo, hi := scale100(c.kind, c.mode.W, c.mode.H, SizeMin, c.panel), scale100(c.kind, c.mode.W, c.mode.H, SizeMax, c.panel)
		for v := lo; v <= hi; v += 5 {
			value := float64(v) / 100
			if got := ScaleFor(c.kind, c.mode, AdoptSize(value, c.kind, c.mode, c.panel), c.panel); got != value {
				t.Errorf("%s %v: adopting %v gives %v", c.kind, c.mode, value, got)
			}
		}
		// A value off the 0.05 grid comes back on it.
		if got := ScaleFor(c.kind, c.mode, AdoptSize(float64(lo+7)/100, c.kind, c.mode, c.panel), c.panel); got != float64(lo+5)/100 {
			t.Errorf("%s %v: adopting %v gives %v", c.kind, c.mode, float64(lo+7)/100, got)
		}
	}
	// A value beyond vosd's own band (H/400 = 2.70 here, H/1350 = 0.80 on
	// 1080p) comes back too: the band bounds only what vosd infers.
	for _, c := range []struct {
		kind  Kind
		mode  edid.Mode
		value float64
	}{
		{KindPhone, scrMode(2400, 1080, 60), 3.00},
		{KindPhone, scrMode(2400, 1080, 60), 3.80}, // Steam's own top, 2.3961·r
		{KindTV, scrMode(1920, 1080, 60), 0.75},
		{KindUnknown, scrMode(1920, 1080, 60), 2.85},
	} {
		if got := ScaleFor(c.kind, c.mode, AdoptSize(c.value, c.kind, c.mode, Panel{}), Panel{}); got != c.value {
			t.Errorf("%s %v: adopting %v gives %v", c.kind, c.mode, c.value, got)
		}
	}
	if got := AdoptSize(9, KindPhone, scrMode(2400, 1080, 60), Panel{}); got != SizeMax {
		t.Errorf("AdoptSize beyond the range = %v", got)
	}
	if got := AdoptSize(0.1, KindTV, scrMode(3840, 2160, 60), Panel{}); got != SizeMin {
		t.Errorf("AdoptSize below the range = %v", got)
	}
}

// TestSizeRange: beyond the range a screen's scale no longer moves, and a
// step of 10 % inside its edge still does.
func TestSizeRange(t *testing.T) {
	for _, c := range []struct {
		what   string
		kind   Kind
		mode   edid.Mode
		panel  Panel
		lo, hi float64 // 0: not checked
	}{
		// 2.25 at 1.0; Steam's 2.3961·r = 3.81 is 1.69.
		{"Pixel 2400x1080", KindPhone, scrMode(2400, 1080, 120), Panel{}, SizeMin, 381.0 / 225},
		// 1.20 at 1.0; Steam's 0.5·r = 0.95 is 0.79.
		{"27-inch 1440p monitor", KindMonitor, scrMode(2560, 1440, 144), Panel{}, 95.0 / 120, SizeMax},
		{"Steam Deck", KindHandheld, scrMode(1280, 800, 60), Panel{}, 0, 0},
		{"iPhone 15 Pro Max", KindPhone, scrMode(2796, 1290, 120), Panel{}, 0, 0},
		{"MacBook Pro 14", KindLaptop, scrMode(3024, 1964, 120), Panel{}, 0, 0},
		{"4K TV", KindTV, scrMode(3840, 2160, 60), Panel{}, 0, 0},
		{"1080p unknown", KindUnknown, scrMode(1920, 1080, 60), Panel{}, 0, 0},
		{"portrait phone", KindPhone, scrMode(1290, 2796, 120), Panel{}, 0, 0},
		{"letterbox", KindLaptop, scrMode(1920, 1080, 60), Panel{1512, 982}, 0, 0},
		{"vosd's band on the base", KindPhone, scrMode(5120, 1440, 60), Panel{430, 932}, 0, 0},
	} {
		lo, hi := SizeRange(c.kind, c.mode, c.panel)
		if lo < SizeMin || hi > SizeMax || lo > hi {
			t.Errorf("%s: range %v..%v", c.what, lo, hi)
			continue
		}
		if (c.lo != 0 && math.Abs(lo-c.lo) > 1e-9) || (c.hi != 0 && math.Abs(hi-c.hi) > 1e-9) {
			t.Errorf("%s: range %v..%v, want %v..%v", c.what, lo, hi, c.lo, c.hi)
		}
		s := func(size float64) int64 { return scale100(c.kind, c.mode.W, c.mode.H, size, c.panel) }
		top, bottom := s(SizeMax), s(SizeMin)
		for i := 40; i <= 250; i++ {
			size := float64(i) / 100
			if size >= hi && s(size) != top {
				t.Errorf("%s: size %v (above %v) gives %d, not the top %d", c.what, size, hi, s(size), top)
			}
			if size <= lo && s(size) != bottom {
				t.Errorf("%s: size %v (below %v) gives %d, not the bottom %d", c.what, size, lo, s(size), bottom)
			}
		}
		if hi < SizeMax && hi-0.1 >= SizeMin && s(hi-0.1) >= top {
			t.Errorf("%s: 10 %% below the top edge %v gives %d, the top already", c.what, hi, s(hi-0.1))
		}
		if lo > SizeMin && lo+0.1 <= SizeMax && s(lo+0.1) <= bottom {
			t.Errorf("%s: 10 %% above the bottom edge %v gives %d, the bottom still", c.what, lo, s(lo+0.1))
		}
	}
	if lo, hi := SizeRange(KindTV, scrMode(0, 0, 0), Panel{}); lo != 0 || hi != 0 {
		t.Errorf("no mode: %v..%v", lo, hi)
	}
}

// TestSteamAutoScale: Steam's own automatic scale is sqrt(W·H/1,266,000),
// to 0.01, on any mode, not Valve's 844 lines of it.
func TestSteamAutoScale(t *testing.T) {
	for _, c := range []struct {
		mode edid.Mode
		want float64
		dpi  int
	}{
		{scrMode(3840, 2160, 60), 2.56, 192},
		{scrMode(1920, 1080, 60), 1.28, 96},
		{scrMode(3120, 1440, 120), 1.88, 144}, // 1440/844 would be 1.70 and 120
		{scrMode(1440, 3120, 120), 1.88, 144},
		{scrMode(2560, 1600, 60), 1.80, 144},
		{scrMode(2796, 1290, 120), 1.69, 120},
		{scrMode(0, 0, 0), 0, 96},
	} {
		got := steamAutoScale(c.mode)
		if got != c.want || GameDPI(got, c.mode) != c.dpi {
			t.Errorf("steamAutoScale(%v) = %v (dpi %d), want %v (%d)", c.mode, got, GameDPI(got, c.mode), c.want, c.dpi)
		}
	}
}

func TestInferKindNames(t *testing.T) {
	for name, want := range map[string]Kind{
		"Steam Deck":        KindHandheld,
		"steamdeck-01":      KindHandheld,
		"ROG Ally X":        KindHandheld,
		"RC71L":             KindHandheld,
		"Legion Go S":       KindHandheld,
		"MSI Claw 8":        KindHandheld,
		"Nintendo Switch":   KindHandheld,
		"Xbox Ally":         KindHandheld,
		"Jasper's iPhone":   KindPhone,
		"Jasper’s iPhone":   KindPhone,
		"JasperiPhone":      KindPhone,
		"Justin-iPhone":     KindPhone,
		"Pixel 8 Pro":       KindPhone,
		"Pixel8":            KindPhone,
		"SM-S928B":          KindPhone,
		"Galaxy S24 Ultra":  KindPhone,
		"OnePlus 12":        KindPhone,
		"Pixel Tablet":      KindTablet,
		"Pixel Fold":        KindTablet,
		"Galaxy Z Fold5":    KindTablet,
		"Galaxy Tab S9":     KindTablet,
		"SM-X710":           KindTablet,
		"iPad Pro":          KindTablet,
		"MacBook Air":       KindLaptop,
		"Justin-MacbookPro": KindLaptop,
		"ThinkPad X1":       KindLaptop,
		"Framework 13":      KindLaptop,
		"LAPTOP-8H2K":       KindLaptop,
		"DESKTOP-4F3K2LQ":   KindMonitor,
		"Gaming PC":         KindMonitor,
		"GamingPC":          KindMonitor,
		"Living Room PC":    KindMonitor,
		"Mac mini":          KindMonitor,
		"iMac":              KindMonitor,
		"Living room TV":    KindTV,
		"SamsungTV":         KindTV,
		"SHIELD":            KindTV,
		"AFTMM":             KindTV,
		"Apple TV 4K":       KindTV,
		"Xbox Series X":     KindTV,
		"Bedroom":           KindTV,
		"PlayStation 5":     KindTV,
	} {
		if got, ok := nameKind(name); !ok || got != want {
			t.Errorf("nameKind(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"Clawson", "Bodine", "Folder", "Pixelbook", "tvbox", "Headphones", "Switcheroo", "MyAFT", "Jasper's Deck", "Mac", ""} {
		if got, ok := nameKind(name); ok {
			t.Errorf("nameKind(%q) = %q, want nothing", name, got)
		}
	}
}

func TestInferKindResolution(t *testing.T) {
	for _, c := range []struct {
		mode edid.Mode
		want Kind // "" for no signal
	}{
		{scrMode(1280, 800, 60), KindHandheld},
		{scrMode(800, 1280, 60), KindHandheld},
		{scrMode(2400, 1080, 60), KindPhone},
		{scrMode(2442, 1227, 60), KindPhone}, // an iPhone's Safe area
		{scrMode(1334, 750, 60), KindPhone},
		{scrMode(2208, 1242, 60), KindPhone},
		{scrMode(2560, 1080, 60), KindMonitor}, // ultrawide, not a phone
		{scrMode(3440, 1440, 100), KindMonitor},
		{scrMode(5120, 1440, 120), KindMonitor},
		{scrMode(2732, 2048, 120), KindTablet},
		{scrMode(2420, 1668, 120), KindTablet},
		{scrMode(2420, 1628, 120), KindTablet}, // Safe area
		{scrMode(2266, 1448, 60), KindTablet},
		{scrMode(3024, 1964, 120), KindLaptop},
		{scrMode(3456, 2160, 120), KindLaptop},
		{scrMode(2256, 1504, 60), KindLaptop},
		{scrMode(3840, 2160, 60), KindTV},
		{scrMode(2560, 1440, 144), KindMonitor},
		{scrMode(1920, 1200, 60), KindLaptop},
		{scrMode(2560, 1600, 60), KindLaptop},
		{scrMode(1920, 1200, 120), KindUnknown},
		{scrMode(2560, 1600, 144), KindUnknown},
		{scrMode(1920, 1080, 60), ""},
		{scrMode(1280, 720, 60), ""},
		{scrMode(1600, 900, 60), ""},
	} {
		got, ok := resolutionKind(c.mode)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("resolutionKind(%v) = %q, %v; want %q", c.mode, got, ok, c.want)
		}
	}
}

func TestInferKindOrder(t *testing.T) {
	laptopHint := &Hint{Kind: KindLaptop, W: 1512, H: 982, DPR: 2}
	for _, c := range []struct {
		what      string
		sig       Signals
		kind      Kind
		from      Source
		guess     Kind
		guessFrom Source
	}{
		{"you first", Signals{You: KindTablet, Name: "iPhone", Mode: scrMode(2796, 1290, 60)}, KindTablet, FromYou, KindPhone, FromName},
		{"a pick at pairing", Signals{Name: "roth", Hint: &Hint{Kind: KindTV, You: true}, Mode: scrMode(1280, 720, 60)}, KindTV, FromYou, KindUnknown, FromDefault},
		{"name before browser", Signals{Name: "Living room TV", Hint: laptopHint, Mode: scrMode(1920, 1080, 60)}, KindTV, FromName, KindTV, FromName},
		{"generic names say nothing", Signals{Name: " Roth ", Hint: laptopHint, Mode: scrMode(1920, 1080, 60)}, KindLaptop, FromBrowser, KindLaptop, FromBrowser},
		{"browser before resolution", Signals{Name: "Moonlight", Hint: laptopHint, Mode: scrMode(1280, 800, 60)}, KindLaptop, FromBrowser, KindLaptop, FromBrowser},
		{"an unknown browser says nothing", Signals{Hint: &Hint{Kind: KindUnknown}, Mode: scrMode(1280, 800, 60)}, KindHandheld, FromResolution, KindHandheld, FromResolution},
		{"resolution before history", Signals{Mode: scrMode(1280, 800, 60), History: []edid.Mode{scrMode(2796, 1290, 120)}}, KindHandheld, FromResolution, KindHandheld, FromResolution},
		{"history, newest first", Signals{Mode: scrMode(1280, 720, 60), History: []edid.Mode{scrMode(1920, 1080, 60), scrMode(2796, 1290, 120), scrMode(3840, 2160, 60)}}, KindPhone, FromHistory, KindPhone, FromHistory},
		{"144 fps", Signals{Mode: scrMode(1920, 1080, 144), Audio: "5.1"}, KindMonitor, FromStream, KindMonitor, FromStream},
		{"90 fps", Signals{Mode: scrMode(1920, 1080, 90)}, KindHandheld, FromStream, KindHandheld, FromStream},
		{"surround", Signals{Mode: scrMode(1920, 1080, 60), Audio: "7.1"}, KindTV, FromStream, KindTV, FromStream},
		{"an unknown resolution stops the stream rule", Signals{Mode: scrMode(2560, 1600, 144)}, KindUnknown, FromResolution, KindUnknown, FromResolution},
		{"default", Signals{Name: "roth", Mode: scrMode(1920, 1080, 60), Audio: "2.0"}, KindUnknown, FromDefault, KindUnknown, FromDefault},
	} {
		got := InferKind(c.sig)
		want := Inference{Kind: c.kind, From: c.from, Guess: c.guess, GuessFrom: c.guessFrom}
		if got != want {
			t.Errorf("%s: InferKind = %+v, want %+v", c.what, got, want)
		}
	}
}

func TestInferKindVeto(t *testing.T) {
	deckHint := &Hint{Kind: KindHandheld, W: 1280, H: 800, DPR: 1}
	s23 := &Hint{Kind: KindPhone, W: 384, H: 823, DPR: 2.8125, Touch: 5}
	for _, c := range []struct {
		what   string
		sig    Signals
		vetoed bool
	}{
		{"a docked Deck at 4K", Signals{Name: "Steam Deck", Mode: scrMode(3840, 2160, 60)}, true},
		{"a docked Deck at 1080p, by its hint", Signals{Hint: deckHint, Mode: scrMode(1920, 1080, 60)}, true},
		{"a Deck in hand", Signals{Hint: deckHint, Mode: scrMode(1280, 800, 60)}, false},
		{"a phone at its tallest", Signals{Name: "iPhone", Mode: scrMode(2560, 1440, 60)}, false},
		{"a phone on a 4K TV", Signals{Name: "iPhone", Mode: scrMode(3840, 2160, 60)}, true},
		{"the biggest iPad", Signals{Mode: scrMode(2752, 2064, 120)}, false},
		{"CSS pixels are rounded", Signals{Hint: &Hint{Kind: KindPhone, W: 411, H: 914, DPR: 2.625}, Mode: scrMode(2400, 1080, 60)}, false},
		{"a laptop is never vetoed", Signals{Name: "MacBook", Mode: scrMode(3840, 2160, 60)}, false},
		{"nor the user's pick", Signals{You: KindPhone, Mode: scrMode(3840, 2160, 60)}, false},
		{"nor a pick at pairing", Signals{Hint: &Hint{Kind: KindHandheld, You: true, W: 1280, H: 800, DPR: 1}, Mode: scrMode(3840, 2160, 60)}, false},
		// An S23 Ultra at its default FHD+ reports less than its WQHD+ panel,
		// which Moonlight offers natively: phone-shaped, no wider than the
		// hint, so its own panel.
		{"a phone's full panel above its browser's", Signals{Hint: s23, Mode: scrMode(3088, 1440, 120)}, false},
		{"the same phone on a 1440p screen", Signals{Hint: s23, Mode: scrMode(2560, 1440, 60)}, true},
		{"an iPhone on a 1440p screen", Signals{Hint: &Hint{Kind: KindPhone, W: 430, H: 932, DPR: 3}, Mode: scrMode(2560, 1440, 60)}, true},
		{"wider than the phone's panel", Signals{Hint: s23, Mode: scrMode(3440, 1440, 60)}, true},
		{"only for phones", Signals{Hint: &Hint{Kind: KindHandheld, W: 1280, H: 800, DPR: 1}, Mode: scrMode(3200, 1600, 60)}, true},
	} {
		got := InferKind(c.sig)
		if v := got.Kind == KindTV && got.From == FromStream; v != c.vetoed || got.Vetoed != c.vetoed {
			t.Errorf("%s: InferKind = %+v, vetoed %v", c.what, got, v)
		}
		if c.vetoed && (got.Guess == KindTV || got.GuessFrom == FromStream) {
			t.Errorf("%s: the guess holds the veto: %+v", c.what, got)
		}
	}
}

func TestPanelFor(t *testing.T) {
	hint := &Hint{Kind: KindLaptop, W: 1512, H: 982, DPR: 2}
	for _, c := range []struct {
		what    string
		hint    *Hint
		asked   edid.Mode
		history []edid.Mode
		want    Panel
	}{
		{"the hint first", hint, scrMode(3024, 1964, 60), nil, Panel{1512, 982}},
		{"a native mode asked", nil, scrMode(3024, 1964, 60), []edid.Mode{scrMode(2880, 1800, 60)}, Panel{3024, 1964}},
		{"a native mode before", nil, scrMode(1920, 1080, 60), []edid.Mode{scrMode(1920, 1080, 60), scrMode(2560, 1440, 60), scrMode(2880, 1800, 60)}, Panel{2880, 1800}},
		{"presets are no panel", nil, scrMode(1920, 1080, 60), []edid.Mode{scrMode(3840, 2160, 60), scrMode(1280, 720, 60)}, Panel{}},
		{"a hint without a screen", &Hint{Kind: KindTV, You: true}, scrMode(1920, 1080, 60), nil, Panel{}},
		// A docked Deck: its own panel is not where the stream is.
		{"a hint shorter than the stream", &Hint{Kind: KindHandheld, W: 1280, H: 800, DPR: 1}, scrMode(1920, 1080, 60), []edid.Mode{scrMode(1280, 800, 60)}, Panel{}},
		{"a native mode shorter than the stream", nil, scrMode(3840, 2160, 60), []edid.Mode{scrMode(3024, 1964, 60)}, Panel{}},
	} {
		if got := PanelFor(c.hint, c.asked, c.history); got != c.want {
			t.Errorf("%s: PanelFor = %v, want %v", c.what, got, c.want)
		}
	}
}

// review-ux-calibration's devices, end to end: what each gets.
func TestScaleDeviceTable(t *testing.T) {
	const (
		iPhoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
		macUA    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
		edgeUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0"
		deckUA   = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
		// Chrome on an Android tablet, asking for desktop sites.
		chromeLinuxUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"
		overlayUA     = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678"
		androidUA     = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36"
	)
	browser := func(ua string, w, h int, dpr float64, touch int) *Hint {
		return &Hint{Kind: ClassifyBrowser(ua, w, h, dpr, touch), W: w, H: h, DPR: dpr, Touch: touch}
	}
	for _, c := range []struct {
		what  string
		sig   Signals
		kind  Kind
		from  Source
		scale float64
		dpi   int
	}{
		{"iPhone at Moonlight's default", Signals{Name: "roth", Mode: scrMode(1280, 720, 60)}, KindUnknown, FromDefault, 0.85, 96},
		{"iPhone at its default, with its hint", Signals{Name: "roth", Hint: browser(iPhoneUA, 430, 932, 3, 5), Mode: scrMode(1280, 720, 60)}, KindPhone, FromBrowser, 1.50, 96},
		{"iPhone 15 Pro Max, Safe area", Signals{Name: "roth", Mode: scrMode(2442, 1227, 60)}, KindPhone, FromResolution, 2.55, 144},
		{"Shield at 1080p", Signals{Name: "roth", Mode: scrMode(1920, 1080, 60)}, KindUnknown, FromDefault, 1.30, 96},
		{"Shield at 1080p, 5.1", Signals{Name: "roth", Mode: scrMode(1920, 1080, 60), Audio: "5.1"}, KindTV, FromStream, 1.30, 96},
		{"Shield at 4K", Signals{Name: "roth", Mode: scrMode(3840, 2160, 60)}, KindTV, FromResolution, 2.55, 192},
		{"Deck docked to a 4K TV", Signals{Name: "Steam Deck", Mode: scrMode(3840, 2160, 60)}, KindTV, FromStream, 2.55, 192},
		{"Deck docked at 1080p, with its hint", Signals{Name: "roth", Hint: browser(deckUA, 1280, 800, 1, 10), Mode: scrMode(1920, 1080, 60)}, KindTV, FromStream, 1.30, 96},
		{"Deck in hand", Signals{Name: "steamdeck", Mode: scrMode(1280, 800, 60)}, KindHandheld, FromName, 1.50, 96},
		{"MacBook Pro 14 at 1080p, with its hint", Signals{Name: "roth", Hint: browser(macUA, 1512, 982, 2, 0), Mode: scrMode(1920, 1080, 60)}, KindLaptop, FromBrowser, 1.25, 96},
		{"MacBook Pro 14 native", Signals{Name: "roth", Mode: scrMode(3024, 1964, 120)}, KindLaptop, FromResolution, 1.95, 144},
		{"iPad Pro 11, Full screen", Signals{Name: "roth", Mode: scrMode(2420, 1668, 120)}, KindTablet, FromResolution, 1.90, 144},
		{"iPad Pro 11, Safe area", Signals{Name: "roth", Mode: scrMode(2420, 1628, 120)}, KindTablet, FromResolution, 1.85, 144},
		{"27-inch 1440p monitor", Signals{Name: "roth", Mode: scrMode(2560, 1440, 144)}, KindMonitor, FromResolution, 1.20, 96},
		{"ROG Ally at 1080p 120 Hz", Signals{Name: "roth", Mode: scrMode(1920, 1080, 120)}, KindUnknown, FromDefault, 1.30, 96},
		{"ROG Ally with its own Edge's hint", Signals{Name: "roth", Hint: browser(edgeUA, 1280, 720, 1.5, 10), Mode: scrMode(1920, 1080, 120)}, KindHandheld, FromBrowser, 2.05, 144},
		{"Legion Go at 2560x1600 144 Hz", Signals{Name: "roth", Mode: scrMode(2560, 1600, 144)}, KindUnknown, FromResolution, 1.90, 144},
		{"Legion Go, named", Signals{Name: "Legion Go", Mode: scrMode(2560, 1600, 144)}, KindHandheld, FromName, 3.00, 192},
		{"Galaxy S24 Ultra native", Signals{Name: "SM-S928B", Mode: scrMode(3120, 1440, 120)}, KindPhone, FromName, 3.00, 192},
		{"Pixel Tablet on desktop sites", Signals{Name: "roth", Hint: browser(chromeLinuxUA, 1280, 800, 2, 5), Mode: scrMode(2560, 1600, 60)}, KindTablet, FromBrowser, 1.80, 144},
		{"XPS 13 touch at 150 %, native", Signals{Name: "roth", Hint: browser(edgeUA, 1280, 800, 1.5, 10), Mode: scrMode(1920, 1200, 60)}, KindLaptop, FromResolution, 1.20, 96},
		{"XPS 13 touch, its panel seen before", Signals{Name: "roth", Hint: browser(edgeUA, 1280, 800, 1.5, 10), Mode: scrMode(1920, 1080, 60),
			History: []edid.Mode{scrMode(1920, 1200, 60)}}, KindLaptop, FromHistory, 1.20, 96}, // letterboxed on its 16:10 panel
		{"Steam's browser on a 1440p desktop", Signals{Name: "roth", Hint: browser(overlayUA, 2560, 1440, 1, 0), Mode: scrMode(2560, 1440, 144)}, KindMonitor, FromBrowser, 1.20, 96},
		{"S23 Ultra at FHD+, native WQHD+", Signals{Name: "roth", Hint: browser(androidUA, 384, 823, 2.8125, 5), Mode: scrMode(3088, 1440, 120)}, KindPhone, FromBrowser, 3.00, 192},
	} {
		inf := InferKind(c.sig)
		panel := PanelFor(c.sig.Hint, c.sig.Mode, c.sig.History)
		s := ScaleFor(inf.Kind, c.sig.Mode, 1, panel)
		dpi := GameDPI(s, c.sig.Mode)
		if inf.Kind != c.kind || inf.From != c.from || s != c.scale || dpi != c.dpi {
			t.Errorf("%s: %s/%s %v %d; want %s/%s %v %d", c.what, inf.Kind, inf.From, s, dpi, c.kind, c.from, c.scale, c.dpi)
		}
	}
}

func TestClassifyBrowser(t *testing.T) {
	for _, c := range []struct {
		ua    string
		w, h  int
		dpr   float64
		touch int
		want  Kind
	}{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", 430, 932, 3, 5, KindPhone},
		{"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", 820, 1180, 2, 5, KindTablet},
		// iPadOS asks for desktop sites as a Mac with touch.
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", 1194, 834, 2, 5, KindTablet},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", 1512, 982, 2, 0, KindLaptop},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", 2056, 1329, 2, 0, KindLaptop},  // More Space
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", 2560, 1440, 2, 0, KindMonitor}, // a 5K iMac
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", 1920, 1080, 1, 0, KindMonitor},
		{"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36", 412, 915, 2.625, 5, KindPhone},
		{"Mozilla/5.0 (Android 14; Mobile; rv:128.0) Gecko/128.0 Firefox/128.0", 412, 915, 2.625, 5, KindPhone},
		{"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 800, 1280, 2, 10, KindTablet},
		{"Mozilla/5.0 (Linux; Android 11; SHIELD Android TV Build/RQ1A.210105.003; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Safari/537.36", 960, 540, 2, 0, KindTV},
		{"Mozilla/5.0 (Linux; Android 9; AFTMM Build/PS7633.3445N; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Mobile Safari/537.36", 960, 540, 2, 0, KindTV},
		{"Mozilla/5.0 (Linux; Android 12; Chromecast Google TV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 960, 540, 2, 0, KindTV},
		{"Mozilla/5.0 (SMART-TV; LINUX; Tizen 6.0) AppleWebKit/537.36 (KHTML, like Gecko) 76.0.3809.146/6.0 TV Safari/537.36", 1920, 1080, 1, 0, KindTV},
		{"Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36 WebAppManager", 1920, 1080, 1, 0, KindTV},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64; Xbox; Xbox One) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", 1920, 1080, 1, 0, KindTV},
		{"Mozilla/5.0 (PlayStation; PlayStation 5/2.26) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0 Safari/605.1.15", 1920, 1080, 1, 0, KindTV},
		// The Steam Deck: desktop mode's Firefox, and Steam's own browser.
		{"Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0", 1280, 800, 1, 10, KindHandheld},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678", 1280, 800, 1, 10, KindHandheld},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678", 0, 0, 0, 0, KindHandheld},
		// Steam's own browser on a desktop is that desktop's.
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678", 2560, 1440, 1, 0, KindMonitor},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam Client", 1920, 1080, 1, 0, KindMonitor},
		// Chrome on an Android tablet asks for desktop sites as Linux: the
		// Pixel Tablet, the Galaxy Tab S9 FE and S9 Ultra.
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36", 1280, 800, 2, 5, KindTablet},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36", 1152, 720, 2, 10, KindTablet},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36", 924, 1480, 2, 10, KindTablet},
		// A Deck at 150 % in desktop mode is still one.
		{"Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0", 853, 533, 1.5, 10, KindHandheld},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", 1280, 720, 1.5, 10, KindHandheld}, // ROG Ally
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 800, 1280, 2, 10, KindHandheld},                 // portrait
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 1280, 800, 1.5, 0, KindLaptop},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 2560, 1440, 1.5, 0, KindMonitor},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 2560, 1440, 1, 0, KindMonitor},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 1920, 1080, 1, 0, KindMonitor},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 1280, 800, 2, 10, KindLaptop},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", 1366, 768, 1, 0, KindMonitor},
		{"Mozilla/5.0 (compatible; Microsoft Office)", 1920, 1080, 1, 0, KindUnknown},
		{"curl/8.7.1", 0, 0, 0, 0, KindUnknown},
		{"", 1920, 1080, 1, 0, KindUnknown},
	} {
		if got := ClassifyBrowser(c.ua, c.w, c.h, c.dpr, c.touch); got != c.want {
			t.Errorf("ClassifyBrowser(%q, %dx%d@%v, touch %d) = %q, want %q", c.ua, c.w, c.h, c.dpr, c.touch, got, c.want)
		}
	}
}

func TestDeviceLabel(t *testing.T) {
	const (
		mac   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
		linux = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
	)
	for _, c := range []struct {
		kind Kind
		ua   string
		want string
	}{
		{KindPhone, "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", "iPhone"},
		{"", "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", "iPad"},
		{KindTablet, mac, "iPad"},
		{KindLaptop, mac, "Mac"},
		{"", mac, "Mac"},
		{"", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36", "Android phone"},
		{"", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", "Android tablet"},
		{"", "Mozilla/5.0 (Linux; Android 11; SHIELD Android TV Build/RQ1A.210105.003; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Safari/537.36", "TV"},
		{"", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; Xbox; Xbox One) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", "TV"},
		{"", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678", "Steam Deck"},
		{KindHandheld, "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678", "Steam Deck"},
		// Steam's own browser on a desktop is named after the desktop.
		{KindMonitor, "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam GameOverlay/1712345678", "Linux PC"},
		{KindMonitor, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Valve Steam Client", "Windows PC"},
		{KindHandheld, linux, "Steam Deck"},
		// An Android tablet asking for desktop sites.
		{KindTablet, "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36", "Android tablet"},
		{KindMonitor, linux, "Linux PC"},
		{KindHandheld, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", "Windows PC"},
		{"", "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", ""},
		{"", "", ""},
	} {
		if got := DeviceLabel(c.kind, c.ua); got != c.want {
			t.Errorf("DeviceLabel(%q, %q) = %q, want %q", c.kind, c.ua, got, c.want)
		}
	}
}

func TestUserKindAndGenericName(t *testing.T) {
	for _, k := range []string{"phone", "handheld", "tablet", "laptop", "monitor", "tv"} {
		if got, ok := UserKind(k); !ok || string(got) != k {
			t.Errorf("UserKind(%q) = %q, %v", k, got, ok)
		}
	}
	for _, k := range []string{"unknown", "auto", "", "TV", "desktop"} {
		if _, ok := UserKind(k); ok {
			t.Errorf("UserKind(%q) accepted", k)
		}
	}
	for name, want := range map[string]bool{"": true, "roth": true, " ROTH ": true, "Moonlight": true, "unknown": true, "roth2": false, "iPhone": false} {
		if GenericName(name) != want {
			t.Errorf("GenericName(%q) = %v", name, !want)
		}
	}
}

func ExampleScaleFor() {
	deck := edid.Mode{W: 1280, H: 800, Refresh: 60}
	s := ScaleFor(KindHandheld, deck, 1, Panel{})
	fmt.Println(s, GameDPI(s, deck))
	// Output: 1.5 96
}
