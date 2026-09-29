package edid

import (
	"math"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{
		"1920x1080@60":    {1920, 1080, 60},
		" 2796X1290@120 ": {2796, 1290, 120},
		"2560x1440":       {2560, 1440, 60},
		"1920x1080@59.94": {1920, 1080, 60},
		"1280x800@89.9":   {1280, 800, 90},
	} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "1920", "1920x", "x1080@60", "0x0@60", "1920x1080@", "1920x1080@abc", "1920x1080@-5"} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("ParseMode(%q) accepted", bad)
		}
	}
	if s := (Mode{2560, 1600, 120}).String(); s != "2560x1600@120" {
		t.Errorf("String = %q", s)
	}
}

// Published CVT-RB2 timings (VESA CVT 1.2 spreadsheet / edid-decode --cvt).
func TestCVTRB2Known(t *testing.T) {
	for _, c := range []struct {
		m                Mode
		clockKHz, ht, vt int
		vfp              int
	}{
		{Mode{1920, 1080, 60}, 133320, 2000, 1111, 17},
		{Mode{3840, 2160, 60}, 522614, 3920, 2222, 48},
		{Mode{2560, 1440, 60}, 234590, 2640, 1481, 27},
		{Mode{1280, 720, 60}, 60465, 1360, 741, 7},
	} {
		got := CVTRB2(c.m)
		if got.ClockKHz != c.clockKHz || got.HTotal() != c.ht || got.VTotal() != c.vt || got.VFront != c.vfp {
			t.Errorf("%s: clock %d ht %d vt %d vfp %d; want %d %d %d %d",
				c.m, got.ClockKHz, got.HTotal(), got.VTotal(), got.VFront, c.clockKHz, c.ht, c.vt, c.vfp)
		}
		if got.HFront != 8 || got.HSync != 32 || got.HBack != 40 || got.VSync != 8 || got.VBack != 6 {
			t.Errorf("%s: porches %+v", c.m, got)
		}
		if !got.HSyncPositive || got.VSyncPositive {
			t.Errorf("%s: RB timings are +hsync -vsync", c.m)
		}
	}
}

// referenceRB2 is a direct transcription of edid-decode's floating-point
// calc_cvt_mode for RB_CVT_V2, used to cross-check the integer version.
func referenceRB2(w, h int, rate float64) (clockKHz, vblank int) {
	hPeriodEst := ((1000000.0 / rate) - 460.0) / float64(h)
	vbiLines := math.Floor(460.0/hPeriodEst) + 1
	vBlank := math.Max(vbiLines, 1+8+6)
	totalV := vBlank + float64(h)
	totalPixels := 80 + float64(w)
	freq := rate * totalV * totalPixels
	pixelFreq := math.Floor((freq/1000000.0)/0.001) * 0.001
	return int(math.Round(1000 * pixelFreq)), int(vBlank)
}

func TestCVTRB2MatchesReference(t *testing.T) {
	var modes []Mode
	modes = append(modes, Catalogue...)
	for _, w := range []int{800, 1024, 1366, 1600, 2048, 2880, 3000, 3200, 5120} {
		for _, r := range []int{24, 30, 50, 60, 75, 90, 100, 120, 144, 165, 240} {
			modes = append(modes, Mode{w, w * 9 / 16, r}, Mode{w, w * 10 / 16, r})
		}
	}
	for _, m := range modes {
		got := CVTRB2(m)
		clk, vb := referenceRB2(m.W, m.H, float64(m.Refresh))
		// Floating point can land one kHz step apart at exact boundaries.
		if d := got.ClockKHz - clk; d < -1 || d > 1 || got.VBlank() != vb {
			t.Errorf("%s: clock %d vblank %d; reference %d %d", m, got.ClockKHz, got.VBlank(), clk, vb)
		}
		if math.Abs(got.ExactRefresh()-float64(m.Refresh)) > 0.01 {
			t.Errorf("%s: real refresh %.4f", m, got.ExactRefresh())
		}
	}
}

func TestCheck(t *testing.T) {
	for _, m := range Catalogue {
		if err := Check(m); err != nil {
			t.Errorf("catalogue mode rejected: %v", err)
		}
		if tt := CVTRB2(m); tt.ClockKHz > 600000 {
			t.Errorf("%s over 600 MHz", m)
		}
	}
	for _, m := range []Mode{{4096, 2160, 60}, {4096, 2160, 30}, {3840, 2160, 120}, {1920, 1080, 360}, {1920, 1080, 10}, {100, 100, 60}} {
		if Check(m) == nil {
			t.Errorf("%s accepted", m)
		}
	}
}

func blockSum(b []byte) byte {
	var s byte
	for _, v := range b {
		s += v
	}
	return s
}

