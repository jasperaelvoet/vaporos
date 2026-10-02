package extensions

import (
	"log"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// SunshineApp is an app Sunshine lists after the Steam games
// (docs/CONTRACTS.md "Units", apps.json): its name and the commands
// Sunshine runs detached, as the gaming user.
type SunshineApp struct {
	Name     string   `json:"name"`
	Detached []string `json:"detached"`
}

// sessionLaunch starts a Steam URL in gamescope's Steam (Sunshine's own
// game entries use it too).
const sessionLaunch = "/usr/bin/vos session launch steam://rungameid/"

// SunshineApps returns the apps the extensions add to Sunshine, for those
// mounted and still wanted: each of their shortcuts whose game id prepare
// recorded, then the helpers' own entries.
func SunshineApps() []SunshineApp {
	if config.IsLive() {
		return nil
	}
	e, err := loadSteamEntries()
	if err != nil {
		log.Printf("extensions: Sunshine apps: %v", err)
		return nil
	}
	var out []SunshineApp
	if d := e.desiredSteam(false); len(d.Shortcuts) > 0 {
		st := readPrepareState()
		for _, sc := range d.Shortcuts {
			if id, ok := st.gameID(sc.Owner, sc.Key); ok {
				out = append(out, SunshineApp{Name: sc.Name, Detached: []string{sessionLaunch + strconv.FormatUint(id, 10)}})
			}
		}
	}
	for _, id := range e.ids {
		for _, a := range e.parts[id].SunshineApps {
			if !validSunshineApp(a) {
				log.Printf("extensions: %s: Sunshine app %q is not valid", id, a.Name)
				continue
			}
			out = append(out, a)
		}
	}
	return out
}

func validSunshineApp(a SunshineApp) bool {
	text := func(s string, max int) bool {
		return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
	}
	if !text(a.Name, 60) || len(a.Detached) == 0 {
		return false
	}
	for _, c := range a.Detached {
		if !text(c, 1024) {
			return false
		}
	}
	return true
}
