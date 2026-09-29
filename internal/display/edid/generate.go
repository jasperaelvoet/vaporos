package edid

import (
	"errors"
	"fmt"
	"math"
)

// Identity of the virtual display. gamescope names a display "<Make>
// <Model>", where Make is the PNP id looked up in hwdata's pnp.ids and Model
// is the display product name; that string keys its modes.cfg.
const (
	PNPID       = "VPR"
	ProductCode = 1
	// ModelName is the product name descriptor. The descriptor holds at
	// most 13 characters, so "VaporOS Virtual" cannot fit.
	ModelName = "VaporOS"
	// modelYear goes into the manufacture date; fixed so builds are reproducible.
	modelYear = 2026
)

// HDR static metadata advertised in the CTA block (CTA-861-G 7.5.13): a
// 1000-nit peak, 600-nit frame average, 0.01-nit black panel, which is what
// typical HDR10 content is mastered for.
const (
	hdrMaxNits     = 1000.0
	hdrMaxFALLNits = 600.0
	hdrMinNits     = 0.01
)

// EDID block tags and sizes.
const (
	blockLen     = 128
	tagCTA       = 0x02
	tagDisplayID = 0x70
	dtdLen       = 18
	didTimingLen = 20
	// A DisplayID section in an EDID extension: tag + 4-byte header, data
	// blocks, section checksum, then the EDID block checksum at byte 127.
	didMaxPayload = blockLen - 1 - 5 - 1 // 121
	didPerBlock   = (didMaxPayload - 3) / didTimingLen
	// The first (base) section also carries the product identification,
	// display parameters and display interface blocks, and repeats the
	// preferred timing, as DisplayID requires of a display.
	didProductIDLen = 3 + 12 + len(ModelName)
	didParamsLen    = 3 + 12
	didIntfLen      = 3 + 10
	didFirstBlock   = (didMaxPayload-didProductIDLen-didParamsLen-didIntfLen-3)/didTimingLen - 1
	ctaMaxDTDs      = 6
	// The physical size the base block claims, in mm; DTD image sizes must
	// fit inside it.
	screenWmm, screenHmm = 600, 340
)

// Result is a generated EDID and what it contains.
type Result struct {
	EDID    []byte
	Modes   []Mode  // every mode offered, preferred first
	Skipped []error // requested extra modes that were left out, and why
}

// Generate builds the VaporOS virtual display EDID: the catalogue plus up to
// MaxExtra extra modes (configured first, then learned from clients).
func Generate(extra []Mode) (*Result, error) {
	res := &Result{}
	seen := map[Mode]bool{}
	add := func(m Mode) error {
		if seen[m] {
			return nil
		}
		if err := Check(m); err != nil {
			return err
		}
		seen[m] = true
		res.Modes = append(res.Modes, m)
		return nil
	}
	if err := add(Preferred); err != nil {
		return nil, err
	}
	for _, m := range Catalogue {
		if err := add(m); err != nil {
			return nil, fmt.Errorf("catalogue: %w", err)
		}
	}
	added := 0
	for _, m := range extra {
		if seen[m] {
			continue
		}
		if added >= MaxExtra {
			res.Skipped = append(res.Skipped, fmt.Errorf("%s: more than %d extra modes", m, MaxExtra))
			continue
		}
		if err := add(m); err != nil {
			res.Skipped = append(res.Skipped, err)
			continue
		}
		added++
	}

	// Place timings: the preferred one first in the base block, then the
	// next DTD-encodable one; six more DTDs in the CTA block; everything
	// else (including timings whose porches overflow a DTD) in DisplayID.
	var base, cta, did []Timing
	base = append(base, CVTRB2(res.Modes[0]))
	for _, m := range res.Modes[1:] {
		t := CVTRB2(m)
		switch {
		case fitsDTD(t) && len(base) < 2:
			base = append(base, t)
		case fitsDTD(t) && len(cta) < ctaMaxDTDs:
			cta = append(cta, t)
		default:
			did = append(did, t)
		}
	}

	var chunks [][]Timing
	for rest, n := did, didFirstBlock; len(rest) > 0; n = didPerBlock {
		k := min(n, len(rest))
		chunks = append(chunks, rest[:k])
		rest = rest[k:]
	}
	if 2+len(chunks) > 255 {
		return nil, errors.New("edid: too many modes")
	}
	out := baseBlock(base, 1+len(chunks))
	out = append(out, ctaBlock(cta)...)
	for i, c := range chunks {
		if i == 0 {
			out = append(out, displayIDBaseBlock(base[0], c, len(chunks)-1)...)
		} else {
			out = append(out, displayIDBlock(c)...)
		}
	}
	res.EDID = out
	return res, nil
}

