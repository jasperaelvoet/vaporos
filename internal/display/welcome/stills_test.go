package welcome

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The website's TV stills are real renders of the welcome screen, drawn from
// the fixtures here, for the pages that picture the screen on the PC
// (website/src/lib/stills.ts). They are not committed: pages.yml writes them
// into website/public/tv before it builds the site, and a site built without
// them draws its own pictures instead.
//
//	VOS_WELCOME_STILLS="$PWD/website/public/tv" \
//	VOS_WELCOME_STILLS_QR=https://jasperaelvoet.github.io/vaporos/demo/ \
//	go test -count=1 ./internal/display/welcome -run 'TestExportStills$'
//
// On a still the QR code opens the website (VOS_WELCOME_STILLS_QR, the
// site's root when unset), not a PC, so scanning one on a screen goes
// somewhere. The stills are JPEG: the renderer's grain makes a 1280×720 PNG
// about 900 kB, six times the site's budget for a still.

// siteStills are the fixtures the website shows.
var siteStills = []string{"installer-ready", "ready", "streaming", "pairing", "installer-running", "no-gpu"}

const (
	stillW, stillH = 1280, 720
	// stillBudget keeps a still under website/scripts/budget.mjs's 150 kB,
	// with room for the bytes a render differs by between machines.
	stillBudget = 140_000
	// stillQR is the website's root, which the QR codes open by default.
	stillQR = "https://jasperaelvoet.github.io/vaporos/"
)

// stillQualities are the JPEG qualities a still tries, best first; it takes
// the first that fits the budget.
var stillQualities = []int{88, 85, 82, 80, 78, 75, 72, 70}

// siteEdits make a fixture agree with the website around it. The site is
// public, so a still carries made-up names only (the export fails on any of
// personalNames), and the Living room TV streams at the mode the site gives
// it (website/src/content/story.ts devices).
var (
	siteEdits = strings.NewReplacer(
		"Jasper’s iPhone", "Steam Deck",
		"Jasper's iPhone", "Steam Deck",
		"3840 × 2160 · 120 Hz · HDR", "3840 × 2160 · 60 Hz · HDR",
	)
	personalNames = []string{"Jasper"}
)

// stillsManifest is stills.json.
type stillsManifest struct {
	QR     string           `json:"qr"` // what every still's QR code opens
	Stills map[string]still `json:"stills"`
}

type still struct {
	File   string `json:"file"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	V      string `json:"v"` // the file's sha256, short: a cache buster for its URL
	Tone   string `json:"tone"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	URL    string `json:"url"`
	IPURL  string `json:"ip_url"`
	Code   string `json:"code,omitempty"`
	Alt    string `json:"alt"` // the picture's text alternative
}

// siteStill is fixture st as the website shows it.
func siteStill(st State, qr string) State {
	st.QR = qr
	st.Status = siteEdits.Replace(st.Status)
	st.Detail = siteEdits.Replace(st.Detail)
	return st
}

