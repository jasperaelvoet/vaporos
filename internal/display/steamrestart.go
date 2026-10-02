package display

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Restarting Steam. `vos steam prepare` applies what VaporOS sets in
// Steam (docs/CONTRACTS.md "Extensions", Steam) as gamescope's unit starts,
// before Steam, which keeps its files in memory while it runs. When that
// changes under a running Steam (steam.json, a new account), vosd restarts
// Steam at a quiet moment so prepare runs again: only while gamescope runs
// for the virtual display with nobody streaming and nothing busy, and only
// on a box without a monitor; with one, the welcome screen replaces
// gamescope whenever the box idles, and its next start applies it anyway.

// steamOutcome is how one restart attempt ended.
type steamOutcome int

const (
	steamNotNeeded steamOutcome = iota // nothing to restart, or Steam started after the change
	steamRestarted                     // Steam (or gamescope) started again
	steamNotNow                        // busy: try again later
)

// steamRequest is a Steam restart waiting for its moment.
type steamRequest struct {
	reason string
	since  time.Time // when it was asked for: a Steam started after that has seen the change
	user   bool      // someone asked: not held back by steamEvery
}

// RestartSteam asks for Steam to be restarted, for reason, so `vos steam
// prepare` runs again. It never blocks: the policy loop restarts Steam
// once it can. Requests made before that are one restart. vosd's own
// (user false) wait steamEvery after the last restart; a person's do not.
func (m *Manager) RestartSteam(reason string, user bool) {
	m.mu.Lock()
	req := &steamRequest{reason: reason, since: m.now(), user: user}
	if old := m.steamReq; old != nil {
		req.user = req.user || old.user
	}
	m.steamReq = req
	m.mu.Unlock()
	m.poke()
}

// maybeRestartSteam starts a requested restart when none runs and the
// rate limit allows, in its own goroutine: one waits up to steamWait for
// Steam to come back.
func (m *Manager) maybeRestartSteam(ctx context.Context) {
	m.mu.Lock()
	req := m.steamReq
	if req == nil || m.steamBusy || (!req.user && !m.steamLast.IsZero() && m.now().Sub(m.steamLast) < m.steamEvery) {
		m.mu.Unlock()
		return
	}
	m.steamBusy = true
	m.mu.Unlock()
	go func() {
		out, err := m.restartSteam(ctx, req)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.steamBusy = false
		switch {
		case err != nil:
			log.Printf("display: restarting Steam (%s): %v", req.reason, err)
			m.steamLast = m.now() // try again after steamEvery, not on every tick
			if m.steamReq == req {
				req.user = false
			}
		case out == steamNotNow:
		default:
			if out == steamRestarted {
				m.steamLast = m.now()
			}
			if m.steamReq == req {
				m.steamReq = nil
			}
		}
	}()
}

// restartSteam is one attempt. Under m.op (TryLock: never waiting for a
// session's mode switch) it checks that a restart is due and allowed,
// then asks the Steam client holding steam.pipe to shut down, so gamescope
// exits with it and systemd starts both again; without such a client it
// restarts gamescope's unit. `steam -shutdown` gets steamShutdownWait and
// runs without m.op, so a session's Begin is never held up behind it:
// Begin waits for gamescope to come back instead (steamDown). The attempt
// judges by Steam's pid or the unit's main process start whether Steam
// came back; when the command failed, or after steamWait, it restarts the
// unit instead.
func (m *Manager) restartSteam(ctx context.Context, req *steamRequest) (steamOutcome, error) {
	if !m.op.TryLock() {
		return steamNotNow, nil
	}
	out, ok := m.steamRestartAllowed(ctx)
	var main0 time.Time
	if ok {
		var job time.Time
		job, main0 = m.h.UnitStarted(ctx, GamescopeUnit, true)
		if !job.IsZero() && job.After(req.since) {
			out, ok = steamNotNeeded, false // its prepare step ran after the change
		} else if busy, _ := m.h.Busy(ctx); busy {
			out, ok = steamNotNow, false
		}
	}
	if !ok {
		m.op.Unlock()
		return out, nil
	}
	pid := m.h.SteamPID()
	if pid == 0 {
		defer m.op.Unlock()
		log.Printf("display: restarting gamescope and Steam: %s", req.reason)
		return steamRestarted, m.h.RestartUnit(ctx, GamescopeUnit, true)
	}

	down := make(chan struct{})
	m.mu.Lock()
	m.steamDown = down
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.steamDown = nil
		m.mu.Unlock()
		close(down)
	}()
	m.op.Unlock()

	log.Printf("display: restarting Steam (pid %d): %s", pid, req.reason)
	sctx, cancel := context.WithTimeout(ctx, m.steamShutdownWait)
	err := m.h.ShutdownSteam(sctx, pid)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return steamNotNow, nil
		}
		log.Printf("display: steam -shutdown: %v; restarting gamescope instead", err)
		return m.restartGamescope(ctx)
	}
	tries := max(int(m.steamWait/max(m.steamPoll, time.Millisecond)), 1)
	for range tries {
		if !sleepCtx(ctx, m.steamPoll) {
			return steamNotNow, nil
		}
		if p := m.h.SteamPID(); p != 0 && p != pid {
			return steamRestarted, nil
		}
		if _, main := m.h.UnitStarted(ctx, GamescopeUnit, true); !main.IsZero() && !main.Equal(main0) {
			return steamRestarted, nil
		}
	}
	log.Printf("display: Steam did not shut down within %s; restarting gamescope", m.steamWait)
	return m.restartGamescope(ctx)
}

