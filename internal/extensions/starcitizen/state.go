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

// dataDir is the extension's system data area, where state.json is.
func dataDir() string { return filepath.Join(config.ExtDataDir(), ID) }

// readState returns the state Install wrote, if it wrote one that names a
// place it uses and the filesystem it is on.
func readState(dataDir string) (state, bool) {
	var st state
	if err := config.ReadJSON(statePath(dataDir), &st); err != nil {
		return state{}, false
	}
	if _, ok := placeOf(st.Prefix); !ok || !uuidRe.MatchString(st.UUID) {
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

// recorded is what Install recorded, read as vapor (the launch hook,
// fetch-installer), once its prefix's files can be used (locate, with the
// recorded filesystem). prefix is the one the caller was given: it must be
// a place VaporOS uses, with a state recorded, and, unless follow, the
// recorded prefix. With follow, another place is where Star Citizen was
// before it was set up again elsewhere: Steam's shortcut names it until
// Steam picks up the new target, and the recorded prefix is the one to
// use. state.json is root's, and vapor may read it.
func recorded(prefix string, follow bool) (state, error) {
	st, ok := readState(dataDir())
	if _, known := placeOf(prefix); !ok || !known || (st.Prefix != prefix && !follow) {
		return state{}, refuse(codeFilesElsewhere, "%s is not the prefix Install recorded", prefix)
	}
	p, _ := placeOf(st.Prefix) // readState took only a place
	ms, err := readMounts()
	if err != nil {
		return state{}, notThere(p, "reading the mount table: %v", err)
	}
	if _, err := locate(ms, p, st.UUID); err != nil {
		return state{}, err
	}
	return st, nil
}