// stillAlt says what the screen shows, in the order it shows it.
func stillAlt(st State) string {
	var b strings.Builder
	b.WriteString("The screen on the PC: ")
	b.WriteString(sentence(st.Status))
	if st.Detail != "" {
		b.WriteString(" " + sentence(st.Detail))
	}
	b.WriteString(" " + st.URL)
	if st.IPURL != "" {
		b.WriteString(" or " + st.IPURL)
	}
	if st.Code != "" {
		b.WriteString(", setup code " + st.Code)
	}
	b.WriteString(", and a QR code.")
	return b.String()
}

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// encodeStill renders st as a JPEG, at the best quality that fits the budget.
func encodeStill(st State) ([]byte, error) {
	img := Render(st, stillW, stillH)
	var b bytes.Buffer
	for _, q := range stillQualities {
		b.Reset()
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if b.Len() <= stillBudget {
			return b.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("%d bytes at quality %d, over the %d byte budget", b.Len(), stillQualities[len(stillQualities)-1], stillBudget)
}

// exportStills writes every site still and stills.json into dir.
func exportStills(fx map[string]State, dir, qr string) (stillsManifest, error) {
	m := stillsManifest{QR: qr, Stills: map[string]still{}}
	if u, err := url.Parse(qr); err != nil || u.Scheme != "https" || u.Host == "" {
		return m, fmt.Errorf("the QR code's address %q is not an https URL", qr)
	}
	states := map[string]State{}
	for _, name := range siteStills {
		fixture, ok := fx[name]
		if !ok {
			return m, fmt.Errorf("%s: no such fixture", name)
		}
		st := siteStill(fixture, qr)
		s := still{
			File: name + ".jpg", W: stillW, H: stillH, Tone: string(st.Tone),
			Status: st.Status, Detail: st.Detail, URL: st.URL, IPURL: st.IPURL, Code: st.Code, Alt: stillAlt(st),
		}
		for _, who := range personalNames {
			if strings.Contains(s.Status+s.Detail+s.URL+s.IPURL+s.Code+s.Alt, who) {
				return m, fmt.Errorf("%s: names %s; the website is public (give siteEdits a made-up one)", name, who)
			}
		}
		states[name], m.Stills[name] = st, s
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m, err
	}
	for _, name := range siteStills {
		s := m.Stills[name]
		b, err := encodeStill(states[name])
		if err != nil {
			return m, fmt.Errorf("%s: %w", name, err)
		}
		sum := sha256.Sum256(b)
		s.V = hex.EncodeToString(sum[:6])
		if err := os.WriteFile(filepath.Join(dir, s.File), b, 0o644); err != nil {
			return m, err
		}
		m.Stills[name] = s
	}
	j, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	return m, os.WriteFile(filepath.Join(dir, "stills.json"), append(j, '\n'), 0o644)
}

// TestExportStills writes the website's TV stills (see the top of this file).
func TestExportStills(t *testing.T) {
	dir := os.Getenv("VOS_WELCOME_STILLS")
	if dir == "" {
		t.Skip("set VOS_WELCOME_STILLS=<dir> to write the website's TV stills")
	}
	qr := os.Getenv("VOS_WELCOME_STILLS_QR")
	if qr == "" {
		qr = stillQR
	}
	m, err := exportStills(loadFixtures(t), dir, qr)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(dir)
	t.Logf("wrote %d stills and stills.json to %s; their QR codes open %s", len(m.Stills), abs, qr)
}

// TestStills exports the stills into a scratch directory, so a change to the
// renderer or the fixtures that breaks them (a still over the site's budget,
// a real name, a fixture gone) fails here rather than in pages.yml.
func TestStills(t *testing.T) {
	dir := t.TempDir()
	const qr = stillQR + "demo/"
	m, err := exportStills(loadFixtures(t), dir, qr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "stills.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got stillsManifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("stills.json: %v", err)
	}
	if got.QR != qr || len(got.Stills) != len(siteStills) {
		t.Fatalf("stills.json: qr %q and %d stills, want %q and %d", got.QR, len(got.Stills), qr, len(siteStills))
	}
	for _, name := range siteStills {
		s := got.Stills[name]
		if s != m.Stills[name] {
			t.Errorf("%s: stills.json has %+v, the export %+v", name, s, m.Stills[name])
		}
		img, err := os.ReadFile(filepath.Join(dir, s.File))
		if err != nil {
			t.Error(err)
			continue
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(img))
		if err != nil || cfg.Width != s.W || cfg.Height != s.H || s.W != stillW || s.H != stillH {
			t.Errorf("%s: %dx%d (%v), stills.json says %dx%d, want %dx%d", s.File, cfg.Width, cfg.Height, err, s.W, s.H, stillW, stillH)
		}
		if len(img) > stillBudget {
			t.Errorf("%s: %d bytes, over the %d byte budget", s.File, len(img), stillBudget)
		}
		if sum := sha256.Sum256(img); s.V != hex.EncodeToString(sum[:6]) {
			t.Errorf("%s: v %q is not the file's hash", s.File, s.V)
		}
		if s.Status == "" || s.URL == "" || !strings.HasPrefix(s.Alt, "The screen on the PC: "+s.Status) {
			t.Errorf("%s: status %q, url %q, alt %q", name, s.Status, s.URL, s.Alt)
		}
	}
	if s := got.Stills["pairing"]; !strings.HasPrefix(s.Status, "Steam Deck ") {
		t.Errorf("pairing: status %q, want the made-up Steam Deck", s.Status)
	}
	if s := got.Stills["installer-ready"]; s.Code == "" || !strings.Contains(s.Alt, "setup code "+s.Code) {
		t.Errorf("installer-ready: code %q, alt %q", s.Code, s.Alt)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	want := []string{"stills.json"}
	for _, name := range siteStills {
		want = append(want, name+".jpg")
	}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("wrote %v, want %v", names, want)
	}
}

// A still that names a person fails, and an address that isn't https does.
func TestStillsRefuse(t *testing.T) {
	fx := loadFixtures(t)
	st := fx["ready"]
	st.Status = "Jasper’s Mac wants to pair"
	fx["ready"] = st
	if _, err := exportStills(fx, t.TempDir(), stillQR); err == nil || !strings.Contains(err.Error(), "Jasper") {
		t.Errorf("a still naming a person: %v", err)
	}
	if _, err := exportStills(loadFixtures(t), t.TempDir(), "http://192.168.1.50/"); err == nil {
		t.Error("an http address for the QR code was taken")
	}
}
