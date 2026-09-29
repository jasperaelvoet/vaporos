package brand

import (
	"encoding/xml"
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestHandshakeVectors(t *testing.T) {
	var vec struct {
		Cases []struct {
			Hostname, Key, Fnv1a32 string
			Rows                   []string
			On, Ground             int
			OnHex, GroundHex       string
		}
	}
	readVectors(t, "handshake-vectors.json", &vec)
	tf, _ := loadTokens(t)
	for _, c := range vec.Cases {
		h := HandshakeFor(c.Hostname)
		if h.Key != c.Key || fmt.Sprintf("%08x", h.Hash) != c.Fnv1a32 || !slices.Equal(h.Rows(), c.Rows) ||
			h.OnIndex != c.On || h.GroundIndex != c.Ground {
			t.Errorf("HandshakeFor(%q) = key %q hash %08x rows %v on %d ground %d; want key %q hash %s rows %v on %d ground %d",
				c.Hostname, h.Key, h.Hash, h.Rows(), h.OnIndex, h.GroundIndex, c.Key, c.Fnv1a32, c.Rows, c.On, c.Ground)
		}
		// The colours follow the lists in tokens.json; after a palette
		// change, update onHex and groundHex here.
		if hexString(h.On) != c.OnHex || hexString(h.Ground) != c.GroundHex {
			t.Errorf("HandshakeFor(%q) colours %s on %s, vectors say %s on %s (tokens.json handshake lists changed?)",
				c.Hostname, hexString(h.On), hexString(h.Ground), c.OnHex, c.GroundHex)
		}
	}
	if len(handshakeOn) != len(tf.Handshake.On) || len(handshakeGround) != len(tf.Handshake.Ground) {
		t.Errorf("tokens_gen.go handshake lists are stale — run %s", regenerate)
	}
}

// TestHandshakeShape checks every mark, not just the vectors: mirrored, 4 to
// 11 free cells on, and an SVG a template can inline under the CSP.
func TestHandshakeShape(t *testing.T) {
	for i := range 2000 {
		host := "host-" + strconv.Itoa(i)
		h := HandshakeFor(host)
		n := 0
		for r := range 5 {
			for c := range 5 {
				if h.Cells[r][c] != h.Cells[r][4-c] {
					t.Fatalf("%s: row %d is not mirrored", host, r)
				}
				if c < 3 && h.Cells[r][c] {
					n++
				}
			}
		}
		if n < 4 || n > 11 {
			t.Fatalf("%s: %d of 15 free cells on, want 4..11", host, n)
		}
		if h != HandshakeFor(strings.ToUpper(host)+".local") {
			t.Fatalf("%s: case or .local changes the mark", host)
		}
	}
	svg := HandshakeFor("vapor").SVG()
	if err := xml.Unmarshal([]byte(svg), new(struct{})); err != nil {
		t.Fatalf("SVG does not parse: %v\n%s", err, svg)
	}
	for _, bad := range []string{" id=", " class=", " style=", "<script", " on"} {
		if strings.Contains(svg, bad) {
			t.Errorf("SVG contains %q: %s", bad, svg)
		}
	}
	// Every on cell is painted exactly once: count the unit squares in the path.
	_, d, _ := strings.Cut(svg, ` d="`)
	d, _, _ = strings.Cut(d, `"`)
	area := 0
	for _, seg := range strings.Split(d, "M")[1:] {
		var x, y, w int
		if _, err := fmt.Sscanf(seg, "%d %dh%d", &x, &y, &w); err == nil {
			area += w
		}
	}
	on := 0
	for _, row := range HandshakeFor("vapor").Cells {
		for _, c := range row {
			if c {
				on++
			}
		}
	}
	if area != on {
		t.Errorf("SVG paints %d cells, the mark has %d", area, on)
	}
	if bits.OnesCount32(balanceMask) != 7 {
		t.Error("balanceMask must keep 7 of the 15 bits so a balanced pattern lands in 4..11")
	}
}
