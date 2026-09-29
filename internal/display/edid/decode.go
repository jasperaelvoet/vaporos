package edid

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Info is what Decode extracts from an EDID: identity, the detailed timings
// (base block DTDs, CTA DTDs and DisplayID Type I/VII timings, which are
// what VaporOS generates) and the HDR capabilities.
type Info struct {
	Version     string // "1.4"
	PNP         string // three-letter manufacturer id, e.g. "VPR"
	Product     uint16
	Serial      uint32
	Name        string // display product name descriptor
	Blocks      int
	Timings     []DecodedTiming
	Range       *RangeLimits
	HDR         *HDRStatic
	Colorimetry []string
}

// DecodedTiming is a timing plus where it came from.
type DecodedTiming struct {
	Timing
	Source    string // "base", "cta", "displayid"
	Preferred bool
}

// RangeLimits is the display range limits descriptor.
type RangeLimits struct {
	MinV, MaxV       int // Hz
	MinHKHz, MaxHKHz int
	MaxClockMHz      int
}

// HDRStatic is the CTA HDR static metadata data block.
type HDRStatic struct {
	EOTFs                []string
	MaxNits, MaxFALLNits float64
	MinNits              float64
}

// Modes returns the distinct modes, in EDID order.
func (i *Info) Modes() []Mode {
	seen := map[Mode]bool{}
	var out []Mode
	for _, t := range i.Timings {
		if !seen[t.Mode] {
			seen[t.Mode] = true
			out = append(out, t.Mode)
		}
	}
	return out
}

// Has reports whether the EDID offers mode m.
func (i *Info) Has(m Mode) bool {
	for _, t := range i.Timings {
		if t.Mode == m {
			return true
		}
	}
	return false
}

var header = []byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00}

// Decode parses an EDID blob. It checks the header, the block count and
// every block checksum, because the kernel rejects an EDID that fails any
// of them.
func Decode(b []byte) (*Info, error) {
	if len(b) < blockLen || !bytes.Equal(b[:8], header) {
		return nil, errors.New("edid: not an EDID (bad header)")
	}
	n := int(b[126]) + 1
	if len(b) < n*blockLen {
		return nil, fmt.Errorf("edid: %d bytes but the base block announces %d blocks", len(b), n)
	}
	for i := 0; i < n; i++ {
		blk := b[i*blockLen : (i+1)*blockLen]
		if checksum(blk[:127]) != blk[127] {
			return nil, fmt.Errorf("edid: block %d checksum mismatch", i)
		}
	}
	info := &Info{
		Version: fmt.Sprintf("%d.%d", b[18], b[19]),
		PNP:     decodePNP(b[8], b[9]),
		Product: uint16(b[10]) | uint16(b[11])<<8,
		Serial:  uint32(b[12]) | uint32(b[13])<<8 | uint32(b[14])<<16 | uint32(b[15])<<24,
		Blocks:  n,
	}
	firstDTD := true
	for slot := 54; slot < 126; slot += dtdLen {
		d := b[slot : slot+dtdLen]
		if d[0] != 0 || d[1] != 0 {
			t, ok := decodeDTD(d)
			if ok {
				info.Timings = append(info.Timings, DecodedTiming{Timing: t, Source: "base", Preferred: firstDTD})
			}
			firstDTD = false
			continue
		}
		switch d[3] {
		case 0xfc:
			info.Name = descriptorString(d[5:])
		case 0xfd:
			info.Range = decodeRange(d)
		}
	}
	for i := 1; i < n; i++ {
		blk := b[i*blockLen : (i+1)*blockLen]
		switch blk[0] {
		case tagCTA:
			decodeCTA(blk, info)
		case tagDisplayID:
			if err := decodeDisplayID(blk, info); err != nil {
				return nil, fmt.Errorf("edid: block %d: %w", i, err)
			}
		}
	}
	return info, nil
}

func decodePNP(hi, lo byte) string {
	v := uint16(hi)<<8 | uint16(lo)
	c := func(x uint16) byte { return byte('A' + x - 1) }
	return string([]byte{c(v >> 10 & 0x1f), c(v >> 5 & 0x1f), c(v & 0x1f)})
}

// descriptorString decodes a text descriptor: up to 13 bytes, ended by LF,
// padded with spaces.
func descriptorString(b []byte) string {
	if i := bytes.IndexByte(b, 0x0a); i >= 0 {
		b = b[:i]
	}
	return strings.TrimRight(string(b), " \x00")
}