func TestGenerateStructure(t *testing.T) {
	res, err := Generate(nil)
	if err != nil {
		t.Fatal(err)
	}
	b := res.EDID
	// base + CTA + 14 remaining catalogue modes in DisplayID: 2 in the base
	// section (next to its descriptive blocks and the repeated preferred
	// timing), then 5, 5 and 2.
	if len(b) != 6*128 || int(b[126]) != 5 {
		t.Fatalf("size %d, extensions %d; want 6 blocks", len(b), b[126])
	}
	if b[35] != 0x20 {
		t.Error("640x480@60 missing from the established timings")
	}
	for i := 0; i < len(b)/128; i++ {
		if s := blockSum(b[i*128 : (i+1)*128]); s != 0 {
			t.Errorf("block %d sums to %d", i, s)
		}
	}
	if string(b[:8]) != "\x00\xff\xff\xff\xff\xff\xff\x00" || b[18] != 1 || b[19] != 4 {
		t.Error("bad header or version")
	}
	if b[24]&0x01 != 0 {
		t.Error("continuous-frequency bit set: the kernel would add inferred modes")
	}
	if b[128] != 0x02 || b[128+1] != 3 {
		t.Errorf("block 1 is not CTA-861 rev 3")
	}
	for i := 2; i < 6; i++ {
		blk := b[i*128 : (i+1)*128]
		if blk[0] != 0x70 || blk[1] != 0x13 {
			t.Fatalf("block %d is not DisplayID 1.3", i)
		}
		payload := int(blk[2])
		// The kernel checksums the DisplayID section (header+payload+checksum).
		if s := blockSum(blk[1 : 5+payload+1]); s != 0 {
			t.Errorf("DisplayID section in block %d sums to %d", i, s)
		}
		// Walk the data blocks the way drm_displayid.c does.
		var tags []byte
		for off := 5; off+3 <= 5+payload; off += 3 + int(blk[off+2]) {
			tag, n := blk[off], int(blk[off+2])
			if off+3+n > 5+payload {
				t.Fatalf("block %d: data block at %d overflows the section", i, off)
			}
			// The kernel only takes Type I blocks whose length is a multiple of 20.
			if tag == 0x03 && (n == 0 || n%20 != 0) {
				t.Errorf("block %d: Type I block of %d bytes", i, n)
			}
			tags = append(tags, tag)
		}
		if i == 2 {
			// Base section: standalone display, 3 sections follow; product
			// id, display parameters, display interface, then timings.
			if blk[3] != 0x03 || blk[4] != 3 || string(tags) != "\x00\x01\x0f\x03" || string(blk[8:11]) != PNPID {
				t.Errorf("base section: header % x, blocks % x", blk[1:5], tags)
			}
		} else if blk[3] != 0 || blk[4] != 0 || string(tags) != "\x03" {
			t.Errorf("block %d: extension section header % x, blocks % x", i, blk[1:5], tags)
		}
	}
}

// TestIdentity pins the vendor bytes: gamescope's modes.cfg key and the
// name Steam shows derive from them. "VPR" (Best Buy in hwdata) must not
// come back.
func TestIdentity(t *testing.T) {
	if PNPID != "VOS" || ModelName != "VaporOS" {
		t.Fatalf("identity %q %q", PNPID, ModelName)
	}
	res, err := Generate(nil)
	if err != nil {
		t.Fatal(err)
	}
	// V=22 O=15 S=19, five bits each, big-endian: 0x59f3.
	if got := res.EDID[8:10]; got[0] != 0x59 || got[1] != 0xf3 {
		t.Errorf("manufacturer bytes % x, want 59 f3", got)
	}
	if info, err := Decode(res.EDID); err != nil || info.PNP != "VOS" {
		t.Errorf("decoded PNP %q, %v", info.PNP, err)
	}
}