// fitsDTD reports whether an 18-byte detailed timing descriptor can encode t.
func fitsDTD(t Timing) bool {
	return t.W <= 4095 && t.H <= 4095 && t.HBlank() <= 4095 && t.VBlank() <= 4095 &&
		t.HFront <= 1023 && t.HSync <= 1023 && t.VFront <= 63 && t.VSync <= 63 &&
		(t.ClockKHz+5)/10 <= 0xffff
}

func baseBlock(dtds []Timing, extensions int) []byte {
	b := make([]byte, blockLen)
	copy(b, []byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00})
	pnp := uint16(PNPID[0]-'A'+1)<<10 | uint16(PNPID[1]-'A'+1)<<5 | uint16(PNPID[2]-'A'+1)
	b[8], b[9] = byte(pnp>>8), byte(pnp)
	b[10], b[11] = ProductCode&0xff, ProductCode>>8
	// 12-15 serial number: 0. 16 week: unspecified.
	b[17] = modelYear - 1990
	b[18], b[19] = 1, 4 // EDID 1.4
	// Digital input, 10 bits per primary (so HDR can run at 10 bpc),
	// DisplayPort interface.
	b[20] = 0x80 | 0x30 | 0x05
	b[21], b[22] = screenWmm/10, screenHmm/10 // a 27" 16:9 panel
	b[23] = 120                               // gamma 2.2
	// Features: RGB 4:4:4 only, sRGB is the default colour space, the
	// preferred timing is the native one. Not continuous-frequency: that
	// would let the kernel invent extra modes from the range limits.
	b[24] = 0x04 | 0x02
	copy(b[25:35], srgbChromaticity())
	// Established timings: only 640x480@60, the fail-safe mode CTA-861
	// requires every sink to accept. 38-53 standard timings: unused.
	b[35] = 0x20
	for i := 38; i < 54; i++ {
		b[i] = 0x01
	}
	slot := 54
	for _, t := range dtds {
		copy(b[slot:], dtd(t))
		slot += dtdLen
	}
	copy(b[slot:], rangeDescriptor())
	slot += dtdLen
	copy(b[slot:], nameDescriptor(ModelName))
	b[126] = byte(extensions)
	b[127] = checksum(b[:127])
	return b
}

// srgbChromaticity encodes the BT.709/sRGB primaries and D65 white point.
func srgbChromaticity() []byte {
	c := func(v float64) int { return int(math.Round(v * 1024)) }
	rx, ry := c(0.640), c(0.330)
	gx, gy := c(0.300), c(0.600)
	bx, by := c(0.150), c(0.060)
	wx, wy := c(0.3127), c(0.3290)
	lo := func(a, b, c, d int) byte { return byte((a&3)<<6 | (b&3)<<4 | (c&3)<<2 | d&3) }
	return []byte{
		lo(rx, ry, gx, gy), lo(bx, by, wx, wy),
		byte(rx >> 2), byte(ry >> 2), byte(gx >> 2), byte(gy >> 2),
		byte(bx >> 2), byte(by >> 2), byte(wx >> 2), byte(wy >> 2),
	}
}

// dtd encodes an 18-byte detailed timing descriptor (clock in 10 kHz units).
func dtd(t Timing) []byte {
	d := make([]byte, dtdLen)
	clk := (t.ClockKHz + 5) / 10
	d[0], d[1] = byte(clk), byte(clk>>8)
	hb, vb := t.HBlank(), t.VBlank()
	d[2], d[3], d[4] = byte(t.W), byte(hb), byte((t.W>>8)<<4|(hb>>8)&0xf)
	d[5], d[6], d[7] = byte(t.H), byte(vb), byte((t.H>>8)<<4|(vb>>8)&0xf)
	d[8], d[9] = byte(t.HFront), byte(t.HSync)
	d[10] = byte((t.VFront&0xf)<<4 | t.VSync&0xf)
	d[11] = byte((t.HFront>>8)<<6 | (t.HSync>>8)<<4 | (t.VFront>>4)<<2 | t.VSync>>4)
	wmm, hmm := imageSizeMM(t.Mode)
	d[12], d[13], d[14] = byte(wmm), byte(hmm), byte((wmm>>8)<<4|(hmm>>8)&0xf)
	misc := byte(0x18) // digital separate sync
	if t.VSyncPositive {
		misc |= 0x04
	}
	if t.HSyncPositive {
		misc |= 0x02
	}
	d[17] = misc
	return d
}