func decodeDTD(d []byte) (Timing, bool) {
	clk := (int(d[0]) | int(d[1])<<8) * 10
	w := int(d[2]) | int(d[4]>>4)<<8
	hb := int(d[3]) | int(d[4]&0xf)<<8
	h := int(d[5]) | int(d[7]>>4)<<8
	vb := int(d[6]) | int(d[7]&0xf)<<8
	hfp := int(d[8]) | int(d[11]>>6&3)<<8
	hsw := int(d[9]) | int(d[11]>>4&3)<<8
	vfp := int(d[10]>>4) | int(d[11]>>2&3)<<4
	vsw := int(d[10]&0xf) | int(d[11]&3)<<4
	if w < 64 || h < 64 || hb == 0 || vb == 0 || d[17]&0x80 != 0 {
		return Timing{}, false // tiny, broken or interlaced: the kernel skips these too
	}
	t := Timing{
		Mode:          Mode{W: w, H: h},
		ClockKHz:      clk,
		HFront:        hfp,
		HSync:         hsw,
		HBack:         hb - hfp - hsw,
		VFront:        vfp,
		VSync:         vsw,
		VBack:         vb - vfp - vsw,
		HSyncPositive: d[17]&0x02 != 0,
		VSyncPositive: d[17]&0x04 != 0,
	}
	t.Refresh = roundRefresh(t)
	return t, true
}

// roundRefresh matches the kernel's drm_mode_vrefresh (nearest Hz).
func roundRefresh(t Timing) int {
	den := int64(t.HTotal()) * int64(t.VTotal())
	if den == 0 {
		return 0
	}
	return int((int64(t.ClockKHz)*1000 + den/2) / den)
}

func decodeRange(d []byte) *RangeLimits {
	r := &RangeLimits{
		MinV: int(d[5]), MaxV: int(d[6]),
		MinHKHz: int(d[7]), MaxHKHz: int(d[8]),
		MaxClockMHz: int(d[9]) * 10,
	}
	if d[4]&0x02 != 0 {
		r.MaxV += 255
		if d[4]&0x01 != 0 {
			r.MinV += 255
		}
	}
	if d[4]&0x08 != 0 {
		r.MaxHKHz += 255
		if d[4]&0x04 != 0 {
			r.MinHKHz += 255
		}
	}
	return r
}

func decodeCTA(blk []byte, info *Info) {
	d := int(blk[2])
	if d < 4 || d > 127 {
		d = 4
	}
	// Data block collection: bytes 4 .. d-1.
	for i := 4; i < d; {
		tag, l := blk[i]>>5, int(blk[i]&0x1f)
		if i+1+l > d {
			break
		}
		payload := blk[i+1 : i+1+l]
		if tag == 7 && l >= 1 {
			switch payload[0] {
			case 0x05:
				if l >= 2 {
					info.Colorimetry = decodeColorimetry(payload[1])
				}
			case 0x06:
				info.HDR = decodeHDR(payload[1:])
			}
		}
		i += 1 + l
	}
	for off := d; d > 4 && off+dtdLen <= 127; off += dtdLen {
		dd := blk[off : off+dtdLen]
		if dd[0] == 0 && dd[1] == 0 {
			break
		}
		if t, ok := decodeDTD(dd); ok {
			info.Timings = append(info.Timings, DecodedTiming{Timing: t, Source: "cta"})
		}
	}
}

func decodeColorimetry(b byte) []string {
	names := []string{"xvYCC601", "xvYCC709", "sYCC601", "opYCC601", "opRGB", "BT2020cYCC", "BT2020YCC", "BT2020RGB"}
	var out []string
	for bit := 7; bit >= 0; bit-- {
		if b&(1<<bit) != 0 {
			out = append(out, names[bit])
		}
	}
	return out
}

func decodeHDR(p []byte) *HDRStatic {
	h := &HDRStatic{}
	if len(p) < 2 {
		return h
	}
	for bit, name := range []string{"SDR", "HDR", "ST2084", "HLG"} {
		if p[0]&(1<<bit) != 0 {
			h.EOTFs = append(h.EOTFs, name)
		}
	}
	lum := func(cv byte) float64 { return 50 * math.Pow(2, float64(cv)/32) }
	if len(p) >= 3 && p[2] != 0 {
		h.MaxNits = lum(p[2])
	}
	if len(p) >= 4 && p[3] != 0 {
		h.MaxFALLNits = lum(p[3])
	}
	if len(p) >= 5 && h.MaxNits > 0 {
		cv := float64(p[4]) / 255
		h.MinNits = h.MaxNits * cv * cv / 100
	}
	return h
}

