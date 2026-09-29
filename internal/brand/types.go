package brand

import "image/color"

// State is one word of the shared state vocabulary: data-state on the web,
// tone on the TV.
type State string

// The seven states, plus Neutral ("") for unknown or not yet known.
const (
	Ready         State = "ready"
	Streaming     State = "streaming"
	Updating      State = "updating"
	RestartNeeded State = "restart-needed"
	Asleep        State = "asleep"
	Fault         State = "fault"
	Installing    State = "installing"
	Neutral       State = ""
)

// States lists the vocabulary in its canonical order (Neutral excluded).
var States = []State{Ready, Streaming, Updating, RestartNeeded, Asleep, Fault, Installing}

// ParseState maps a string to a State. Anything outside the vocabulary is
// Neutral and false, so a surface can render neutral for an unknown tone.
func ParseState(s string) (State, bool) {
	for _, st := range States {
		if string(st) == s {
			return st, true
		}
	}
	return Neutral, s == ""
}

// Style is how a state looks on every surface.
type Style struct {
	Label   string      // chip text, sentence case
	Color   color.NRGBA // the signal colour
	Ink     color.NRGBA // text on Color
	Soft    color.NRGBA // tint for large areas
	Motion  string      // motion pattern (the direction's CSS draws it)
	Reduced string      // pattern under reduced motion: "steady" or "none"
	Signage string      // the HUD word, e.g. "restart needed"

	Heat    float64 // position on the heat scale, 0..1
	Cut     string  // display face: "cold", "warm" or "hot"
	From    float64 // scaleX the state word stretches from
	ColdMap bool    // drawn with the cold map, below the scale (fault)
	Reach   float64 // TV field: how far heat spreads from the QR card, reference px
	Peak    float64 // TV field: heat just outside the card, 0..1
}

// StyleOf returns the style for s; unknown states get the neutral style.
func StyleOf(s State) Style {
	if st, ok := stateStyles[s]; ok {
		return st
	}
	return neutralStyle
}

// TVText is one role of the TV's type scale, in reference pixels
// (1920×1080 landscape, 1080×1920 portrait).
type TVText struct {
	Font       string // "display", "ui" or "mono"
	Face       string // a face of that font; "state" means the state's cut
	Px         int
	PortraitPx int // 0: same as Px
	Tracking   float64
	Upper      bool
}

// TVTypes is the TV's type scale.
type TVTypes struct {
	Wordmark, WordmarkAccent, Version, Scale, ScaleLabel, Status,
	Detail, URL, IP, IPPrefix, CodeLabel, Code, Caption TVText
}

// TVLabelSet is the copy the TV renderer owns.
type TVLabelSet struct {
	CodeLabel, QRCaption, VersionPrefix, InstallerPrefix, Starting, ScaleCold, ScaleHot string
}

// TVQRCard sizes the white-hot QR card in reference pixels.
type TVQRCard struct {
	Landscape, Portrait, QuietModules int
}

// TVLookSet names the TV look and its numeric parameters.
type TVLookSet struct {
	Background, Signature, QRFrame string
	Params                         map[string]float64
}

// Face is one static instance of a font family.
type Face struct {
	Wdth, Wght int
}

// FontRole is a font family with its fallbacks and faces.
type FontRole struct {
	Family   string
	Fallback []string
	Features string
	Faces    map[string]Face
}

// nrgba unpacks 0xRRGGBBAA; the generated tables use it to stay readable.
func nrgba(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}
}
