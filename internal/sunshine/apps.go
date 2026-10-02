package sunshine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// appsFile is Sunshine's apps.json. Sunshine requires the "env" object to
// exist, even when empty.
type appsFile struct {
	Env  map[string]string `json:"env"`
	Apps []appEntry        `json:"apps"`
}

type appEntry struct {
	Name      string   `json:"name"`
	Detached  []string `json:"detached,omitempty"`
	ImagePath string   `json:"image-path,omitempty"`
}

// launchCmd asks the Steam client running in gamescope to start a game.
// No app has a "cmd": Sunshine would run it as the app's own process, and
// a `steam steam://…` started while Steam is not up yet (gamescope is only
// started by the prep command) would bring up a second Steam outside
// gamescope. `vos session launch` hands the URL to the Steam in the
// session instead, and as a "detached" command Sunshine starts it and
// lets it go; the app then runs until it is quit, like "Steam".
const launchCmd = "/usr/bin/vos session launch steam://rungameid/%d"

// renderApps builds apps.json: "Steam" first, which streams the Steam UI
// that gamescope already shows, then one entry per installed game that
// asks the Steam in gamescope to launch it (launchCmd), then the apps the
// extensions add (extensions.SunshineApps: their Steam shortcuts, started
// the same way by game id, and their helpers' own entries), whose
// commands are taken literally.
//
// Sunshine derives each app's id from its name (and image), so names are
// kept unique; that also keeps ids, and Moonlight's shortcuts, stable
// when the list is regenerated.
func renderApps(games []steam.App, extra []extensions.SunshineApp) []byte {
	f := appsFile{
		// Sunshine's default: the gaming user's scripts are on PATH.
		Env:  map[string]string{"PATH": "$(PATH):$(HOME)/.local/bin"},
		Apps: []appEntry{{Name: "Steam", ImagePath: "steam.png"}},
	}
	used := map[string]bool{"steam": true}
	for _, g := range games {
		name := g.Name
		if used[strings.ToLower(name)] {
			name = fmt.Sprintf("%s (%d)", g.Name, g.ID)
		}
		used[strings.ToLower(name)] = true
		f.Apps = append(f.Apps, appEntry{
			Name:     escapeEnv(name),
			Detached: []string{fmt.Sprintf(launchCmd, g.ID)},
		})
	}
	for _, a := range extra {
		name := a.Name
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s (%d)", a.Name, n)
		}
		used[strings.ToLower(name)] = true
		cmds := make([]string, len(a.Detached))
		for i, c := range a.Detached {
			cmds[i] = escapeEnv(c)
		}
		f.Apps = append(f.Apps, appEntry{Name: escapeEnv(name), Detached: cmds})
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	return append(b, '\n')
}

// escapeEnv protects a literal "$": Sunshine expands $(VAR) in app names
// and commands, and reads "$$" as one "$".
func escapeEnv(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// installedGames lists the games in every Steam library of the gaming user.
func installedGames(home string) []steam.App {
	return steam.InstalledGames(steam.Libraries(steam.Root(home)))
}
