package display

// Interface scaling per device (docs/CONTRACTS.md, Display policy,
// Scaling): what kind of screen a Moonlight client shows the stream on, and
// the Steam scale and game DPI that make Steam's interface a sensible
// physical size there. Everything here is pure; the scaler applies it.

import (
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// Kind is the kind of screen a device streams to.
type Kind string

const (
	KindPhone    Kind = "phone"
	KindHandheld Kind = "handheld"
	KindTablet   Kind = "tablet"
	KindLaptop   Kind = "laptop"
	KindMonitor  Kind = "monitor"
	KindTV       Kind = "tv"
	KindUnknown  Kind = "unknown"
)

// Source is the signal a screen's kind came from (kind_from).
type Source string

const (
	FromYou        Source = "you"
	FromName       Source = "name"
	FromBrowser    Source = "browser"
	FromResolution Source = "resolution"
	FromHistory    Source = "history"
	FromStream     Source = "stream"
	FromDefault    Source = "default"
)

// kindTable and the constants below are every number of the scale and DPI
// math. lines is L, the interface height in gamepadui CSS lines a kind
// should get; maxH the tallest stream a device of the kind can show on its
// own panel (0: no limit), above which it is docked to something bigger.
var kindTable = map[Kind]struct{ lines, maxH int }{
	KindPhone:    {480, 1440},
	KindHandheld: {533, 1600}, // the Steam Deck: 1280x800 at 1.5
	KindTablet:   {880, 2064},
	KindLaptop:   {1000, 0},
	KindMonitor:  {1200, 0},
	KindTV:       {844, 0}, // Valve's own TV density
	KindUnknown:  {844, 0}, // never smaller than Steam's own without evidence
}

const (
	// Steam's slider bounds are 0.5·r and 2.3961·r with
	// r = sqrt(W·H/scaleRefArea); vosd also keeps what it infers, H/L_eff,
	// within H/scaleMaxLines..H/scaleMinLines. Hundredths and
	// ten-thousandths keep the math exact.
	scaleRefArea   = 1024000
	scaleMinR100   = 50    // 0.5, in hundredths
	scaleMaxR10000 = 23961 // 2.3961, in ten-thousandths
	scaleMaxLines  = 1350
	scaleMinLines  = 400
	scaleStep100   = 5 // Steam's scale is rounded to 0.05
	// steamAutoRefArea: Steam's own automatic scale on the large display
	// VaporOS's EDID claims is sqrt(W·H/steamAutoRefArea), a virtual
	// 1500x844 on a 16:9 mode.
	steamAutoRefArea = 1266000
	// Game DPI: g = min(s/1.2, H/720), floored to a quarter, at least 1.
	dpiScaleDiv100 = 120
	dpiMinLines    = 720
	dpiBase        = 96
	// Size, the user's factor on VaporOS's own scale.
	SizeMin = 0.4
	SizeMax = 2.5
	// maxScaleDim bounds the modes the integer math takes (it stays well
	// inside int64 below it).
	maxScaleDim = 16384
)

// Valid reports whether k is one of the kinds.
func (k Kind) Valid() bool {
	_, ok := kindTable[k]
	return ok
}

// UserKind parses a kind a person can pick (phone, handheld, tablet,
// laptop, monitor or tv; not unknown).
func UserKind(s string) (Kind, bool) {
	k := Kind(s)
	return k, k != KindUnknown && k.Valid()
}

// GenericName reports whether a client name says nothing about the device:
// Moonlight pairs as "roth" unless a name is typed.
func GenericName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "roth", "moonlight", "unknown":
		return true
	}
	return false
}

// Signals is what a session knows about its device.
type Signals struct {
	// You is the user's pick for the screen ("" for none).
	You Kind
	// Name is the client's name as Sunshine has it.
	Name string
	// Hint is the device's browser hint, already matched and within its
	// lifetime (nil for none). A hint picked at pairing (You) counts as the
	// user's pick when You is empty.
	Hint *Hint
	// Mode is the mode the client asked for; its refresh is the stream's fps.
	Mode edid.Mode
	// History is the screen's earlier modes, newest first.
	History []edid.Mode
	// Audio is the session's audio configuration: "2.0", "5.1" or "7.1".
	Audio string
}

