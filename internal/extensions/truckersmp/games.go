// Package truckersmp is the TruckersMP extension's helper: it keeps the
// TruckersMP mod's files in the gaming user's home, starts multiplayer
// through Steam with the truckersmp-cli injector, and tells the card which
// game versions TruckersMP supports (docs/CONTRACTS.md "Extensions",
// TruckersMP).
package truckersmp

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// ID is the extension's id.
const ID = "truckersmp"

// vosBin is the vos binary every command line here names.
const vosBin = "/usr/bin/vos"

// game is one of the two games TruckersMP adds multiplayer to.
type game struct {
	key     string // "ets2" | "ats": the shortcut key and the files.json type
	app     uint32 // its Steam app id
	short   string // what the card calls it
	exe     string // the Windows build's executable, in GAMEDIR/bin/win_x64
	docs    string // its folder in Documents (the Proton prefix) and in ~/.local/share (the Linux build)
	coreDLL string // the mod's core library, which the version API checksums
}

var games = []game{
	{key: "ets2", app: 227300, short: "ETS2", exe: "eurotrucks2.exe", docs: "Euro Truck Simulator 2", coreDLL: "core_ets2mp.dll"},
	{key: "ats", app: 270880, short: "ATS", exe: "amtrucks.exe", docs: "American Truck Simulator", coreDLL: "core_atsmp.dll"},
}

func gameByKey(key string) (game, bool) {
	for _, g := range games {
		if g.key == key {
			return g, true
		}
	}
	return game{}, false
}

func gameByApp(app uint32) (game, bool) {
	for _, g := range games {
		if g.app == app {
			return g, true
		}
	}
	return game{}, false
}

// homeRel is the home data area, relative to the gaming user's home.
func homeRel() string { return filepath.Join(config.ExtGamerDataSubdir, ID) }

// homeDir is the home data area: the injector, the mod's files and the
// manifest of what the last sync wrote, all the gaming user's.
func homeDir() string { return filepath.Join(config.GamerHome, homeRel()) }

// The files in the home data area.
const (
	binRel      = "bin/truckersmp-cli.exe" // the injector's copy, which Proton's container can see
	filesRel    = "files"                  // MODDIR: the mod's files as files.json lays them out
	partialRel  = "partial"                // downloads under way, <md5>.part, kept for resuming
	manifestRel = "manifest.json"          // what the last sync wrote
	lockRel     = ".sync.lock"             // held by the sync that runs
)

// injectorSource is the injector as the image ships it.
func injectorSource() string { return filepath.Join(config.ExtMountedLibDir, ID, "truckersmp-cli.exe") }

// runtimeDir is the gaming user's XDG_RUNTIME_DIR, where the multiplayer
// flag and the messages for vosd go.
func runtimeDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(rt) {
		return rt
	}
	return "/run/user/" + strconv.Itoa(os.Getuid())
}

// branchPath is the switch to an older game version the person asked for,
// in the system data area: root's, so steam.json never follows a file the
// gaming user can write.
func branchPath(dataDir string) string { return filepath.Join(dataDir, "branch.json") }
