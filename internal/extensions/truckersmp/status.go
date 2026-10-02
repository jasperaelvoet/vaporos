package truckersmp

import (
	"context"
	"errors"
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
// it ("" when none does), the version it last started with and where
// Steam has it.
type gameState struct {
	g       game
	lib     string
	version string
	steam   steamView
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
		var hold *branchHold
		if b, ok := held[s.g.app]; ok {
			hold = &b
		}
		if tone, text := versionLine(s, info.supported(s.g), hold); text != "" {
			add(tone, "%s", text)
		}
	}

	newGame := m != nil && slices.ContainsFunc(installed, func(k string) bool { return !slices.Contains(m.Games, k) })
	behind := m == nil || (info != nil && m.Version != info.Name) || newGame
	var space *spaceError
	switch {
	case j != nil && total > 0:
		add("", "Downloading the TruckersMP mod: %d %%", min(done*100/total, 100))
	case j != nil && done > 0:
		add("", "Downloading the TruckersMP mod: %s so far", sizeText(done))
	case j != nil:
		add("", "Checking the TruckersMP mod for updates")
	case len(installed) == 0:
		add("", "Install ETS2 or ATS in Steam to play TruckersMP.")
	case lastErr != nil && behind && errors.As(lastErr, &space):
		add("warning", "There isn't enough free space to download the TruckersMP mod. Free up %s, and VaporOS tries again within an hour.", sizeText(space.short))
	case lastErr != nil && behind && errors.Is(lastErr, errUnchecked):
		add("warning", "TruckersMP hasn't confirmed the checksum of its new files yet, so VaporOS didn't download them. It tries again within an hour.")
	case lastErr != nil && behind:
		add("warning", "Couldn't download the TruckersMP mod. VaporOS tries again within an hour.")
	case m == nil:
		add("", "The TruckersMP mod isn't downloaded yet.")
	case info != nil && m.Version != info.Name:
		add("", "TruckersMP %s is out. VaporOS downloads it next.", info.Name)
	case newGame:
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
// it on (hold, nil when none), or its version against the one TruckersMP
// supports. It promises a switch only while prepare has yet to make it:
// a game moved off its branch in Steam since needs a new request.
func versionLine(s gameState, supported string, hold *branchHold) (tone, text string) {
	name := s.g.short
	if hold != nil && hold.Branch != "" {
		v := heldVersion(hold.Branch)
		if want, ok := branchFor(supported); ok && want != hold.Branch {
			if c, ok := compareMinor(supported, newer(hold.Latest, s.version)); !ok || c >= 0 {
				return "warning", fmt.Sprintf("TruckersMP now supports %s %s, its latest version. Switch to the supported version.", name, minorOf(supported))
			}
			return "warning", fmt.Sprintf("TruckersMP now supports %s %s. Switch to the supported version.", name, minorOf(supported))
		}
		c, ok := compareMinor(s.version, v)
		differs := ok && c != 0
		switch {
		case s.steam.pending(*hold) && differs:
			return "", fmt.Sprintf("%s switches to version %s when Steam restarts.", name, v)
		case s.steam.on(*hold) && differs:
			return "", fmt.Sprintf("%s runs version %s from its next start.", name, v)
		case s.steam.left(*hold) && !differs:
			return "warning", fmt.Sprintf("%s was moved off version %s in Steam. Switch to the supported version.", name, v)
		case s.steam.pending(*hold) || !differs:
			return "", fmt.Sprintf("%s stays on version %s for TruckersMP.", name, v)
		case c < 0:
			return "warning", fmt.Sprintf("%s %s is older than TruckersMP supports. Switch to the supported version.", name, s.version)
		}
		return "warning", fmt.Sprintf("%s %s is newer than TruckersMP supports. Switch to the supported version.", name, s.version)
	}
	if hold != nil {
		if c, ok := compareMinor(s.version, supported); ok && c < 0 {
			switch {
			case s.steam.pending(*hold):
				return "", fmt.Sprintf("%s updates to its latest version when Steam restarts.", name)
			case s.steam.on(*hold):
				return "", fmt.Sprintf("%s runs its latest version from its next start.", name)
			}
		}
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
	applied := appliedRequests()
	var gs []gameState
	for _, g := range games {
		s := gameState{g: g, lib: gameLibrary(libs, g)}
		if s.lib != "" {
			s.version = installedVersion(s.lib, g)
			s.steam = viewSteam(s.lib, g, applied)
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
