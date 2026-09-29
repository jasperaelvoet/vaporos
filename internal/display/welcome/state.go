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
}

// Placeholder is shown until vosd has written the state file.
var Placeholder = State{Title: "VaporOS", Status: "Starting…"}

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
