package display

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// Paths used to talk to the gaming user's gamescope (variables for tests).
var (
	// PNPIDsPath is the hwdata database gamescope resolves EDID vendor ids with.
	PNPIDsPath = "/usr/share/hwdata/pnp.ids"
	// X11SocketDir holds Xwayland's sockets (xprop finds DISPLAY there).
	X11SocketDir = "/tmp/.X11-unix"
)

// compositeForceProp is the X root window property behind gamescope's
// composite_force convar. gamescope's PropertyNotify handler assigns the
// property's value to the convar whenever anyone writes it (Steam does, with
// 0), overriding `gamescopectl composite_force 1`; an unset property reads
// as 0. vosd therefore sets both, and re-checks the property.
const compositeForceProp = "GAMESCOPE_COMPOSITE_FORCE"

// hdrEnabledProp is the X root window property behind gamescope's
// hdr_enabled convar, the one Steam's "Enable HDR" display setting writes.
// Like compositeForceProp, gamescope copies it into the convar whenever
// anyone writes it, and Steam writes its own per-display setting there,
// whatever --hdr-enabled said at start.
const hdrEnabledProp = "GAMESCOPE_DISPLAY_HDR_ENABLED"

// setRootPropArgs and getRootPropArgs are the xprop arguments that set and
// read a CARDINAL property on the root window.
func setRootPropArgs(name, value string) []string {
	return []string{"-root", "-f", name, "32c", "-set", name, value}
}

func getRootPropArgs(name string) []string { return []string{"-root", name} }

// parseXpropCardinal reads `xprop -root NAME` output for a CARDINAL
// property: "NAME(CARDINAL) = 1" gives (1, true); "NAME:  not found." (or
// anything without a value) gives (0, false).
func parseXpropCardinal(out, name string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, name)
		if !ok || (rest != "" && rest[0] != '(' && rest[0] != ':' && rest[0] != ' ' && rest[0] != '=') {
			continue // another property whose name merely starts the same
		}
		_, v, ok := strings.Cut(rest, "=")
		if !ok {
			return 0, false
		}
		first, _, _ := strings.Cut(strings.TrimSpace(v), ",")
		n, err := strconv.ParseInt(strings.TrimSpace(first), 0, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// The files vosd keeps for gamescope live in the gaming user's tree, which
// Steam and every game can change. They are read and written through
// gamerfs (never following a planted symlink, never blocking on a FIFO),
// relative to these roots, and reads are bounded.
const (
	// gamescopeEnvRel is gamescope.env, relative to config.GamerRuntimeDir.
	gamescopeEnvRel = "vos/gamescope.env"
	// modesCfgRel is modes.cfg, relative to config.GamerHome.
	modesCfgRel = ".config/gamescope/modes.cfg"
	// maxGamescopeEnv and maxModesCfg bound the reads; a larger file is
	// not ours and is replaced.
	maxGamescopeEnv = 4 << 10
	maxModesCfg     = 64 << 10
	// maxModesCfgKept bounds the other displays' lines kept in modes.cfg.
	maxModesCfgKept = 64
)

// GamescopeEnvPath is read by vos-gamescope.service (EnvironmentFile=).
func GamescopeEnvPath() string { return filepath.Join(config.GamerRuntimeDir, gamescopeEnvRel) }

// ModesCfgPath is gamescope's GAMESCOPE_MODE_SAVE_FILE.
func ModesCfgPath() string { return filepath.Join(config.GamerHome, modesCfgRel) }

// gamescopeEnv is what the gamescope unit is started with.
type gamescopeEnv struct {
	Output string // --prefer-output: the virtual connector
	HDR    bool   // --hdr-enabled
}

// render formats the EnvironmentFile. VOS_GS_EXTRA is expanded unquoted
// ($VOS_GS_EXTRA) in ExecStart, so it is split into words there.
func (e gamescopeEnv) render() []byte {
	extra := ""
	if e.HDR {
		extra = "--hdr-enabled"
	}
	return []byte(fmt.Sprintf("# Written by vosd; read by vos-gamescope.service.\nVOS_OUTPUT=%s\nVOS_GS_EXTRA=%s\n", e.Output, extra))
}

// parseGamescopeEnv reads back what render wrote.
func parseGamescopeEnv(b []byte) gamescopeEnv {
	var e gamescopeEnv
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "VOS_OUTPUT":
			e.Output = v
		case "VOS_GS_EXTRA":
			for _, f := range strings.Fields(v) {
				if f == "--hdr-enabled" {
					e.HDR = true
				}
			}
		}
	}
	return e
}

// modesCfgLine is one entry as gamescope reads it, sscanf("%255[^:]:%dx%d@%d %u"):
// "<Make> <Model>:WxH@R", optionally followed by " N".
var modesCfgLine = regexp.MustCompile(`^([^:\x00-\x1f\x7f]+):[0-9]{1,5}x[0-9]{1,5}@[0-9]{1,4}( [0-9]{1,10})?$`)