// imageSizeMM is the largest area of the mode's aspect ratio that fits the
// screen size the base block claims.
func imageSizeMM(m Mode) (int, int) {
	w, h := float64(screenWmm), float64(screenWmm)*float64(m.H)/float64(m.W)
	if h > screenHmm {
		w, h = float64(screenHmm)*float64(m.W)/float64(m.H), screenHmm
	}
	return int(math.Floor(w)), int(math.Floor(h))
}

func rangeDescriptor() []byte {
	d := []byte{0, 0, 0, 0xfd,
		0x08, // max horizontal rate carries a +255 kHz offset
		MinVRate, MaxVRate, MinHRateKHz, MaxHRateKHz - 255,
		RangeClockMH / 10,
		0x01, // range limits only, no GTF/CVT formula
		0x0a, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20}
	return d
}

func nameDescriptor(name string) []byte {
	d := []byte{0, 0, 0, 0xfc, 0}
	s := []byte(name)
	if len(s) > 13 {
		s = s[:13]
	}
	d = append(d, s...)
	if len(s) < 13 {
		d = append(d, 0x0a)
	}
	for len(d) < dtdLen {
		d = append(d, 0x20)
	}
	return d
}

// ctaBlock is a CTA-861 extension with a video capability block (RGB
// quantization selectable, everything underscanned), colorimetry (BT.2020
// RGB and YCC), HDR static metadata (SDR, SMPTE ST 2084 and HLG) and up to
// six DTDs. It carries no short video descriptors on purpose: a CTA VIC
// such as 97 (4K60 at 594 MHz) would add a second, fatter 3840x2160@60
// that sorts before ours and might not fit the link at 10 bpc.
func ctaBlock(dtds []Timing) []byte {
	b := make([]byte, blockLen)
	b[0], b[1] = tagCTA, 3
	data := []byte{
		0xe2, 0x00, 0x4a, // extended tag 0: video capability: QS, IT and CE underscanned
		0xe3, 0x05, 0xc0, 0x00, // extended tag 5: colorimetry BT2020_RGB | BT2020_YCC
		0xe6, 0x06, 0x0d, 0x01, // extended tag 6: HDR static metadata, EOTFs SDR|PQ|HLG, type 1
		lumCode(hdrMaxNits), lumCode(hdrMaxFALLNits), minLumCode(hdrMinNits, hdrMaxNits),
	}
	copy(b[4:], data)
	off := 4 + len(data)
	b[2] = byte(off)
	b[3] = 0x80 // underscans IT formats; no audio, no YCbCr, no native DTDs
	for _, t := range dtds {
		copy(b[off:], dtd(t))
		off += dtdLen
	}
	b[127] = checksum(b[:127])
	return b
}

// lumCode encodes a luminance as 50 * 2^(CV/32) cd/m² (CTA-861-G).
func lumCode(nits float64) byte {
	return byte(math.Round(32 * math.Log2(nits/50)))
}

// minLumCode encodes the minimum as maxLum * (CV/255)² / 100.
func minLumCode(minNits, maxNits float64) byte {
	maxActual := 50 * math.Pow(2, float64(lumCode(maxNits))/32)
	return byte(math.Round(255 * math.Sqrt(minNits*100/maxActual)))
}

// DisplayID 1.3 product types.
const (
	didProductExtension  = 0x00
	didProductStandalone = 0x03
)