// Inference is a screen's kind for one session.
type Inference struct {
	// Kind and From are what the session uses: the user's pick, else the
	// guess after the veto.
	Kind Kind
	From Source
	// Guess and GuessFrom are the first signal without the user's pick and
	// before the veto: what screens.json keeps as guess and guess_from.
	Guess     Kind
	GuessFrom Source
	// Vetoed: Kind is the veto's tv, for this session only; no edit of the
	// size pins it as the user's.
	Vetoed bool
}

// InferKind works out a screen's kind, strongest signal first (you, name,
// browser, resolution, history, stream, default), then vetoes a phone,
// handheld or tablet whose stream is taller than its panel can be.
func InferKind(sig Signals) Inference {
	g, gf := guessKind(sig)
	inf := Inference{Kind: g, From: gf, Guess: g, GuessFrom: gf}
	switch {
	case sig.You.Valid():
		inf.Kind, inf.From = sig.You, FromYou
	case sig.Hint != nil && sig.Hint.You && sig.Hint.Kind.Valid():
		inf.Kind, inf.From = sig.Hint.Kind, FromYou
	case vetoed(g, sig.Mode, sig.Hint):
		inf.Kind, inf.From, inf.Vetoed = KindTV, FromStream, true
	}
	return inf
}

func guessKind(sig Signals) (Kind, Source) {
	if !GenericName(sig.Name) {
		if k, ok := nameKind(sig.Name); ok {
			return k, FromName
		}
	}
	mk, mf, byMode := modeKind(sig)
	if h := sig.Hint; h != nil && !h.You && h.Kind.Valid() && h.Kind != KindUnknown {
		// A Windows touch laptop's browser reports the screen a Windows
		// handheld's does; a laptop's own panel among the modes tells them
		// apart. The hint keeps no User-Agent, so this holds for any
		// handheld hint.
		if h.Kind != KindHandheld || !byMode || mk != KindLaptop {
			return h.Kind, FromBrowser
		}
	}
	if byMode {
		return mk, mf
	}
	switch fps := sig.Mode.Refresh; {
	case fps >= 144:
		return KindMonitor, FromStream
	case fps == 90:
		return KindHandheld, FromStream
	case sig.Audio == "5.1" || sig.Audio == "7.1":
		return KindTV, FromStream
	}
	return KindUnknown, FromDefault
}

// modeKind is what the resolution rule says about the mode asked for, else
// about the newest mode in the history it says something about.
func modeKind(sig Signals) (Kind, Source, bool) {
	if k, ok := resolutionKind(sig.Mode); ok {
		return k, FromResolution, true
	}
	for _, md := range sig.History {
		if k, ok := resolutionKind(md); ok {
			return k, FromHistory, true
		}
	}
	return "", "", false
}

// vetoed: a phone, handheld or tablet streaming taller than its kind's
// panels go, or than its own hinted panel (but for a phone's stream in its
// panel's shape), shows the stream on a bigger screen (a docked handheld,
// a phone on a TV).
func vetoed(k Kind, md edid.Mode, h *Hint) bool {
	if k != KindPhone && k != KindHandheld && k != KindTablet {
		return false
	}
	mw, mh := landscape(md.W, md.H)
	if mh > kindTable[k].maxH {
		return true
	}
	return !hintHolds(h, mh) && !(k == KindPhone && phoneShapedFor(h, mw, mh))
}

// phoneShapedFor: a phone-shaped stream (an aspect of 1.85 or more, which
// TVs and monitors do not have short of ultrawides) no wider than the
// hinted panel (1 % slack) is that phone's own panel, taller than its
// browser renders: Android phones with a resolution switch offer Moonlight
// their full panel while the browser reports the lower one.
func phoneShapedFor(h *Hint, mw, mh int) bool {
	if h == nil || h.W <= 0 || h.H <= 0 || mh <= 0 || mw > maxScaleDim {
		return false
	}
	hl, hs := landscape(h.W, h.H)
	if hl > maxPanelDim {
		return false
	}
	return int64(mw)*100 >= int64(mh)*185 && int64(mw)*int64(hs)*100 <= int64(hl)*int64(mh)*101
}

// hintHolds: a stream mh lines tall fits the panel the hint describes (or
// the hint says nothing about its panel). CSS sizes are whole pixels, so
// the panel can be up to one CSS pixel taller than they say.
func hintHolds(h *Hint, mh int) bool {
	if h == nil || h.W <= 0 || h.H <= 0 || h.DPR <= 0 {
		return true
	}
	_, short := landscape(h.W, h.H)
	return float64(mh) <= float64(short+1)*h.DPR
}

