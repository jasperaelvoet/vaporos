package brand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// repoRoot is where the generator reads design/ and writes its outputs.
var repoRoot = filepath.Join("..", "..")

const tokensPath = "design/tokens.json"

// tokensFile is design/tokens.json, schema 1 (design/README.md).
type tokensFile struct {
	Schema     int              `json:"schema"`
	Direction  string           `json:"direction"`
	Targets    []string         `json:"targets"`
	Modes      modesTok         `json:"modes"`
	Theme      themeTok         `json:"theme"`
	App        appTok           `json:"app"`
	Palette    omap[paletteTok] `json:"palette"`
	Ramp       omap[rampTok]    `json:"ramp"`
	Color      omap[modeVal]    `json:"color"`
	State      omap[stateTok]   `json:"state"`
	Neutral    stateTok         `json:"neutral"`
	Attention  omap[stateTok]   `json:"attention"`
	Contrast   []contrastRule   `json:"contrast"`
	Font       omap[fontTok]    `json:"font"`
	Text       omap[textTok]    `json:"text"`
	Radius     omap[string]     `json:"radius"`
	Spacing    spacingTok       `json:"spacing"`
	Breakpoint omap[string]     `json:"breakpoint"`
	Motion     motionTok        `json:"motion"`
	Filter     filterTok        `json:"filter"`
	Handshake  handshakeTok     `json:"handshake"`
	TV         tvTok            `json:"tv"`
	Labels     labelsTok        `json:"labels"`
}

type modesTok struct {
	Web  []string `json:"web"`
	Site []string `json:"site"`
	TV   []string `json:"tv"`
}

type themeTok struct {
	Default string `json:"default"`
	Switch  bool   `json:"switch"`
}

type appTok struct {
	Name        string `json:"name"`
	ShortName   string `json:"shortName"`
	Description string `json:"description"`
}

type paletteTok struct {
	Hex string `json:"hex"`
	Use string `json:"use"`
}

type rampTok struct {
	Prefix string   `json:"prefix"`
	Use    string   `json:"use"`
	Stops  []string `json:"stops"`
}

// modeVal is a colour per mode: a palette or ramp name, or #rrggbb[aa].
type modeVal struct {
	Dark  string `json:"dark"`
	Light string `json:"light,omitempty"`
	TV    string `json:"tv,omitempty"`
}

type stateTok struct {
	Label   string  `json:"label"`
	Color   modeVal `json:"color"`
	Ink     modeVal `json:"ink"`
	Soft    modeVal `json:"soft"`
	Motion  string  `json:"motion"`
	Reduced string  `json:"reduced"`
	Signage string  `json:"signage"`
	Look    lookTok `json:"look"`
}

type lookTok struct {
	Heat  float64 `json:"heat"`
	Cut   string  `json:"cut"`
	From  float64 `json:"from"`
	Map   string  `json:"map"`
	Reach float64 `json:"reach"`
	Peak  float64 `json:"peak"`
}

type contrastRule struct {
	FG  []string `json:"fg"`
	BG  []string `json:"bg"`
	Min float64  `json:"min"`
	Why string   `json:"why"`
}

type fontTok struct {
	Family   string        `json:"family"`
	Fallback []string      `json:"fallback"`
	Features string        `json:"features,omitempty"`
	Optional bool          `json:"optional,omitempty"`
	Faces    omap[faceTok] `json:"faces"`
}

// faceTok is one static cut. F3 adds web (a WOFF2 under
// internal/web/static/fonts/) and tv (a TTF in internal/display/welcome/fonts/).
type faceTok struct {
	Wdth    int    `json:"wdth"`
	Wght    int    `json:"wght"`
	Web     string `json:"web,omitempty"`
	TV      string `json:"tv,omitempty"`
	Preload bool   `json:"preload,omitempty"`
}

type textTok struct {
	Size     string `json:"size"`
	Leading  string `json:"leading"`
	Tracking string `json:"tracking,omitempty"`
}

type spacingTok struct {
	Base  string       `json:"base"`
	Named omap[string] `json:"named"`
}