// displayIDBaseBlock is the first DisplayID section: a standalone display
// described by product identification, display parameters and display
// interface blocks (the kernel ignores them, but DisplayID requires them),
// the preferred timing again (marked preferred, which DisplayID also
// requires; the kernel merges it with the identical base-block mode) and
// the first few other timings. followers is how many extension sections
// come after it.
func displayIDBaseBlock(preferred Timing, ts []Timing, followers int) []byte {
	le16 := func(b []byte, v int) []byte { return append(b, byte(v), byte(v>>8)) }
	var data []byte

	data = append(data, 0x00, 0x00, byte(didProductIDLen-3)) // product identification
	data = append(data, PNPID...)
	data = le16(data, ProductCode)
	data = append(data, 0, 0, 0, 0)           // serial number
	data = append(data, 0, modelYear-2000)    // week unspecified, year
	data = append(data, byte(len(ModelName))) // product name
	data = append(data, ModelName...)

	data = append(data, 0x01, 0x00, byte(didParamsLen-3)) // display parameters
	data = le16(data, screenWmm*10)                       // image size, 0.1 mm
	data = le16(data, screenHmm*10)
	data = le16(data, preferred.W) // native format: the preferred timing
	data = le16(data, preferred.H)
	data = append(data,
		0x00, // no audio, no DPM, not a fixed timing/pixel format
		120,  // gamma 2.2
		byte(math.Round((float64(preferred.W)/float64(preferred.H)-1)*100)),
		0x99, // 10 bpc native and overall
	)

	data = append(data, 0x0f, 0x00, byte(didIntfLen-3)) // display interface
	data = append(data,
		0xa4,       // DisplayPort, 4 lanes
		0x14,       // version 1.4
		0x06,       // RGB at 8 and 10 bpc
		0x00, 0x00, // no YCbCr
		0x00, 0x00, // no content protection
		0x00,       // no spread spectrum
		0x00, 0x00, // no interface attributes
	)

	data = append(data, typeITimings(append([]Timing{preferred}, ts...), 0)...)
	return displayIDSection(didProductStandalone, byte(followers), data)
}

// displayIDBlock is a follow-on DisplayID section with up to five timings.
func displayIDBlock(ts []Timing) []byte {
	return displayIDSection(didProductExtension, 0, typeITimings(ts, -1))
}

// typeITimings encodes a "Video Timing Modes Type 1 - Detailed Timings"
// data block (tag 0x03): the DisplayID timing format the Linux kernel turns
// into connector modes (drm_edid.c add_displayid_detailed_modes). The timing
// at index preferred (-1 for none) is flagged preferred.
func typeITimings(ts []Timing, preferred int) []byte {
	b := []byte{0x03, 0x00, byte(didTimingLen * len(ts))}
	for i, t := range ts {
		d := displayIDTiming(t)
		if i == preferred {
			d[3] |= 0x80
		}
		b = append(b, d...)
	}
	return b
}

// displayIDSection wraps data blocks in a DisplayID 1.3 section inside an
// EDID extension block.
func displayIDSection(productType, extCount byte, data []byte) []byte {
	b := make([]byte, blockLen)
	b[0] = tagDisplayID
	b[1] = 0x13 // DisplayID 1.3
	b[2] = byte(len(data))
	b[3] = productType
	b[4] = extCount
	copy(b[5:], data)
	off := 5 + len(data)
	// The section checksum covers the header and payload (bytes 1..off-1).
	b[off] = checksum(b[1:off])
	b[127] = checksum(b[:127])
	return b
}

func displayIDTiming(t Timing) []byte {
	d := make([]byte, didTimingLen)
	le16 := func(i, v int) { d[i], d[i+1] = byte(v), byte(v>>8) }
	clk := (t.ClockKHz+5)/10 - 1
	d[0], d[1], d[2] = byte(clk), byte(clk>>8), byte(clk>>16)
	d[3] = aspectCode(t.Mode)
	le16(4, t.W-1)
	le16(6, t.HBlank()-1)
	hs := t.HFront - 1
	if t.HSyncPositive {
		hs |= 1 << 15
	}
	le16(8, hs)
	le16(10, t.HSync-1)
	le16(12, t.H-1)
	le16(14, t.VBlank()-1)
	vs := t.VFront - 1
	if t.VSyncPositive {
		vs |= 1 << 15
	}
	le16(16, vs)
	le16(18, t.VSync-1)
	return d
}

// aspectCode is the DisplayID 1.3 Type I aspect ratio field.
func aspectCode(m Mode) byte {
	for code, r := range []Mode{{1, 1, 0}, {5, 4, 0}, {4, 3, 0}, {15, 9, 0}, {16, 9, 0}, {16, 10, 0}, {64, 27, 0}, {256, 135, 0}} {
		if m.W*r.H == m.H*r.W {
			return byte(code)
		}
	}
	return 8 // not defined
}

// checksum returns the byte that makes b plus itself sum to 0 mod 256.
func checksum(b []byte) byte {
	var s byte
	for _, v := range b {
		s += v
	}
	return -s
}
