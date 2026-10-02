package daemon

import (
	"context"
	"log"
	"net/http"
	"sync"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/power"
	"github.com/jasperaelvoet/vaporos/internal/sunshine"
	"github.com/jasperaelvoet/vaporos/internal/system"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// statusSources are the parts of GET /status. None waits on Sunshine or
// ethtool, so the summary answers at page load; each part is its own
// route's answer, or that answer without its slow field.
type statusSources struct {
	system   func() system.Info
	sunshine func(context.Context) sunshine.Summary
	stream   func() *display.Session
	display  func() display.Info
	update   func() update.View
	power    func() power.Summary
	// extensions reports whether a restart would try the extensions'
	// pending set; nil leaves the kind out.
	extensions func() bool
}

// statusView is GET /status (docs/CONTRACTS.md). A part that fails is
// null; the others still answer.
type statusView struct {
	System   *system.Info      `json:"system"`
	Sunshine *sunshine.Summary `json:"sunshine"`
	Stream   *display.Session  `json:"stream"`
	Display  *display.Info     `json:"display"`
	Update   *update.View      `json:"update"`
	Power    *power.Summary    `json:"power"`
	Restart  restartView       `json:"restart"`
}

type restartView struct {
	Needed  bool            `json:"needed"`
	Reasons []restartReason `json:"reasons"`
}

type restartReason struct {
	Kind    string `json:"kind"` // "update", "rollback", "display" or "extensions"
	Version string `json:"version,omitempty"`
}

// view gathers the parts side by side, so the answer takes as long as the
// slowest one.
func (src statusSources) view(ctx context.Context) statusView {
	var v statusView
	var wg sync.WaitGroup
	part := func(name string, f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					log.Printf("status: %s: %v", name, p)
				}
			}()
			f()
		}()
	}
	part("system", func() { x := src.system(); v.System = &x })
	part("sunshine", func() { x := src.sunshine(ctx); v.Sunshine = &x })
	part("stream", func() { v.Stream = src.stream() })
	part("display", func() { x := src.display(); v.Display = &x })
	part("update", func() { x := src.update(); v.Update = &x })
	part("power", func() { x := src.power(); v.Power = &x })
	ext := false
	if src.extensions != nil {
		part("extensions", func() { ext = src.extensions() })
	}
	wg.Wait()
	v.Restart = restartFor(v.Update, v.Display, ext)
	return v
}

// restartFor says why a restart is wanted: the entry it would start is not
// the running one (newer: an update, else a rollback), the virtual display
// waits for one, or it would try a change to the extensions.
func restartFor(u *update.View, d *display.Info, ext bool) restartView {
	r := restartView{Reasons: []restartReason{}}
	if u != nil && u.NextBoot != nil {
		kind := "rollback"
		if boot.CompareVersions(u.NextBoot.Version, u.Booted) > 0 {
			kind = "update"
		}
		r.Reasons = append(r.Reasons, restartReason{Kind: kind, Version: u.NextBoot.Version})
	}
	if d != nil && d.RebootNeeded {
		r.Reasons = append(r.Reasons, restartReason{Kind: "display"})
	}
	if ext {
		r.Reasons = append(r.Reasons, restartReason{Kind: "extensions"})
	}
	r.Needed = len(r.Reasons) > 0
	return r
}

func (src statusSources) handle(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, src.view(r.Context()))
}