// landscape returns the long and the short side.
func landscape(w, h int) (int, int) {
	if h > w {
		return h, w
	}
	return w, h
}

type size2 struct{ w, h int }

var (
	ultrawides = map[size2]bool{{2560, 1080}: true, {3440, 1440}: true, {3840, 1600}: true, {5120, 2160}: true, {3840, 1080}: true, {5120, 1440}: true}
	oldIPhones = map[size2]bool{{1334, 750}: true, {2208, 1242}: true}
	iPads      = map[size2]bool{{2732, 2048}: true, {2752, 2064}: true, {2388, 1668}: true, {2420, 1668}: true, {2360, 1640}: true,
		{2160, 1620}: true, {2048, 1536}: true, {2266, 1488}: true, {2224, 1668}: true}
	laptopPanels = map[size2]bool{
		// Macs, with and without the notch
		{2560, 1664}: true, {2880, 1864}: true, {2880, 1800}: true, {3024, 1964}: true, {3024, 1890}: true,
		{3456, 2234}: true, {3456, 2160}: true, {2940, 1912}: true, {3420, 2224}: true,
		// 3:2
		{2256, 1504}: true, {2880, 1920}: true, {3000, 2000}: true, {2160, 1440}: true,
	}
)

// moonlightSafeArea is how much shorter Moonlight's "Safe area" makes an
// iPad's mode.
const moonlightSafeArea = 40

// resolutionKind is what a mode alone says about the panel, if anything.
func resolutionKind(md edid.Mode) (Kind, bool) {
	w, h := landscape(md.W, md.H)
	if h <= 0 {
		return "", false
	}
	sz := size2{w, h}
	switch {
	case sz == size2{1280, 800}:
		return KindHandheld, true
	case ultrawides[sz]:
		return KindMonitor, true
	case w*100 >= h*185, oldIPhones[sz]:
		return KindPhone, true
	case iPads[sz], iPads[size2{w, h + moonlightSafeArea}]:
		return KindTablet, true
	case laptopPanels[sz]:
		return KindLaptop, true
	case sz == size2{3840, 2160}:
		return KindTV, true
	case sz == size2{2560, 1440}:
		return KindMonitor, true
	case sz == size2{1920, 1200}, sz == size2{2560, 1600}:
		// Laptops' 16:10, unless it is a fast handheld's.
		if md.Refresh >= 120 {
			return KindUnknown, true
		}
		return KindLaptop, true
	}
	return "", false
}

// nativeLooking: a mode that looks like a client's own panel rather than
// one of Moonlight's presets.
func nativeLooking(md edid.Mode) bool {
	w, h := landscape(md.W, md.H)
	if sz := (size2{w, h}); sz == (size2{2560, 1440}) || sz == (size2{3840, 2160}) {
		return false
	}
	_, ok := resolutionKind(md)
	return ok
}

// nameKeyword is one keyword of the name rule: re is anchored at its start;
// not, when set, must not match right after it; prefix keywords match only
// at the start of the name and need no boundary after them.
type nameKeyword struct {
	re, not *regexp.Regexp
	prefix  bool
}

func keywords(alts ...string) []nameKeyword {
	out := make([]nameKeyword, 0, len(alts))
	for _, a := range alts {
		kw := nameKeyword{}
		if rest, ok := strings.CutPrefix(a, "^"); ok {
			kw.prefix, a = true, rest
		}
		if a, not, ok := strings.Cut(a, "!"); ok {
			kw.re, kw.not = regexp.MustCompile("^(?:"+a+")"), regexp.MustCompile("^(?:"+not+")")
		} else {
			kw.re = regexp.MustCompile("^(?:" + a + ")")
		}
		out = append(out, kw)
	}
	return out
}

// nameRules are tried in order; "a!b" is keyword a not followed by b.
var nameRules = []struct {
	kind Kind
	kws  []nameKeyword
}{
	{KindHandheld, keywords(`steam ?deck`, `steamdeck`, `jupiter`, `galileo`, `rog ?ally`, `rc7[12]l`, `xbox ?ally`,
		`legion ?go`, `claw`, `ayaneo`, `gpd`, `onexplayer`, `retroid`, `odin`, `switch`)},
	{KindPhone, keywords(`iphone`, `pixel! ?(?:tablet|fold)`, `sm-[san]\d`, `galaxy ?[sa]\d`, `oneplus`, `xperia`, `phone`)},
	{KindTablet, keywords(`ipad`, `tablet`, `tab ?s\d`, `pixel ?tablet`, `sm-[xtp]\d`, `fold`)},
	{KindLaptop, keywords(`macbook`, `laptop`, `notebook`, `thinkpad`, `xps`, `zenbook`, `surface ?laptop`, `framework`, `chromebook`)},
	{KindMonitor, keywords(`imac`, `desktop`, `pc`, `monitor`, `mac ?mini`, `mac ?studio`)},
	{KindTV, keywords(`tv`, `shield`, `apple ?tv`, `chromecast`, `google ?tv`, `^aft`, `bravia`, `webos`, `tizen`, `fire ?tv`,
		`living ?room`, `bedroom`, `xbox`, `playstation`)},
}