type motionTok struct {
	Duration omap[int]        `json:"duration"`
	Ease     omap[string]     `json:"ease"`
	Spring   omap[springTok]  `json:"spring"`
	Pattern  omap[patternTok] `json:"pattern"`
	Hold     holdTok          `json:"hold"`
	Scene    sceneTok         `json:"scene"`
}

type springTok struct {
	Zeta float64 `json:"zeta"`
	Hz   float64 `json:"hz"`
}

type patternTok struct {
	PeriodMs int     `json:"periodMs,omitempty"`
	StepMs   int     `json:"stepMs,omitempty"`
	Hz       float64 `json:"hz,omitempty"`
}

type holdTok struct {
	Ms        int `json:"ms"`
	ReleaseMs int `json:"releaseMs"`
	FireMs    int `json:"fireMs"`
	NudgeMs   int `json:"nudgeMs"`
}

type sceneTok struct {
	CoolMs int `json:"coolMs"`
	HeatMs int `json:"heatMs"`
}

type filterTok struct {
	Turbulence struct {
		BaseFrequency string  `json:"baseFrequency"`
		Octaves       int     `json:"octaves"`
		Seed          int     `json:"seed"`
		Scale         float64 `json:"scale"`
		Blur          float64 `json:"blur"`
	} `json:"turbulence"`
	Maps omap[filterMapTok] `json:"maps"`
}

type filterMapTok struct {
	Ramp  string  `json:"ramp"`
	Bands int     `json:"bands"`
	Sub   int     `json:"sub"`
	Line  float64 `json:"line"`
}

type handshakeTok struct {
	On     []string `json:"on"`
	Ground []string `json:"ground"`
}

type tvTok struct {
	Reference struct {
		Landscape [2]int `json:"landscape"`
		Portrait  [2]int `json:"portrait"`
	} `json:"reference"`
	SafeInset float64   `json:"safeInset"`
	Type      tvTypeTok `json:"type"`
	QR        struct {
		Card struct {
			Landscape int `json:"landscape"`
			Portrait  int `json:"portrait"`
		} `json:"card"`
		QuietModules int    `json:"quietModules"`
		Dark         string `json:"dark"`
		Light        string `json:"light"`
	} `json:"qr"`
	Look struct {
		Background string        `json:"background"`
		Signature  string        `json:"signature"`
		QRFrame    string        `json:"qrFrame"`
		Params     omap[float64] `json:"params"`
	} `json:"look"`
}

type tvTextTok struct {
	Font       string  `json:"font"`
	Face       string  `json:"face"`
	Px         int     `json:"px"`
	PortraitPx int     `json:"portraitPx,omitempty"`
	Tracking   float64 `json:"tracking,omitempty"`
	Upper      bool    `json:"upper,omitempty"`
}

type tvTypeTok struct {
	Wordmark       tvTextTok `json:"wordmark"`
	WordmarkAccent tvTextTok `json:"wordmarkAccent"`
	Version        tvTextTok `json:"version"`
	Scale          tvTextTok `json:"scale"`
	ScaleLabel     tvTextTok `json:"scaleLabel"`
	Status         tvTextTok `json:"status"`
	Detail         tvTextTok `json:"detail"`
	URL            tvTextTok `json:"url"`
	IP             tvTextTok `json:"ip"`
	IPPrefix       tvTextTok `json:"ipPrefix"`
	CodeLabel      tvTextTok `json:"codeLabel"`
	Code           tvTextTok `json:"code"`
	Caption        tvTextTok `json:"caption"`
}