// restartGamescope restarts gamescope's unit, and Steam with it, when
// m.op is free and a restart still allowed.
func (m *Manager) restartGamescope(ctx context.Context) (steamOutcome, error) {
	if !m.op.TryLock() {
		return steamNotNow, nil
	}
	defer m.op.Unlock()
	if out, ok := m.steamRestartAllowed(ctx); !ok {
		return out, nil
	}
	return steamRestarted, m.h.RestartUnit(ctx, GamescopeUnit, true)
}

// steamRestartAllowed reports whether Steam may be restarted now, and if
// not, whether later (steamNotNow) or never for this request. Callers
// hold m.op.
func (m *Manager) steamRestartAllowed(ctx context.Context) (steamOutcome, bool) {
	m.mu.Lock()
	gaming, session, monitor := m.state == StateGaming, m.session != nil, m.physicalConnectedLocked()
	m.mu.Unlock()
	switch {
	case !gaming || monitor:
		return steamNotNeeded, false
	case session:
		return steamNotNow, false
	}
	switch m.h.UnitState(ctx, GamescopeUnit, true) {
	case "active", "reloading":
		return 0, true
	case "activating", "deactivating":
		// Starting (prepare may be reading the old steam.json right now),
		// waiting out RestartSec, or stopping: look again once it settles.
		return steamNotNow, false
	}
	return steamNotNeeded, false // down: its next start runs prepare
}

// awaitSteamBack lets a Begin that found Steam shutting down for a restart
// wait, within ctx, until the restart is over: gamescope exits with Steam
// and comes back, and Begin must switch the mode of the gamescope that
// stays. Callers hold m.op.
func (m *Manager) awaitSteamBack(ctx context.Context) {
	m.mu.Lock()
	down := m.steamDown
	m.mu.Unlock()
	if down == nil {
		return
	}
	log.Printf("display: session begin: waiting for Steam to come back from its restart")
	select {
	case <-down:
	case <-ctx.Done():
	}
}

// awaitSteamJSON gives the extensions service a moment to write this
// boot's steam.json before vosd first starts gamescope, so prepare sees
// this boot's extensions and no Steam restart has to follow. Once per
// vosd; callers hold m.op.
func (m *Manager) awaitSteamJSON(ctx context.Context) {
	if m.steamGated {
		return
	}
	m.steamGated = true
	if !sysd.WaitFor(ctx, m.steamGateWait, 100*time.Millisecond, steamJSONCurrent) {
		log.Printf("display: starting gamescope before steam.json names this boot's extensions")
	}
}

// steamJSONCurrent reports whether /var/lib/vos/ext/steam.json is this
// boot's: its set is the one the boot report names. An image without
// extensions has nothing to wait for.
func steamJSONCurrent() bool {
	if _, err := os.Stat(config.ExtCatalogPath); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	rep, err := store.LoadBootReport()
	if err != nil {
		return true
	}
	var d struct {
		Set *string `json:"set"`
	}
	if err := config.ReadJSON(config.ExtSteamPath(), &d); err != nil || d.Set == nil {
		return false
	}
	return *d.Set == rep.Set
}