func TestRoundTrip(t *testing.T) {
	res, err := Generate(nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Decode(res.EDID)
	if err != nil {
		t.Fatal(err)
	}
	if info.PNP != PNPID || info.Name != ModelName || info.Product != ProductCode || info.Version != "1.4" {
		t.Errorf("identity %+v", info)
	}
	if len(res.Modes) != len(Catalogue) {
		t.Errorf("%d modes, catalogue has %d", len(res.Modes), len(Catalogue))
	}
	for _, m := range Catalogue {
		if !info.Has(m) {
			t.Errorf("decoded EDID lacks %s", m)
		}
	}
	if got := info.Modes(); len(got) != len(Catalogue) {
		t.Errorf("decoded %d distinct modes: %v", len(got), got)
	}
	var pref []Mode
	sources := map[string]int{}
	for _, tt := range info.Timings {
		if tt.Preferred {
			pref = append(pref, tt.Mode)
		}
		sources[tt.Source]++
		want := CVTRB2(tt.Mode)
		if tt.HTotal() != want.HTotal() || tt.VTotal() != want.VTotal() || tt.VFront != want.VFront ||
			tt.HSyncPositive != want.HSyncPositive || tt.VSyncPositive != want.VSyncPositive {
			t.Errorf("%s (%s) decoded as %+v, generated %+v", tt.Mode, tt.Source, tt.Timing, want)
		}
		if d := tt.ClockKHz - want.ClockKHz; d < -5 || d > 5 {
			t.Errorf("%s clock %d vs %d", tt.Mode, tt.ClockKHz, want.ClockKHz)
		}
	}
	// The base block's first DTD, repeated as DisplayID's preferred timing.
	if len(pref) != 2 || pref[0] != Preferred || pref[1] != Preferred {
		t.Errorf("preferred = %v", pref)
	}
	if sources["base"] != 2 || sources["cta"] != 6 || sources["displayid"] != 15 {
		t.Errorf("timing placement %v", sources)
	}
	if h := info.HDR; h == nil || strings.Join(h.EOTFs, " ") != "SDR ST2084 HLG" ||
		h.MaxNits < 950 || h.MaxNits > 1050 || h.MinNits <= 0 || h.MinNits > 0.02 {
		t.Errorf("HDR = %+v", info.HDR)
	}
	if strings.Join(info.Colorimetry, " ") != "BT2020RGB BT2020YCC" {
		t.Errorf("colorimetry = %v", info.Colorimetry)
	}
	if r := info.Range; r == nil || r.MinV != 24 || r.MaxV != 240 || r.MinHKHz != 15 || r.MaxHKHz != 400 || r.MaxClockMHz != 700 {
		t.Errorf("range = %+v", info.Range)
	}
	if !strings.Contains(info.Format(), "3840x2160@60") {
		t.Error("Format lacks 4K60")
	}
}

func TestGenerateExtras(t *testing.T) {
	extra := []Mode{
		{1920, 1080, 60},  // duplicate of the catalogue: ignored silently
		{4096, 2160, 60},  // blocklisted
		{3840, 2160, 120}, // over 600 MHz
		{1280, 720, 240},  // porches too big for a DTD: DisplayID
		{2560, 1080, 75},
	}
	for i := 0; i < 40; i++ {
		extra = append(extra, Mode{1000 + 8*i, 700, 60})
	}
	res, err := Generate(extra)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(Catalogue) + MaxExtra; len(res.Modes) != want {
		t.Errorf("%d modes, want %d", len(res.Modes), want)
	}
	// 2 rejected + 12 over the cap.
	if len(res.Skipped) != 2+12 {
		t.Errorf("skipped %d: %v", len(res.Skipped), res.Skipped)
	}
	info, err := Decode(res.EDID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.EDID) != 128*(int(res.EDID[126])+1) {
		t.Error("length does not match the extension count")
	}
	for _, m := range res.Modes {
		if !info.Has(m) {
			t.Errorf("lost %s", m)
		}
	}
	for _, m := range []Mode{{1280, 720, 240}, {2560, 1080, 75}} {
		if !info.Has(m) {
			t.Errorf("extra %s missing", m)
		}
	}
	if info.Has(Mode{4096, 2160, 60}) || info.Has(Mode{3840, 2160, 120}) {
		t.Error("invalid extra mode made it in")
	}
	for _, tt := range info.Timings {
		if tt.ClockKHz > 600000 {
			t.Errorf("%s at %d kHz", tt.Mode, tt.ClockKHz)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	res, _ := Generate(nil)
	b := append([]byte(nil), res.EDID...)
	b[200]++
	if _, err := Decode(b); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("corrupt block accepted: %v", err)
	}
	if _, err := Decode(res.EDID[:256]); err == nil {
		t.Error("truncated EDID accepted")
	}
	if _, err := Decode([]byte("not an edid")); err == nil {
		t.Error("garbage accepted")
	}
	// A DisplayID section checksum error is fatal for the kernel too.
	b = append([]byte(nil), res.EDID...)
	blk := b[256:384]
	blk[10]++
	blk[127] = checksum(blk[:127])
	if _, err := Decode(b); err == nil || !strings.Contains(err.Error(), "DisplayID") {
		t.Errorf("bad DisplayID checksum accepted: %v", err)
	}
}

func TestSameAspect(t *testing.T) {
	if !SameAspect(Mode{2796, 1290, 0}, Mode{2556, 1179, 0}) {
		t.Error("iPhone panels differ")
	}
	if !SameAspect(Mode{1366, 768, 0}, Mode{1920, 1080, 0}) {
		t.Error("1366x768 is 16:9")
	}
	if SameAspect(Mode{1920, 1200, 0}, Mode{1920, 1080, 0}) {
		t.Error("16:10 matched 16:9")
	}
}