// maxNameRunes bounds the work on a client name.
const maxNameRunes = 128

// nameKind finds a keyword in a client name, case aside and word-bounded: a
// keyword starts and ends where a letter meets a non-letter, the name's
// start or end, or a lower-case letter an upper-case one ("MacbookPro").
func nameKind(name string) (Kind, bool) {
	orig := []rune(name)
	if len(orig) > maxNameRunes {
		orig = orig[:maxNameRunes]
	}
	lower := make([]rune, len(orig))
	for i, r := range orig {
		lower[i] = unicode.ToLower(r)
	}
	edge := func(i int) bool {
		if i <= 0 || i >= len(orig) {
			return true
		}
		a, b := orig[i-1], orig[i]
		return !unicode.IsLetter(a) || !unicode.IsLetter(b) || (unicode.IsLower(a) && unicode.IsUpper(b))
	}
	for _, rule := range nameRules {
		for _, kw := range rule.kws {
			for start := range lower {
				if (kw.prefix && start > 0) || !edge(start) {
					continue
				}
				rest := string(lower[start:])
				loc := kw.re.FindStringIndex(rest)
				if loc == nil {
					continue
				}
				if kw.not != nil && kw.not.MatchString(rest[loc[1]:]) {
					continue
				}
				if kw.prefix || edge(start+utf8.RuneCountInString(rest[:loc[1]])) {
					return rule.kind, true
				}
			}
		}
	}
	return "", false
}

var (
	tvUA      = regexp.MustCompile(`smart-?tv|tizen|web0s|webos|bravia|\baft|crkey|google ?tv|android ?tv|shield|xbox|playstation`)
	crosUA    = regexp.MustCompile(`\bcros\b`)
	deckUA    = regexp.MustCompile(`steam ?deck|valve`)
	windowsUA = regexp.MustCompile(`windows`)
)

// ClassifyBrowser is the kind of screen a browser is on, from its
// User-Agent and what it says about its screen (CSS screen size, device
// pixel ratio and touch points); unknown when it cannot tell. POST
// /display/hint stores only this, never the User-Agent.
func ClassifyBrowser(userAgent string, w, h int, dpr float64, touch int) Kind {
	ua := strings.ToLower(userAgent)
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case tvUA.MatchString(ua):
		return KindTV
	case has("iphone"), has("android") && has("mobile"):
		return KindPhone
	case has("ipad"), has("macintosh") && touch > 1, has("android"):
		return KindTablet
	}
	long, short := landscape(w, h)
	small := short > 0 && long <= 1280 && short <= 800
	// Steam's own browser is on a Steam Deck only at the Deck's size (or
	// when nothing tells the size); on a desktop it is that desktop's.
	if deckUA.MatchString(ua) && (small || short <= 0) {
		return KindHandheld
	}
	windows, linux := windowsUA.MatchString(ua), has("linux")
	switch {
	case linux && touch > 0 && dpr >= 2 && short > 0 && long <= 1600 && short <= 1000:
		// Chrome on an Android tablet of 10" or more asks for desktop
		// sites as Linux; the Steam Deck's dpr is 1 (1.5 at most).
		return KindTablet
	case (windows || linux) && touch > 0 && small:
		return KindHandheld
	}
	if windows || linux || has("macintosh") || crosUA.MatchString(ua) {
		if dpr >= 1.5 && long > 0 && long <= 2100 {
			return KindLaptop
		}
		return KindMonitor
	}
	return KindUnknown
}

