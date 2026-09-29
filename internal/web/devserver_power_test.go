package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// The dev server's fake of power (internal/power): idle power-off,
// keep-awake, Wake-on-LAN (wol[] with each wired adapter's MAC, address,
// prefix and broadcast, for the Wake card) and the power.idle events.
// Document: base/power.json; busy, web_until, idle_seconds and shutdown_in
// are worked out on every answer, as power.snapshot does.

const (
	fakePowerInterval = 15 * time.Second // power.interval
	fakeWebWindow     = 5 * time.Minute  // power.webActivityWindow
	fakeMaxKeepAwake  = 7 * 24 * 60      // minutes, power.maxKeepAwake
	fakeWebReason     = "web UI in use"
)

func (f *devFake) powerRoutes(add fakeAdder) {
	add("GET", "/power", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		return f.powerStateLocked(time.Now())
	})
	add("PUT", "/power", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			IdleShutdown *bool `json:"idle_shutdown"`
			IdleMinutes  *int  `json:"idle_minutes"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		if req.IdleMinutes != nil && (*req.IdleMinutes < 1 || *req.IdleMinutes > 24*60) {
			api.Error(w, http.StatusBadRequest, "idle_minutes must be between 1 and 1440")
			return nil
		}
		p := f.doc("power")
		if req.IdleShutdown != nil {
			p["idle_shutdown"] = *req.IdleShutdown
		}
		if req.IdleMinutes != nil {
			p["idle_minutes"] = float64(*req.IdleMinutes)
		}
		return f.powerStateLocked(time.Now())
	})
	add("POST", "/power/keep-awake", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Minutes *int `json:"minutes"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		if req.Minutes == nil || *req.Minutes < 0 || *req.Minutes > fakeMaxKeepAwake {
			api.Error(w, http.StatusBadRequest, "minutes must be between 0 and %d", fakeMaxKeepAwake)
			return nil
		}
		p, now := f.doc("power"), time.Now()
		delete(p, "keep_awake_until")
		if *req.Minutes > 0 {
			p["keep_awake_until"] = now.Add(time.Duration(*req.Minutes) * time.Minute).UTC().Truncate(time.Second).Format(time.RFC3339)
			f.idleSince = now
		}
		f.powerCheckLocked(now, false)
		return fakeOK
	})
}

// busyLocked is power.busyReason: keep-awake, the stream, an update being
// installed, then the viewer's own web activity, last.
func (f *devFake) busyLocked(now time.Time) (reason string, web bool) {
	if t, err := time.Parse(time.RFC3339, asStr(f.doc("power")["keep_awake_until"])); err == nil && now.Before(t) {
		return "keep-awake", false
	}
	if s := f.doc("sunshine"); s["streaming"] == true {
		return "streaming to " + asStr(asObj(s["session"])["client"]), false
	}
	if f.stage != nil {
		p := f.stage.progress
		if p["phase"] != "check" && asStr(p["version"]) != "" {
			return fmt.Sprintf("installing update %s (%d%%)", asStr(p["version"]), int(asNum(p["percent"]))), false
		}
		return "checking for updates", false
	}
	if f.webUntilLocked(now).After(now) {
		return fakeWebReason, true
	}
	return "", false
}

func (f *devFake) webUntilLocked(now time.Time) time.Time {
	if f.lastTouch.IsZero() || !now.Before(f.lastTouch.Add(fakeWebWindow)) {
		return time.Time{}
	}
	return f.lastTouch.Add(fakeWebWindow)
}

// powerStateLocked is GET /power (power.snapshot).
func (f *devFake) powerStateLocked(now time.Time) map[string]any {
	p := cloneDoc(f.doc("power"))
	if t, err := time.Parse(time.RFC3339, asStr(p["keep_awake_until"])); err != nil || !now.Before(t) {
		delete(p, "keep_awake_until")
	}
	delete(p, "web_until")
	if until := f.webUntilLocked(now); !until.IsZero() {
		p["web_until"] = until.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	p["busy"], p["idle_seconds"], p["shutdown_in"] = nil, 0, nil
	if reason, web := f.busyLocked(now); reason != "" {
		p["busy"] = fakeBusy(reason, web)
		return p
	}
	idle := now.Sub(f.idleSince)
	p["idle_seconds"] = int(idle.Seconds())
	if left, ok := f.shutdownInLocked(idle); ok {
		p["shutdown_in"] = left
	}
	return p
}

func fakeBusy(reason string, web bool) map[string]any {
	b := map[string]any{"reason": reason}
	if web {
		b["web"] = true
	}
	return b
}

// shutdownInLocked is the seconds left before idle power-off, when it is on.
func (f *devFake) shutdownInLocked(idle time.Duration) (int, bool) {
	p := f.doc("power")
	limit := time.Duration(asNum(p["idle_minutes"])) * time.Minute
	if p["idle_shutdown"] != true || limit <= 0 {
		return 0, false
	}
	return max(int((limit - idle).Seconds()), 0), true
}

// powerLoop is power.Run: every 15 s, one pass of the idle policy.
func (f *devFake) powerLoop(ctx context.Context) {
	t := time.NewTicker(fakePowerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		f.mu.Lock()
		f.powerCheckLocked(time.Now(), true)
		f.mu.Unlock()
	}
}

// powerCheckLocked is power.tick: busy publishes power.idle once per
// reason; idle publishes it on every pass (tick), and powers off once idle
// for idle_minutes. Between passes it only reports a change of reason.
func (f *devFake) powerCheckLocked(now time.Time, tick bool) {
	if f.installer {
		return
	}
	reason, web := f.busyLocked(now)
	if reason != "" {
		f.idleSince = now
		if f.powerSent != reason {
			f.powerSent = reason
			f.hub.Publish("power.idle", map[string]any{"idle_seconds": 0, "shutdown_in": nil, "busy": fakeBusy(reason, web)})
		}
		return
	}
	if !tick && f.powerSent == "idle" {
		return
	}
	f.powerSent = "idle"
	idle := now.Sub(f.idleSince)
	ev := map[string]any{"idle_seconds": int(idle.Seconds()), "shutdown_in": nil, "busy": nil}
	left, on := f.shutdownInLocked(idle)
	if on {
		ev["shutdown_in"] = left
	}
	f.hub.Publish("power.idle", ev)
	if on && left == 0 && tick && f.d != nil {
		minutes := int(asNum(f.doc("power")["idle_minutes"]))
		f.emitLocked("system.message", map[string]string{"level": "info", "text": fmt.Sprintf(
			"Nothing has happened for %d minutes, so VaporOS is switching off. Wake it with Wake-on-LAN (Moonlight does this for you).", minutes)})
		f.restartLocked(-1, nil)
	}
}
