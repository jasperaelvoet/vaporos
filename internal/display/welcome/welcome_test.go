package welcome

import (
	"encoding/json"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
	"rsc.io/qr"
)

var sample = State{
	Mode: "installer", Hostname: "vapor",
	URL: "http://vapor.local", IPURL: "http://192.168.1.50",
	QR: "http://192.168.1.50/setup?code=ABCD-EFGH", Code: "ABCD-EFGH",
	Title: "VaporOS", Status: "Ready to install",
	Detail:  "Open this address on a phone or computer to install VaporOS",
	Version: "20260929.123456",
}

func luma(c color.RGBA) int { return (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000 }

// The QR on screen must be exactly rsc.io/qr's code for the state's URL,
// module for module, so any phone camera decodes the right address.
func TestQRMatchesEncoder(t *testing.T) {
	for _, sz := range pixelSizes {
		l := computeLayout(sample, sz.X, sz.Y)
		checkQR(t, sizeName(sz), l, paint(l, true), sample.QR)
	}
}

// checkQR compares every module of the rendered code, quiet zone included,
// with rsc.io/qr's matrix for text.
func checkQR(t *testing.T, at string, l *layout, img *image.RGBA, text string) {
	t.Helper()
	want, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	if l.QR == nil || l.QR.Size != want.Size || l.Module < 3 {
		t.Fatalf("%s: layout QR %+v module %d", at, l.QR, l.Module)
	}
	if !l.QRCard.In(img.Bounds()) {
		t.Errorf("%s: QR card %v off screen", at, l.QRCard)
	}
	for y := -4; y < want.Size+4; y++ { // include the quiet zone
		for x := -4; x < want.Size+4; x++ {
			c := img.RGBAAt(l.QRAt.X+x*l.Module+l.Module/2, l.QRAt.Y+y*l.Module+l.Module/2)
			dark := luma(c) < 64
			light := luma(c) > 192
			if want.Black(x, y) != dark || want.Black(x, y) == light {
				t.Fatalf("%s: module (%d,%d) is %v, want black=%v", at, x, y, c, want.Black(x, y))
			}
		}
	}
}

// Every line must actually be drawn inside the screen and not on top of
// the QR code, and a state with a setup code shows one line of every role.
func TestTextDrawn(t *testing.T) {
	required := []role{roleWordmark, roleStatus, roleDetail, roleURL, roleIP, roleCodeLabel, roleCode, roleCaption, roleVersion}
	for _, sz := range []image.Point{{1920, 1080}, {1080, 1920}, {1280, 720}} {
		l := computeLayout(sample, sz.X, sz.Y)
		img := paint(l, true)
		for _, r := range required {
			if !slices.ContainsFunc(l.Texts, func(it textItem) bool { return it.Role == r }) {
				t.Errorf("%v: no %s line", sz, r)
			}
		}
		for _, it := range l.Texts {
			if it.Rect.Empty() || !it.Rect.In(img.Bounds()) {
				t.Errorf("%v: %q at %v outside %v", sz, it.Text, it.Rect, img.Bounds())
				continue
			}
			if it.Rect.Overlaps(l.QRCard) {
				t.Errorf("%v: %q overlaps the QR card", sz, it.Text)
			}
			// The text colour must show up inside its box.
			hits := 0
			for y := it.Rect.Min.Y; y < it.Rect.Max.Y; y++ {
				for x := it.Rect.Min.X; x < it.Rect.Max.X; x++ {
					c := img.RGBAAt(x, y)
					if abs(int(c.R)-int(it.Color.R))+abs(int(c.G)-int(it.Color.G))+abs(int(c.B)-int(it.Color.B)) < 40 {
						hits++
					}
				}
			}
			if hits < it.Rect.Dx()*it.Rect.Dy()/20 {
				t.Errorf("%v: %q: only %d text pixels in %v", sz, it.Text, hits, it.Rect)
			}
		}
	}
}

func TestLongTextFits(t *testing.T) {
	st := sample
	st.URL = "http://a-very-long-hostname-for-a-gaming-pc-in-the-living-room.local"
	st.Detail = "Some very long detail line that would never fit into the left column of the welcome screen at its normal size"
	for _, sz := range layoutSizes {
		l := computeLayout(st, sz.X, sz.Y)
		for _, it := range l.Texts {
			if it.Rect.Overlaps(l.QRCard) || !it.Rect.In(l.Safe.Intersect(l.Content)) {
				t.Errorf("%s: %q runs into the QR card %v or off the screen: %v", sizeName(sz), it.Text, l.QRCard, it.Rect)
			}
		}
	}
}

func TestNoQR(t *testing.T) {
	st := sample
	st.QR, st.Code = "", ""
	l := computeLayout(st, 1920, 1080)
	if l.QR != nil || !l.QRCard.Empty() || !l.Panel.Empty() {
		t.Errorf("layout = %+v", l)
	}
	img := Render(Placeholder, 640, 480)
	if img.Bounds().Dx() != 640 {
		t.Error("placeholder render failed")
	}
}

func TestCopyXRGB(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.SetRGBA(0, 0, color.RGBA{0x11, 0x22, 0x33, 0xff})
	img.SetRGBA(2, 1, color.RGBA{0xaa, 0xbb, 0xcc, 0xff})
	pitch := 16 // wider than 3*4, like real dumb buffers
	dst := make([]byte, pitch*2)
	copyXRGB(dst, pitch, 3, 2, img)
	if dst[0] != 0x33 || dst[1] != 0x22 || dst[2] != 0x11 || dst[3] != 0xff {
		t.Errorf("pixel (0,0) = % x", dst[0:4])
	}
	if p := dst[pitch+8 : pitch+12]; p[0] != 0xcc || p[1] != 0xbb || p[2] != 0xaa {
		t.Errorf("pixel (2,1) = % x", p)
	}
	if dst[12] != 0 || dst[pitch+12] != 0 {
		t.Error("wrote into pitch padding")
	}
	copyXRGB(make([]byte, 10), 16, 3, 2, img) // short buffer: no panic
}

func TestStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "welcome.json")
	if st, changed := stateChanged(path, State{}, false); !changed || st != Placeholder {
		t.Errorf("missing file = %+v %v", st, changed)
	}
	os.WriteFile(path, []byte(`{"status":"Ready to stream","url":"http://vapor.local"}`), 0o644)
	st, changed := stateChanged(path, Placeholder, true)
	if !changed || st.Status != "Ready to stream" {
		t.Errorf("load = %+v %v", st, changed)
	}
	if _, changed := stateChanged(path, st, true); changed {
		t.Error("unchanged file reported as changed")
	}
	os.Remove(path)
	if got, changed := stateChanged(path, st, true); changed || got != st {
		t.Error("a vanished file must keep the current screen")
	}

	// tone, attention and progress round-trip, and each alone is a change.
	full := State{Status: "Downloading update 20261003.0915", Tone: brand.Updating, Attention: AttentionPair, Progress: 42}
	b, _ := json.Marshal(full)
	os.WriteFile(path, b, 0o644)
	got, changed := stateChanged(path, st, true)
	if !changed || got != full {
		t.Errorf("round trip = %+v %v", got, changed)
	}
	for _, next := range []State{
		{Status: full.Status, Tone: brand.Fault, Attention: full.Attention, Progress: full.Progress},
		{Status: full.Status, Tone: full.Tone, Progress: full.Progress},
		{Status: full.Status, Tone: full.Tone, Attention: full.Attention, Progress: 43},
	} {
		b, _ := json.Marshal(next)
		os.WriteFile(path, b, 0o644)
		if got, changed := stateChanged(path, full, true); !changed || got != next {
			t.Errorf("%+v after %+v: changed=%v", next, full, changed)
		}
	}
	// Unset, they are left out, so a welcome.json without them is unchanged;
	// a tone outside the vocabulary still loads (and renders neutral).
	if b, _ := json.Marshal(State{Status: "Ready to stream"}); strings.Contains(string(b), "tone") || strings.Contains(string(b), "attention") || strings.Contains(string(b), "progress") {
		t.Errorf("unset fields written: %s", b)
	}
	os.WriteFile(path, []byte(`{"status":"x","tone":"bogus"}`), 0o644)
	if got, err := Load(path); err != nil || got.Tone != "bogus" {
		t.Errorf("unknown tone: %+v %v", got, err)
	}
	if Placeholder.Tone != brand.Neutral || Placeholder.Progress != 0 || Placeholder.Attention != "" {
		t.Errorf("placeholder = %+v, want neutral", Placeholder)
	}
}

func TestMainPNG(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "welcome.json")
	os.WriteFile(state, []byte(`{"status":"Ready to stream"}`), 0o644)
	out := filepath.Join(dir, "w.png")
	if code := Main([]string{"--png", out, "--size", "800x600"}, state, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatal("no PNG written")
	}
	if code := Main([]string{"--png", out, "--size", "big"}, state, nil); code != 2 {
		t.Errorf("bad size: exit %d", code)
	}
}