// DeviceLabel names a device after its browser for pairing ("iPhone",
// "iPad", "Mac", "Android phone", "Android tablet", "Steam Deck",
// "Windows PC", "Linux PC" or "TV"); "" when the User-Agent says too little.
// kind is the browser's hint kind when known (it tells an iPad that asks for
// desktop sites from a Mac, a Steam Deck and an Android tablet that asks
// for desktop sites from another Linux PC, and Steam's own browser on a
// Deck from the same on a desktop).
func DeviceLabel(kind Kind, userAgent string) string {
	ua := strings.ToLower(userAgent)
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case tvUA.MatchString(ua):
		return "TV"
	case has("iphone"):
		return "iPhone"
	case has("ipad"):
		return "iPad"
	case has("android") && has("mobile"):
		return "Android phone"
	case has("android"):
		return "Android tablet"
	case has("macintosh") && kind == KindTablet:
		return "iPad"
	case has("macintosh"):
		return "Mac"
	case deckUA.MatchString(ua) && (kind == KindHandheld || kind == "" || kind == KindUnknown):
		return "Steam Deck"
	case windowsUA.MatchString(ua):
		return "Windows PC"
	case has("linux") && kind == KindHandheld:
		return "Steam Deck"
	case has("linux") && kind == KindTablet:
		return "Android tablet"
	case has("linux"):
		return "Linux PC"
	}
	return ""
}

// Panel is the shape of a client's panel (any unit: only the aspect
// counts); zero when unknown.
type Panel struct{ W, H int }

// PanelFor is the shape of the client's panel: the CSS screen of its
// browser hint, else the mode it asked for when that looks native, else the
// newest native-looking mode in its history; zero when unknown. A panel
// shorter than the stream asked is some other screen's (the stream is on a
// bigger one), so it does not count.
func PanelFor(h *Hint, asked edid.Mode, history []edid.Mode) Panel {
	_, ah := landscape(asked.W, asked.H)
	if h != nil && h.W > 0 && h.H > 0 && hintHolds(h, ah) {
		return Panel{h.W, h.H}
	}
	for _, md := range append([]edid.Mode{asked}, history...) {
		if _, mh := landscape(md.W, md.H); nativeLooking(md) && mh >= ah {
			return Panel{md.W, md.H}
		}
	}
	return Panel{}
}

// kindLines is L for a kind (unknown's for anything else).
func kindLines(k Kind) int64 {
	if ks, ok := kindTable[k]; ok {
		return int64(ks.lines)
	}
	return int64(kindTable[KindUnknown].lines)
}

// maxPanelDim bounds a panel side (as POST /display/hint does).
const maxPanelDim = 20000

// letterboxed returns the panel's long and short side when the client
// letterboxes a w x h (landscape) stream on it, because the stream is wider
// than the panel: L_eff is then L × panel aspect / stream aspect.
func letterboxed(w, h int, p Panel) (int64, int64, bool) {
	pw, ph := landscape(p.W, p.H)
	if ph <= 0 || pw > maxPanelDim || int64(w)*int64(ph) <= int64(pw)*int64(h) {
		return 0, 0, false
	}
	return int64(pw), int64(ph), true
}

// sizePPM is a size factor in millionths, within SizeMin..SizeMax (1.0
// for none).
func sizePPM(size float64) int64 {
	if size == 0 || math.IsNaN(size) {
		size = 1
	}
	size = min(max(size, SizeMin), SizeMax)
	return int64(math.Floor(size*1e6 + 0.5))
}

// divRound is a/b rounded half up, for a ≥ 0 and b > 0.
func divRound(a, b int64) int64 { return (2*a + b) / (2 * b) }

// sqrtRound is sqrt(a/b) rounded half up, for a ≥ 0 and b > 0:
// floor(x + 1/2) = floor((floor(2x) + 1) / 2), and floor(sqrt(y)) =
// isqrt(floor(y)).
func sqrtRound(a, b int64) int64 { return (isqrt(4*a/b) + 1) / 2 }

func isqrt(n int64) int64 {
	x := int64(math.Sqrt(float64(n)))
	for x > 0 && x*x > n {
		x--
	}
	for (x+1)*(x+1) <= n {
		x++
	}
	return x
}

// scaleBase is H / L_eff for a kind on a landscape w x h scanout (h > 0,
// w ≤ maxScaleDim), as the fraction num/den, kept within vosd's line band
// H/scaleMaxLines..H/scaleMinLines: the scale at size 1.0 before Steam's own
// bounds. The band bounds only what vosd infers, never the user's size on
// top of it, so a size adopted from Steam gives Steam's value back.
// Letterboxed, H / L_eff = (panel h · W) / (L · panel w).
func scaleBase(k Kind, w, h int, p Panel) (num, den int64) {
	l := kindLines(k)
	num, den = int64(h), l
	if pw, ph, ok := letterboxed(w, h, p); ok {
		num, den = ph*int64(w), l*pw
	}
	switch {
	case num*scaleMaxLines < int64(h)*den:
		num, den = int64(h), scaleMaxLines
	case num*scaleMinLines > int64(h)*den:
		num, den = int64(h), scaleMinLines
	}
	return num, den
}

