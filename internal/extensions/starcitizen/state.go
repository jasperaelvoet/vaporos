package starcitizen

import (
	"path/filepath"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// state is what Install did, in the extension's system data area (root's,
// so vosd trusts it): data/star-citizen/state.json.
type state struct {
	Disk      string `json:"disk"`      // the disk setting it used, normalized
	Prefix    string `json:"prefix"`    // the Proton prefix it made; its place is the one placeOf finds
	UUID      string `json:"uuid"`      // the filesystem the prefix is on, as its marker holds it
	Installer string `json:"installer"` // the setup in <prefix>/installer/, once it is there
	Version   string `json:"version"`   // the launcher version the setup installs
}

func statePath(dataDir string) string { return filepath.Join(dataDir, "state.json") }

// readState returns the state Install wrote, if it wrote one that names a
// place it uses.
func readState(dataDir string) (state, bool) {
	var st state
	if err := config.ReadJSON(statePath(dataDir), &st); err != nil {
		return state{}, false
	}
	if _, ok := placeOf(st.Prefix); !ok {
		return state{}, false
	}
	if st.Installer != "" && !installerRe.MatchString(st.Installer) {
		st.Installer = ""
	}
	return st, true
}

func writeState(dataDir string, st state) error {
	return config.WriteJSONAtomic(statePath(dataDir), st, 0o644)
}
