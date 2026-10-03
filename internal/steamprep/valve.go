package steamprep

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// Steam ranks Valve's own pick for a game above the default ("0", 75):
// the runtime its Deck profile recommends (85) and the Steam Play
// manifest's per-game mappings. So the default alone reaches only games
// Valve never tested, and VaporOS writes a per-game copy of it where
// Valve's pick is a Proton in general (stable or experimental). A game
// Valve pins to one version, maps in its manifest, or runs natively
// keeps Valve's pick.
var genericProtons = map[string]bool{"proton-stable": true, "proton-experimental": true}

// steamPlayManifests is the app whose appinfo holds the Steam Play
// manifest (extended/app_mappings).
const steamPlayManifests = 891390

// The appinfo types of what a person plays: tools and configs never get
// a mapping.
var playable = map[string]bool{"game": true, "demo": true, "application": true, "beta": true}

// pick is what Valve's data says about one app.
type pick int

const (
	pickUnknown pick = iota // no appinfo for it, or none at all yet
	pickProton              // Valve's pick is a Proton in general: VaporOS's default stands in
	pickValve               // anything else: Valve's pick, or none, and the default reaches it
)

// valvePicks reads what Valve picks for apps from Steam's appinfo cache,
// once a run. Without the cache, or without the Steam Play manifest in it
// (Steam fetches both as it starts), every app is unknown.
func (p *prep) valvePicks(apps []uint32) map[uint32]pick {
	out := make(map[uint32]pick, len(apps))
	if len(apps) == 0 {
		return out
	}
	wanted := map[uint32]bool{steamPlayManifests: true}
	for _, a := range apps {
		wanted[a] = true
	}
	info, err := steam.ReadAppInfo(steam.AppInfoPath(p.root), func(app uint32) bool { return wanted[app] })
	if err != nil {
		if !os.IsNotExist(err) {
			p.o.Log.Printf("prepare: %s: %v; Valve's picks are not known, so no game gets a copy of the default this run", relName(p.root, steam.AppInfoPath(p.root)), err)
		}
		return out
	}
	manifest := info[steamPlayManifests].Get("appinfo", "extended", "app_mappings")
	if manifest == nil {
		return out
	}
	mapped := map[uint32]bool{}
	for _, m := range manifest.Children {
		if id, ok := m.Get("appid").Uint32(); ok && m.Get("tool").Text() != "" {
			mapped[id] = true
		}
	}
	for _, app := range apps {
		a := info[app].Get("appinfo")
		if a == nil {
			continue
		}
		rr := a.Get("common", "steam_deck_compatibility", "configuration", "recommended_runtime").Text()
		if playable[strings.ToLower(a.Get("common", "type").Text())] && !mapped[app] && genericProtons[strings.ToLower(rr)] {
			out[app] = pickProton
		} else {
			out[app] = pickValve
		}
	}
	return out
}

// installedApps lists the app ids of the appmanifests in Steam's
// libraries, by their names. A library whose drive is away lists none.
func (p *prep) installedApps() []uint32 {
	var ids []uint32
	for _, lib := range p.libraries() {
		names, _ := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
		for _, n := range names {
			s := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), "appmanifest_"), ".acf")
			if id, err := strconv.ParseUint(s, 10, 32); err == nil && id > 0 && strconv.FormatUint(id, 10) == s {
				ids = append(ids, uint32(id))
			}
		}
	}
	return ids
}
