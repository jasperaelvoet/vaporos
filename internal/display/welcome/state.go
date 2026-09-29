// Package welcome draws the screen a monitor shows while VaporOS is idle:
// the address of the web UI, a QR code of it, a status line and, during
// setup, the setup code. vosd decides what to show and writes it to
// /run/vos/welcome.json; `vos welcome` (this package's Run) owns DRM master,
// lights every connected connector with a dumb buffer and redraws when the
// file or the set of connectors changes. See docs/CONTRACTS.md.
package welcome

import (
	"encoding/json"
	"os"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// State is the content of /run/vos/welcome.json.
type State struct {
	Mode     string `json:"mode"` // "os" | "installer"
	Hostname string `json:"hostname"`
	URL      string `json:"url"`    // http://vapor.local
	IPURL    string `json:"ip_url"` // http://192.168.1.50
	QR       string `json:"qr"`     // what the QR code encodes
	Code     string `json:"code"`   // setup code, "" when none is needed
	Title    string `json:"title"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Version  string `json:"version"`

	// Tone is the state word of design/tokens.json that the screen is
	// coloured by: ready, streaming, updating, restart-needed, asleep, fault
	// or installing. "" is neutral (starting, or not known yet), and a word
	// outside the vocabulary renders as neutral too. The words above carry
	// the meaning; the tone only decides how the screen looks.
	Tone brand.State `json:"tone,omitempty"`
	// Attention is "pair" while a device waits for its PIN.
	Attention string `json:"attention,omitempty"`
	// Progress is the percent of a running install or update, 0 to 100.
	// 0 draws no bar.
	Progress int `json:"progress,omitempty"`
}

// AttentionPair is the Attention value while a device waits for its PIN.
const AttentionPair = "pair"

// Placeholder is shown until vosd has written the state file. Its tone is
// neutral.
var Placeholder = State{Title: "VaporOS", Status: brand.TVLabels.Starting}

// Load reads a state file.
func Load(path string) (State, error) {
	var st State
	b, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(b, &st)
	return st, err
}
