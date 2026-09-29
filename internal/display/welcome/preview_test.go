package welcome

import (
	"fmt"
	"html"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// previewSizes are the default sizes of the previews: 1080p, 720p, 4K,
// portrait, ultrawide, 4:3 and a small panel.
var previewSizes = []image.Point{{1920, 1080}, {1280, 720}, {3840, 2160}, {1080, 1920}, {3440, 1440}, {1024, 768}, {800, 480}}

// crockford is the setup code's alphabet (internal/daemon/setupcode.go);
// the code face must keep 0 1 2 5 8 B S Z apart.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// TestWelcomePreviews writes every fixture at every preview size, the setup
// code alphabet and a contact sheet (index.html) for design review. Nothing
// opens a browser.
//
//	VOS_GEN_WELCOME=1 go test ./internal/display/welcome -run TestWelcomePreviews
//
// It writes to .build/welcome-preview/ (gitignored) unless VOS_WELCOME_OUT
// names another directory; VOS_WELCOME_SIZES=1920x1080,3840x2160 narrows
// the sizes.
func TestWelcomePreviews(t *testing.T) {
	if os.Getenv("VOS_GEN_WELCOME") == "" {
		t.Skip("set VOS_GEN_WELCOME=1 to write the previews")
	}
	out := os.Getenv("VOS_WELCOME_OUT")
	if out == "" {
		out = filepath.Join("..", "..", "..", ".build", "welcome-preview")
	}
	sizes := previewSizes
	if s := os.Getenv("VOS_WELCOME_SIZES"); s != "" {
		sizes = nil
		for _, f := range strings.Split(s, ",") {
			w, h, ok := strings.Cut(strings.TrimSpace(f), "x")
			wi, err1 := strconv.Atoi(w)
			hi, err2 := strconv.Atoi(h)
			if !ok || err1 != nil || err2 != nil || wi < 16 || hi < 16 {
				t.Fatalf("VOS_WELCOME_SIZES: bad size %q", f)
			}
			sizes = append(sizes, image.Pt(wi, hi))
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	fx := loadFixtures(t)

	// Rendering shares font faces, so it runs here, one at a time; PNG
	// encoding is the slow part and runs in parallel.
	type job struct {
		path string
		img  *image.RGBA
	}
	jobs := make(chan job, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := writePreview(j.path, j.img); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}
	for _, name := range fixtureNames {
		for _, sz := range sizes {
			jobs <- job{filepath.Join(out, previewFile(name, sz)), Render(fx[name], sz.X, sz.Y)}
		}
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		t.Error(err)
	}
	if err := writePreview(filepath.Join(out, "alphabet.png"), alphabetSheet(fx["installer-ready"])); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(out, "index.html"), contactSheet(fx, sizes), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(out)
	t.Logf("wrote %d previews and index.html to %s", len(fixtureNames)*len(sizes)+1, abs)
}

func previewFile(name string, sz image.Point) string { return name + "@" + sizeName(sz) + ".png" }

func writePreview(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// alphabetSheet sets the setup code alphabet, then the characters it must
// keep apart, in the code's face, size and colours at 1920×1080.
func alphabetSheet(st State) *image.RGBA {
	l := computeLayout(st, 1920, 1080)
	var code textItem
	for _, it := range l.Texts {
		if it.Role == roleCode {
			code = it
		}
	}
	pal := theLook.palette()
	lines := []string{crockford[:16], crockford[16:], "0 1 2 5 8 B S Z"}
	lh := code.Px * 3 / 2
	img := image.NewRGBA(image.Rect(0, 0, 1920, lh*len(lines)+code.Px))
	fillRounded(img, img.Bounds(), 0, pal.Surface)
	for i, s := range lines {
		drawText(img, textItem{Text: s, Font: code.Font, Px: code.Px, Color: pal.Ink, Dot: image.Pt(96, lh*(i+1))})
	}
	return img
}

// contactSheet is a static page of every preview, one row per fixture.
func contactSheet(fx map[string]State, sizes []image.Point) []byte {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Welcome screen previews</title>
<style>
body{margin:0;padding:24px;background:#111;color:#eee;font:15px/1.4 system-ui,sans-serif}
h2{margin:32px 0 4px;font-size:18px}p{margin:0 0 12px;color:#aaa}
.row{display:flex;flex-wrap:wrap;gap:16px;align-items:flex-start}
figure{margin:0}figcaption{color:#aaa;font-size:13px}
img{display:block;max-width:100%;height:auto;border:1px solid #333}
</style></head><body>
<h1>Welcome screen previews</h1>
<p>Rendered by go test ./internal/display/welcome -run TestWelcomePreviews. <a href="alphabet.png" style="color:#9cf">Setup code alphabet</a></p>
`)
	for _, name := range fixtureNames {
		st := fx[name]
		fmt.Fprintf(&b, "<h2 id=%q>%s</h2>\n<p>tone %q · %s · %s</p>\n<div class=\"row\">\n",
			name, html.EscapeString(name), st.Tone, html.EscapeString(st.Status), html.EscapeString(st.Detail))
		for _, sz := range sizes {
			w := min(sz.X/4, 480)
			fmt.Fprintf(&b, "<figure><a href=%q><img src=%q width=\"%d\" height=\"%d\" loading=\"lazy\" alt=\"%s at %s\"></a><figcaption>%s</figcaption></figure>\n",
				previewFile(name, sz), previewFile(name, sz), w, w*sz.Y/sz.X, html.EscapeString(name), sizeName(sz), sizeName(sz))
		}
		b.WriteString("</div>\n")
	}
	b.WriteString("</body></html>\n")
	return []byte(b.String())
}