// decodeDisplayID reads one DisplayID section embedded in an EDID
// extension, validating it the way the kernel does (drm_displayid.c).
func decodeDisplayID(blk []byte, info *Info) error {
	payload := int(blk[2])
	if 5+payload+1 > 127 {
		return errors.New("DisplayID section overflows its block")
	}
	if checksum(blk[1:5+payload]) != blk[5+payload] {
		return errors.New("DisplayID section checksum mismatch")
	}
	for i := 5; i+3 <= 5+payload; {
		tag, n := blk[i], int(blk[i+2])
		if i+3+n > 5+payload {
			break
		}
		body := blk[i+3 : i+3+n]
		if (tag == 0x03 || tag == 0x22) && n%didTimingLen == 0 {
			for j := 0; j < n; j += didTimingLen {
				t := decodeDisplayIDTiming(body[j:j+didTimingLen], tag == 0x22)
				info.Timings = append(info.Timings, DecodedTiming{
					Timing: t, Source: "displayid", Preferred: body[j+3]&0x80 != 0,
				})
			}
		}
		i += 3 + n
	}
	return nil
}

func decodeDisplayIDTiming(d []byte, type7 bool) Timing {
	le16 := func(i int) int { return int(d[i]) | int(d[i+1])<<8 }
	clk := (int(d[0]) | int(d[1])<<8 | int(d[2])<<16) + 1
	if !type7 {
		clk *= 10 // Type I counts 10 kHz units, Type VII 1 kHz
	}
	hb, hfp, hsw := le16(6)+1, le16(8)&0x7fff+1, le16(10)+1
	vb, vfp, vsw := le16(14)+1, le16(16)&0x7fff+1, le16(18)+1
	t := Timing{
		Mode:          Mode{W: le16(4) + 1, H: le16(12) + 1},
		ClockKHz:      clk,
		HFront:        hfp,
		HSync:         hsw,
		HBack:         hb - hfp - hsw,
		VFront:        vfp,
		VSync:         vsw,
		VBack:         vb - vfp - vsw,
		HSyncPositive: le16(8)&0x8000 != 0,
		VSyncPositive: le16(16)&0x8000 != 0,
	}
	t.Refresh = roundRefresh(t)
	return t
}

// Format renders Info the way `vos edid decode` prints it.
func (i *Info) Format() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %q (product %d), EDID %s, %d blocks\n", i.PNP, i.Name, i.Product, i.Version, i.Blocks)
	if r := i.Range; r != nil {
		fmt.Fprintf(&sb, "range: %d-%d Hz, %d-%d kHz, max %d MHz\n", r.MinV, r.MaxV, r.MinHKHz, r.MaxHKHz, r.MaxClockMHz)
	}
	if h := i.HDR; h != nil {
		fmt.Fprintf(&sb, "hdr: %s, max %.0f nits, frame-average %.0f nits, min %.4f nits\n",
			strings.Join(h.EOTFs, " "), h.MaxNits, h.MaxFALLNits, h.MinNits)
	}
	if len(i.Colorimetry) > 0 {
		fmt.Fprintf(&sb, "colorimetry: %s\n", strings.Join(i.Colorimetry, " "))
	}
	ts := append([]DecodedTiming(nil), i.Timings...)
	sort.SliceStable(ts, func(a, b int) bool {
		if ts[a].Preferred != ts[b].Preferred {
			return ts[a].Preferred
		}
		if ts[a].Area() != ts[b].Area() {
			return ts[a].Area() > ts[b].Area()
		}
		return ts[a].Refresh > ts[b].Refresh
	})
	fmt.Fprintf(&sb, "%d modes:\n", len(ts))
	for _, t := range ts {
		pref := ""
		if t.Preferred {
			pref = "  preferred"
		}
		fmt.Fprintf(&sb, "  %-14s %8.3f MHz  %9.4f Hz  h %d+%d+%d  v %d+%d+%d  %s%s\n",
			t.Mode, float64(t.ClockKHz)/1000, t.ExactRefresh(),
			t.HFront, t.HSync, t.HBack, t.VFront, t.VSync, t.VBack, t.Source, pref)
	}
	return sb.String()
}
