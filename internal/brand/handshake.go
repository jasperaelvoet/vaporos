package brand

import (
	"fmt"
	"hash/fnv"
	"image/color"
	"math/bits"
	"strings"
)

// Handshake is the mark the TV draws next to the setup code and the phone
// shows after the scan, so the two can be matched at a glance: a 5×5
// mirrored cell pattern in two palette colours. It comes from the hostname
// only, never from the setup code, so showing it gives nothing away.
type Handshake struct {
	Key         string // the normalised hostname the mark is derived from
	Hash        uint32 // FNV-1a 32 of Key
	Cells       [5][5]bool
	OnIndex     int // into the tokens' handshake.on colours
	GroundIndex int // into handshake.ground
	On, Ground  color.NRGBA
}

// handshakeKey normalises a hostname: lower case, no surrounding space, no
// trailing dot and no ".local", so "Vapor.local." and "vapor" match.
func handshakeKey(host string) string {
	k := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return strings.TrimSuffix(k, ".local")
}

// balanceMask is XORed into a pattern with fewer than 4 or more than 11 of
// its 15 free cells on; the result always has 4 to 11.
const balanceMask = 0x2aaa

// HandshakeFor derives the mark for a hostname. Bits 0–14 of the hash fill
// the left three columns row by row (bit r*3+c is row r, column c), which
// are mirrored onto the right two; bits 15–19 pick the on colour and bits
// 20–24 the ground colour, each modulo its list.
func HandshakeFor(host string) Handshake {
	key := handshakeKey(host)
	f := fnv.New32a()
	f.Write([]byte(key))
	h := Handshake{Key: key, Hash: f.Sum32()}
	cells := h.Hash & 0x7fff
	if n := bits.OnesCount32(cells); n < 4 || n > 11 {
		cells ^= balanceMask
	}
	for r := range 5 {
		for c := range 3 {
			on := cells>>(r*3+c)&1 == 1
			h.Cells[r][c], h.Cells[r][4-c] = on, on
		}
	}
	h.OnIndex = int(h.Hash>>15&0x1f) % len(handshakeOn)
	h.GroundIndex = int(h.Hash>>20&0x1f) % len(handshakeGround)
	h.On, h.Ground = handshakeOn[h.OnIndex], handshakeGround[h.GroundIndex]
	return h
}

// Rows draws the cells as five strings, "#" on and "." off.
func (h Handshake) Rows() []string {
	out := make([]string, 5)
	for r, row := range h.Cells {
		var b strings.Builder
		for _, on := range row {
			if on {
				b.WriteByte('#')
			} else {
				b.WriteByte('.')
			}
		}
		out[r] = b.String()
	}
	return out
}

func hexString(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// SVG draws the mark as inline SVG: a 7×7 ground with the cells one unit in
// from the edge, runs of cells merged into one rectangle each. It carries no
// id, class or style, so a template can size it with its own class.
func (h Handshake) SVG() string {
	var d strings.Builder
	for r, row := range h.Cells {
		for c := 0; c < 5; {
			if !row[c] {
				c++
				continue
			}
			run := 1
			for c+run < 5 && row[c+run] {
				run++
			}
			fmt.Fprintf(&d, "M%d %dh%dv1h-%dz", c+1, r+1, run, run)
			c += run
		}
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 7 7" shape-rendering="crispEdges" aria-hidden="true">` +
		`<rect width="7" height="7" fill="` + hexString(h.Ground) + `"/>` +
		`<path fill="` + hexString(h.On) + `" d="` + d.String() + `"/></svg>`
}