// roles lists the TV type roles in order, with the Go field each one fills.
func (t *tvTypeTok) roles() []struct {
	key, field string
	v          *tvTextTok
} {
	type r = struct {
		key, field string
		v          *tvTextTok
	}
	return []r{
		{"wordmark", "Wordmark", &t.Wordmark}, {"wordmarkAccent", "WordmarkAccent", &t.WordmarkAccent},
		{"version", "Version", &t.Version}, {"scale", "Scale", &t.Scale}, {"scaleLabel", "ScaleLabel", &t.ScaleLabel},
		{"status", "Status", &t.Status}, {"detail", "Detail", &t.Detail}, {"url", "URL", &t.URL},
		{"ip", "IP", &t.IP}, {"ipPrefix", "IPPrefix", &t.IPPrefix}, {"codeLabel", "CodeLabel", &t.CodeLabel},
		{"code", "Code", &t.Code}, {"caption", "Caption", &t.Caption},
	}
}

type labelsTok struct {
	TV labelsTV `json:"tv"`
}

type labelsTV struct {
	CodeLabel       string `json:"codeLabel"`
	QRCaption       string `json:"qrCaption"`
	VersionPrefix   string `json:"versionPrefix"`
	InstallerPrefix string `json:"installerPrefix"`
	Starting        string `json:"starting"`
	ScaleCold       string `json:"scaleCold"`
	ScaleHot        string `json:"scaleHot"`
}

// omap is a JSON object that keeps its key order (outputs follow the order
// the designer wrote) and refuses duplicate keys.
type omap[V any] struct {
	Keys []string
	Vals map[string]V
}

func (m *omap[V]) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("want a JSON object")
	}
	m.Keys, m.Vals = nil, map[string]V{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k := tok.(string)
		if _, dup := m.Vals[k]; dup {
			return fmt.Errorf("duplicate key %q", k)
		}
		var v V
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		m.Keys = append(m.Keys, k)
		m.Vals[k] = v
	}
	_, err := dec.Token()
	return err
}

func (m *omap[V]) get(k string) (V, bool) {
	v, ok := m.Vals[k]
	return v, ok
}

// loadTokens reads and strictly decodes design/tokens.json.
func loadTokens(t testing.TB) (*tokensFile, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, tokensPath))
	if err != nil {
		t.Fatal(err)
	}
	tf, err := decodeTokens(raw)
	if err != nil {
		t.Fatalf("%s: %v", tokensPath, err)
	}
	return tf, raw
}

func decodeTokens(raw []byte) (*tokensFile, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var tf tokensFile
	if err := dec.Decode(&tf); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the object")
	}
	return &tf, nil
}

var (
	hexRe      = regexp.MustCompile(`^#[0-9a-f]{6}([0-9a-f]{2})?$`)
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	lengthRe   = regexp.MustCompile(`^(0|[0-9]*\.?[0-9]+(rem|px))$`)
	sizeRe     = regexp.MustCompile(`^([0-9]*\.?[0-9]+rem|clamp\([^;{}]+\))$`)
	leadingRe  = regexp.MustCompile(`^([0-9]*\.?[0-9]+(rem)?)$`)
	trackingRe = regexp.MustCompile(`^-?[0-9]*\.?[0-9]+em$`)
	bezierRe   = regexp.MustCompile(`^cubic-bezier\((-?[0-9]*\.?[0-9]+,\s*){3}-?[0-9]*\.?[0-9]+\)$`)
	linearRe   = regexp.MustCompile(`^linear\((-?[0-9]*\.?[0-9]+)(,\s*-?[0-9]*\.?[0-9]+)+\)$`)
	scaleRe    = regexp.MustCompile(`^[0-9]x[sl]$`) // Tailwind's 2xs, 2xl, 3xl
)

var (
	directions  = []string{"draft", "field-unit", "on-air", "sodium", "redline"}
	knownTarget = []string{"tv", "web", "web-icons", "site"}
	stateOrder  = []string{"ready", "streaming", "updating", "restart-needed", "asleep", "fault", "installing"}
	colorRoles  = []string{"canvas", "canvas-2", "surface", "surface-2", "line", "line-strong", "ink", "ink-2", "ink-3",
		"accent", "accent-ink", "focus", "danger", "danger-ink"}
	fontRoles = []string{"display", "ui", "mono"}
)

func report(t *testing.T, errs []error) {
	t.Helper()
	for _, err := range errs {
		t.Error(err)
	}
}
