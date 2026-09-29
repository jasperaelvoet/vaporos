package display

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// Paths used to talk to the gaming user's gamescope (variables for tests).
var (
	// UserRuntimeDir is %t of the gaming user's systemd manager.
	UserRuntimeDir = "/run/user/" + strconv.Itoa(config.GamerUID)
	// PNPIDsPath is the hwdata database gamescope resolves EDID vendor ids with.
	PNPIDsPath = "/usr/share/hwdata/pnp.ids"
	// X11SocketDir holds Xwayland's sockets (for the xprop fallback).
	X11SocketDir = "/tmp/.X11-unix"
)

// GamescopeEnvPath is read by vos-gamescope.service (EnvironmentFile=).
func GamescopeEnvPath() string { return filepath.Join(UserRuntimeDir, "vos", "gamescope.env") }

// ModesCfgPath is gamescope's GAMESCOPE_MODE_SAVE_FILE.
func ModesCfgPath() string {
	return filepath.Join(config.GamerHome, ".config", "gamescope", "modes.cfg")
}

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

// updateModesCfg sets the saved mode for each display key in gamescope's
// mode save file, keeping lines for other displays. gamescope reads it with
// sscanf("%255[^:]:%dx%d@%d %u") and takes the first line whose key equals
// "<Make> <Model>" of the connector it drives.
func updateModesCfg(content []byte, keys []string, m edid.Mode) []byte {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out bytes.Buffer
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if ok && want[key] {
			continue // replaced below
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	for _, k := range keys {
		fmt.Fprintf(&out, "%s:%s\n", k, m)
	}
	return out.Bytes()
}

// gamescopectlInfo is the display part of `gamescopectl` (no arguments):
//
//	gamescope_control info:
//	  - Connector Name: DP-1
//	  - Display Make: Best Buy
//	  - Display Model: VaporOS
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

// pnpName resolves a PNP id the way gamescope does (hwdata pnp.ids:
// "ID\tName" lines), falling back to the raw id.
func pnpName(id string) string {
	pnpOnce.Do(func() {
		pnpDB = map[string]string{}
		f, err := os.Open(PNPIDsPath)
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), "\t"); ok {
				pnpDB[k] = v
			}
		}
	})
	if n, ok := pnpDB[id]; ok {
		return n
	}
	return id
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
	return lowestSocket(UserRuntimeDir, "gamescope-", "gamescope-0")
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