// updateModesCfg sets the saved mode for each display key in gamescope's
// mode save file, keeping entries for other displays. gamescope reads it
// with sscanf("%255[^:]:%dx%d@%d %u") and takes the first line whose key
// equals "<Make> <Model>" of the connector it drives. Only lines that are
// such entries survive (at most maxModesCfgKept of them): whatever else the
// old file held, it is never copied into the new one.
func updateModesCfg(content []byte, keys []string, m edid.Mode) []byte {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out bytes.Buffer
	kept := 0
	for _, line := range strings.Split(string(content), "\n") {
		sub := modesCfgLine.FindStringSubmatch(line)
		if sub == nil || len(sub[1]) > 255 || !utf8.ValidString(sub[1]) || want[sub[1]] {
			continue // not an entry, or ours (replaced below)
		}
		if kept++; kept > maxModesCfgKept {
			break
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	for _, k := range keys {
		line := fmt.Sprintf("%s:%s", k, m)
		if modesCfgLine.MatchString(line) && len(k) <= 255 {
			out.WriteString(line + "\n")
		}
	}
	return out.Bytes()
}

// gamescopectlInfo is the display part of `gamescopectl` (no arguments),
// as gamescope 3.16 prints it:
//
//	gamescope_control info:
//	  - Connector Name: DP-1
//	  - Display Make: Dell Inc.
//	  - Display Model: DELL U2723QE
//
// On the virtual display, Make is "VOS": pnp.ids does not list that id.
type gamescopectlInfo struct {
	Connector, Make, Model string
}

func parseGamescopectl(out string) (gamescopectlInfo, bool) {
	var info gamescopectlInfo
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Connector Name":
			info.Connector = v
		case "Display Make":
			info.Make, found = v, true
		case "Display Model":
			info.Model = v
		}
	}
	return info, found
}

// Key is gamescope's description of the display: "<Make> <Model>".
func (i gamescopectlInfo) Key() string { return i.Make + " " + i.Model }

var (
	pnpOnce sync.Once
	pnpDB   map[string]string

	keyMu    sync.Mutex
	keyCache = map[[32]byte]string{}
)

// pnpName resolves a PNP id the way gamescope's connector Make does: the
// name hwdata's pnp.ids gives it, else (id not listed, or no pnp.ids at
// all) the raw three-letter id itself. VaporOS's "VOS" is not listed, so
// it stays "VOS".
func pnpName(id string) string {
	pnpOnce.Do(func() {
		pnpDB = map[string]string{}
		f, err := os.Open(PNPIDsPath)
		if err != nil {
			return
		}
		defer f.Close()
		pnpDB = parsePNPIDs(f)
	})
	if n, ok := pnpDB[id]; ok {
		return n
	}
	return id
}

// parsePNPIDs reads pnp.ids exactly like gamescope's load_pnps: each line
// loses its newline and is split at its first tab into id and name (the
// name kept verbatim, whatever its length); lines without a tab are
// skipped, and a later line for the same id wins.
func parsePNPIDs(r io.Reader) map[string]string {
	db := map[string]string{}
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		if id, name, ok := strings.Cut(line, "\t"); ok {
			db[id] = name
		}
		if err != nil {
			return db
		}
	}
}

// gamescopeKeyForEDID computes the modes.cfg key gamescope will use for a
// connector with this EDID, cached per EDID.
func gamescopeKeyForEDID(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	keyMu.Lock()
	defer keyMu.Unlock()
	if k, ok := keyCache[sum]; ok {
		return k, nil
	}
	info, err := edid.Decode(b)
	if err != nil {
		return "", err
	}
	// gamescope copies at most 15 characters of the model name.
	model := info.Name
	if len(model) > 15 {
		model = model[:15]
	}
	k := pnpName(info.PNP) + " " + model
	keyCache[sum] = k
	return k, nil
}

// waylandDisplay finds gamescope's Wayland socket ("gamescope-0") in the
// gaming user's runtime dir; gamescopectl needs it in
// GAMESCOPE_WAYLAND_DISPLAY.
func waylandDisplay() string {
	return lowestSocket(config.GamerRuntimeDir, "gamescope-", "gamescope-0")
}

// xDisplay finds the first Xwayland display gamescope serves (":0").
func xDisplay() string {
	s := lowestSocket(X11SocketDir, "X", "X0")
	return ":" + strings.TrimPrefix(s, "X")
}

// lowestSocket returns the socket in dir named prefix<N> with the lowest N.
func lowestSocket(dir, prefix, fallback string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fallback
	}
	type cand struct {
		name string
		n    int
	}
	var cs []cand
	for _, e := range ents {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || e.Type()&os.ModeSocket == 0 {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		cs = append(cs, cand{e.Name(), n})
	}
	if len(cs) == 0 {
		return fallback
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].n < cs[j].n })
	return cs[0].name
}
