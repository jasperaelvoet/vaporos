package sunshine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// appsFile is Sunshine's apps.json. Sunshine requires the "env" object to
// exist, even when empty.
type appsFile struct {
	Env  map[string]string `json:"env"`
	Apps []appEntry        `json:"apps"`
}

type appEntry struct {
	Name      string `json:"name"`
	Cmd       string `json:"cmd,omitempty"`
	ImagePath string `json:"image-path,omitempty"`
}

// renderApps builds apps.json: "Steam" first, which streams the Steam UI
// that gamescope already shows, then one entry per installed game that
// asks the running Steam to launch it. `steam steam://…` hands the URL to
// the running client and exits at once; Sunshine's auto-detach (on by
// default) treats that as a launched, detached app.
//
// Sunshine derives each app's id from its name (and image), so names are
// kept unique; that also keeps ids, and Moonlight's shortcuts, stable
// when the list is regenerated.
func renderApps(games []steam.App) []byte {
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
			Name: escapeEnv(name),
			Cmd:  fmt.Sprintf("steam steam://rungameid/%d", g.ID),
		})
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
