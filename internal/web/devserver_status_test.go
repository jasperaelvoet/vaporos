package web

import (
	"net/http"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/boot"
)

// The dev server's fake of GET /status (internal/daemon/status.go): one
// summary of the other documents for the shell and Home, on the installed
// system only.

func (f *devFake) statusRoutes(add fakeAdder) {
	add("GET", "/status", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		sun := f.sunshineAnswerLocked()
		var stream any
		if sun["streaming"] == true {
			stream = sun["session"]
		}
		delete(sun, "session")
		pw := f.powerStateLocked(time.Now())
		delete(pw, "wol")
		up, disp := f.updateAnswerLocked(), f.doc("display")
		return map[string]any{
			"system": f.systemAnswerLocked(), "sunshine": sun, "stream": stream, "display": disp,
			"update": up, "power": pw, "restart": restartReasons(up, disp, f.doc("extensions")),
		}
	})
}

// restartReasons is daemon.restartFor: the entry a restart starts when it
// is not the running one (newer: update, else rollback), the display, and
// the extensions.
func restartReasons(up, disp, ext map[string]any) map[string]any {
	reasons := []any{}
	if next := asObj(up["next_boot"]); asStr(next["version"]) != "" {
		kind := "rollback"
		if boot.CompareVersions(asStr(next["version"]), asStr(up["booted"])) > 0 {
			kind = "update"
		}
		reasons = append(reasons, map[string]any{"kind": kind, "version": next["version"]})
	}
	if disp["reboot_needed"] == true {
		reasons = append(reasons, map[string]any{"kind": "display"})
	}
	if extensionsRestart(ext) {
		reasons = append(reasons, map[string]any{"kind": "extensions"})
	}
	return map[string]any{"needed": len(reasons) > 0, "reasons": reasons}
}