// steamBounds100 are Steam's slider bounds on a w x h scanout, 0.5·r and
// 2.3961·r, in hundredths.
func steamBounds100(w, h int) (lo, hi int64) {
	area := int64(w) * int64(h)
	return sqrtRound(scaleMinR100*scaleMinR100*area, scaleRefArea),
		sqrtRound(scaleMaxR10000*scaleMaxR10000*area, 10000*scaleRefArea)
}

// scale100 is Steam's scale for a kind on a scanout of w x h, in
// hundredths: s = base × size (base = H / L_eff within vosd's band),
// clamped to Steam's slider bounds, rounded half up to 0.05. 0 for an
// impossible mode.
func scale100(k Kind, w, h int, size float64, p Panel) int64 {
	w, h = landscape(w, h)
	if h <= 0 || w > maxScaleDim {
		return 0
	}
	// s100 = 100 · num/den · size, with size in millionths.
	num, den := scaleBase(k, w, h, p)
	lo, hi := steamBounds100(w, h)
	s := min(max(divRound(sizePPM(size)*num, 10000*den), lo), hi)
	return divRound(s, scaleStep100) * scaleStep100
}

// SizeRange is the sizes between which a screen's scale still moves on the
// scanout mode: at or below lo, and at or above hi, Steam's slider bounds
// hold it; both within SizeMin..SizeMax. 0, 0 for an impossible mode.
func SizeRange(k Kind, scanout edid.Mode, p Panel) (lo, hi float64) {
	w, h := landscape(scanout.W, scanout.H)
	if h <= 0 || w > maxScaleDim {
		return 0, 0
	}
	num, den := scaleBase(k, w, h, p)
	slo, shi := steamBounds100(w, h)
	size := func(s100 int64) float64 {
		return min(max(float64(s100*den)/float64(100*num), SizeMin), SizeMax)
	}
	return size(slo), size(shi)
}

// steamAutoScale is about what Steam's own automatic scale comes to on the
// scanout mode, to Steam's 0.01 and with none of vosd's bounds: what vosd
// goes by for a steam_auto screen while Steam cannot be asked. 0 for an
// impossible mode.
func steamAutoScale(scanout edid.Mode) float64 {
	w, h := landscape(scanout.W, scanout.H)
	if h <= 0 || w > maxScaleDim {
		return 0
	}
	return float64(sqrtRound(10000*int64(w)*int64(h), steamAutoRefArea)) / 100
}

// ScaleFor is Steam's interface scale for a screen of kind k on the
// scanout mode in use (portrait or landscape), with the user's size
// factor (0 for 1.0) and the client's panel shape when known.
func ScaleFor(k Kind, scanout edid.Mode, size float64, p Panel) float64 {
	return float64(scale100(k, scanout.W, scanout.H, size, p)) / 100
}

// GameDPI is the Xft.dpi games get with Steam at scale on the scanout
// mode: 96·g, g = min(scale/1.2, H/720) floored to a quarter, at least 1.
// scale is the applied value, or Steam's own automatic one.
func GameDPI(scale float64, scanout edid.Mode) int {
	_, h := landscape(scanout.W, scanout.H)
	s := int64(0)
	if scale > 0 && !math.IsInf(scale, 0) {
		s = int64(math.Floor(scale*100 + 0.5))
	}
	quarters := min(s*4/dpiScaleDiv100, int64(h)*4/dpiMinLines)
	return int(max(quarters, 4)) * dpiBase / 4
}

// AdoptSize is the size factor that reproduces value, a scale the user set
// in Steam, for a screen of kind k on the scanout mode: value / base, the
// base of ScaleFor, unrounded so that ScaleFor gives value back (to its
// 0.05), within SizeMin..SizeMax.
func AdoptSize(value float64, k Kind, scanout edid.Mode, p Panel) float64 {
	w, h := landscape(scanout.W, scanout.H)
	if h <= 0 || w > maxScaleDim || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	num, den := scaleBase(k, w, h, p)
	return min(max(value*float64(den)/float64(num), SizeMin), SizeMax)
}
