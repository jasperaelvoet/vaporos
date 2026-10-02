package truckersmp

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// gameState is what the card says about one game: the library that has
// it ("" when none does) and the version it last started with.
type gameState struct {
	g       game
	lib     string
	version string
}

// Status is the card's lines: the versions TruckersMP supports, each
// installed game's version against them, and the mod's files. vosd builds
// the card every few seconds, which is also when it looks whether the
// files need a sync: at install, when TruckersMP's version changes (the
// API, asked at most hourly), when a game is installed whose files are
// missing, and daily. A card of TruckersMP while it is not installed has
// no lines (save a first sync's progress).
func (h *Helper) Status(ctx context.Context, x *extensions.Ext) []extensions.StatusLine {
	active := h.active()
	h.mu.Lock()
	running := h.job != nil
	h.mu.Unlock()
	if !active && !running {
		return nil
	}
	m := homeManifest()
	gs := h.games()
	if active {
		h.schedule(m, gs)
	}

	h.mu.Lock()
	info, j, lastErr := h.api, h.job, h.lastErr
	var done, total int64
	if j != nil {
		done, total = j.bytes, j.total
	}
	h.mu.Unlock()

	var lines []extensions.StatusLine
	add := func(tone, format string, a ...any) {
		lines = append(lines, extensions.StatusLine{Text: fmt.Sprintf(format, a...), Tone: tone})
	}
	if s := supportedLine(info); s != "" {
		add("", "%s", s)
	}
	held := readBranch(x.DataDir)
	var installed []string
	for _, s := range gs {
		if s.lib == "" {
			continue
		}
		installed = append(installed, s.g.key)
		if tone, text := versionLine(s, info.supported(s.g), held[s.g.app]); text != "" {
			add(tone, "%s", text)
		}
	}

	switch {
	case j != nil && total > 0:
		add("", "Downloading the TruckersMP mod: %d %%", min(done*100/total, 100))
	case j != nil:
		add("", "Checking the TruckersMP mod for updates")
	case len(installed) == 0:
		add("", "Install ETS2 or ATS in Steam to play TruckersMP.")
	case lastErr != "" && (m == nil || (info != nil && m.Version != info.Name)):
		add("warning", "Couldn't download the TruckersMP mod. VaporOS tries again within an hour.")
	case m == nil:
		add("", "The TruckersMP mod isn't downloaded yet.")
	case info != nil && m.Version != info.Name:
		add("", "TruckersMP %s is out. VaporOS downloads it next.", info.Name)
	case slices.ContainsFunc(installed, func(k string) bool { return !slices.Contains(m.Games, k) }):
		add("", "VaporOS downloads the TruckersMP mod for the newly installed game next.")
	default:
		add("", "TruckersMP %s is ready.", m.Version)
	}
	return lines
}

// supportedLine names the game versions the version API supports.
func supportedLine(info *versionInfo) string {
	if info == nil {
		return ""
	}
	var parts []string
	for _, g := range games {
		if v := info.supported(g); v != "" {
			parts = append(parts, g.short+" "+v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("TruckersMP %s supports %s.", info.Name, strings.Join(parts, " and "))
}

// versionLine is one installed game's line: on the branch VaporOS holds
// it on, or its version against the one TruckersMP supports.
func versionLine(s gameState, supported, held string) (tone, text string) {
	name := s.g.short
	if held != "" {
		heldVersion := strings.ReplaceAll(strings.TrimPrefix(held, "temporary_"), "_", ".")
		if want, ok := branchFor(supported); ok && want != held {
			return "warning", fmt.Sprintf("TruckersMP now supports %s %s. Switch to the latest version.", name, minorOf(supported))
		}
		if c, ok := compareMinor(s.version, heldVersion); ok && c > 0 {
			return "", fmt.Sprintf("%s switches to version %s when Steam restarts.", name, heldVersion)
		}
		return "", fmt.Sprintf("%s stays on version %s for TruckersMP.", name, heldVersion)
	}
	if s.version == "" {
		return "", ""
	}
	c, ok := compareMinor(s.version, supported)
	switch {
	case !ok:
		return "", fmt.Sprintf("%s runs version %s.", name, s.version)
	case c > 0:
		return "warning", fmt.Sprintf("%s %s is newer than TruckersMP supports. Switch to the supported version.", name, s.version)
	case c < 0:
		return "warning", fmt.Sprintf("%s %s is older than TruckersMP supports. Update it in Steam.", name, s.version)
	}
	return "", fmt.Sprintf("%s %s works with TruckersMP.", name, s.version)
}

// homeManifest reads the last sync's manifest from the gaming user's
// home, through gamerfs (vosd is root there); nil without one.
func homeManifest() *manifest {
	b, err := gamerfs.ReadFile(config.GamerHome, filepath.Join(homeRel(), manifestRel), maxManifest)
	if err != nil {
		return nil
	}
	m, err := parseManifest(b)
	if err != nil {
		log.Printf("truckersmp: %v", err)
		return nil
	}
	return m
}

// games looks at the games at most every gamesEvery.
func (h *Helper) games() []gameState {
	h.mu.Lock()
	if h.seen != nil && time.Since(h.seenAt) < gamesEvery {
		gs := h.seen
		h.mu.Unlock()
		return gs
	}
	h.mu.Unlock()
	libs := libraries()
	var gs []gameState
	for _, g := range games {
		s := gameState{g: g, lib: gameLibrary(libs, g)}
		if s.lib != "" {
			s.version = installedVersion(s.lib, g)
		}
		gs = append(gs, s)
	}
	h.mu.Lock()
	h.seen, h.seenAt = gs, time.Now()
	h.mu.Unlock()
	return gs
}

// schedule asks the version API when its answer is old and starts a sync
// when one is due, never two at once nor two within syncGap. Status calls
// it only while the extension is mounted and set up.
func (h *Helper) schedule(m *manifest, gs []gameState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	every := apiEvery
	if h.api == nil {
		every = apiRetry
	}
	if !h.asking && (h.apiAt.IsZero() || time.Since(h.apiAt) >= every) {
		h.asking = true
		go h.askAPI()
	}
	if h.job != nil || (!h.lastRun.IsZero() && now().Sub(h.lastRun) < syncGap) {
		return
	}
	due := m == nil || now().Sub(m.Checked) >= syncEvery || (h.api != nil && h.api.Name != m.Version)
	for _, s := range gs {
		due = due || (s.lib != "" && !m.has(s.g))
	}
	if due {
		h.startLocked()
	}
}

func (h *Helper) askAPI() {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	v, err := h.fetchAPI(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asking, h.apiAt = false, time.Now()
	if err != nil {
		log.Printf("truckersmp: version API: %v", err)
		return
	}
	h.api = v
}
