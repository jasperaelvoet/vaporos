package display

// Steam's sleep (docs/CONTRACTS.md, Display policy, "Steam's sleep").
// Steam's idle sleep and its Sleep entries ask logind to suspend, which
// it never lets vapor do: no resume follows, and gamepadui stays black in
// its suspend screen until Steam restarts. The scaler's rounds keep
// Steam's idle sleep at Never and wake an interface that went to sleep
// anyway, over Steam's debugger, apart from the scale: nothing here
// counts toward the debugger being gone or changes steam_ui.

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display/steamui"
)

const (
	// sleepIdleEvery: without a session Steam's sleep is checked at most
	// this often; during one, every round.
	sleepIdleEvery = 30 * time.Second
	// sleepSetEvery: Steam's idle sleep is set to Never at most this often
	// per Steam process.
	sleepSetEvery = time.Minute
	// Steam suspending on two reads sleepSettle apart once its preparation
	// for the suspend is over, or for sleepStuck whatever it waits for (a
	// cloud sync, a game), is stuck: the suspend it waits for never comes.
	sleepSettle = 5 * time.Second
	sleepStuck  = 2 * time.Minute
	// sleepWakeRounds: a Steam still suspending this many checks after
	// vosd woke it gets a restart.
	sleepWakeRounds = 2
)

// sleepState is what the check remembers of one Steam process (in
// scaleState, guarded by scaleMu).
type sleepState struct {
	pid    int
	lastAt time.Time // the last check, of whichever process
	// setAt is the last NeverSleep; setUnknown: one went out for settings
	// Steam did not show; setLogged: settings that were on were logged.
	setAt      time.Time
	setUnknown bool
	setLogged  bool
	// The episode: since when Steam has been seen suspending (zero while
	// it is awake), whether vosd woke it, and the checks after that which
	// still saw it suspending.
	since     time.Time
	woke      bool
	afterWake int
	asked     bool // a Steam restart was asked for this process
	errLogged bool // a failed call was logged, for this process and episode
}

// steamSleep is a round's check of Steam's sleep: in the gaming state
// while Steam runs, whatever the scale does, every round of a session
// and at most every sleepIdleEvery without one.
func (m *Manager) steamSleep(ctx context.Context) {
	m.mu.Lock()
	gaming, streaming := m.state == StateGaming, m.session != nil
	m.mu.Unlock()
	if !gaming {
		return
	}
	// The cadence first: finding Steam walks /proc.
	s := &m.sc.sleep
	now := m.now()
	if !streaming && !s.lastAt.IsZero() && now.Sub(s.lastAt) < sleepIdleEvery {
		return
	}
	s.lastAt = now
	pid := m.h.SteamPID()
	if pid == 0 {
		return
	}
	if pid != s.pid {
		*s = sleepState{pid: pid, lastAt: now}
	}
	ui := m.h.SteamUI()
	cctx, cancel := context.WithTimeout(ctx, m.scaleCall)
	st, err := ui.Sleep(cctx)
	cancel()
	if err != nil {
		// Steam starting, or not Steam on the port: the scale's own
		// reads say so.
		if !errors.Is(err, steamui.ErrNoDebugger) && !errors.Is(err, steamui.ErrNoTarget) {
			m.sleepFailed("reading Steam's sleep", err)
		}
		return
	}
	if !m.sleepCurrent(pid) {
		return
	}
	m.keepSleepOff(ctx, ui, st, now)
	m.wakeSteam(ctx, ui, st, pid, now)
}

// sleepCurrent reports whether what a read of Steam's sleep saw may still
// be acted on: the same Steam in the gaming state, and no Begin or policy
// apply holding the display meanwhile.
func (m *Manager) sleepCurrent(pid int) bool {
	m.mu.Lock()
	gaming := m.state == StateGaming
	m.mu.Unlock()
	if !gaming || m.h.SteamPID() != pid || !m.op.TryLock() {
		return false
	}
	m.op.Unlock()
	return true
}

// keepSleepOff sets Steam's idle sleep to Never when a read shows it on,
// or once per Steam process when Steam does not show it, at most every
// sleepSetEvery: VaporOS's idle shutdown is the one idle policy.
func (m *Manager) keepSleepOff(ctx context.Context, ui steamUI, st steamui.SleepState, now time.Time) {
	s := &m.sc.sleep
	on := st.AC > 0 || st.Battery > 0
	unknown := !on && (st.AC < 0 || st.Battery < 0)
	switch {
	case !st.CanSet, !on && !unknown, unknown && s.setUnknown:
		return
	case !s.setAt.IsZero() && now.Sub(s.setAt) < sleepSetEvery:
		return
	}
	if unknown {
		s.setUnknown = true
	} else if !s.setLogged {
		s.setLogged = true
		log.Printf("display: Steam's idle sleep is on (AC %s, battery %s), which VaporOS can't do; setting it to Never",
			idleSecs(st.AC), idleSecs(st.Battery))
	}
	s.setAt = now
	cctx, cancel := context.WithTimeout(ctx, m.scaleCall)
	err := ui.NeverSleep(cctx)
	cancel()
	if err != nil {
		m.sleepFailed("setting Steam's idle sleep to Never", err)
	}
}

func idleSecs(v int) string {
	switch {
	case v < 0:
		return "not shown"
	case v == 0:
		return "never"
	}
	return strconv.Itoa(v) + " s"
}

// wakeSteam takes Steam's interface out of its suspend screen once it is
// stuck there, once per episode (until a read shows Steam awake). When
// Steam cannot be woken, or still suspends sleepWakeRounds checks after
// the wake, it asks for a Steam restart.
func (m *Manager) wakeSteam(ctx context.Context, ui steamUI, st steamui.SleepState, pid int, now time.Time) {
	s := &m.sc.sleep
	if !st.Suspending {
		if !s.since.IsZero() {
			s.since, s.woke, s.afterWake, s.errLogged = time.Time{}, false, 0, false
		}
		return
	}
	if s.since.IsZero() {
		s.since, s.errLogged = now, false
		return
	}
	held := now.Sub(s.since)
	over := st.Progress == steamui.SuspendInvalid || st.Progress == steamui.SuspendComplete
	if !(over && held >= sleepSettle) && held < sleepStuck {
		return
	}
	switch {
	case s.woke:
		if s.afterWake++; s.afterWake >= sleepWakeRounds {
			m.restartAsleep(pid, "it still shows its suspend screen after vosd woke it")
		}
		return
	case !st.CanResume:
		m.restartAsleep(pid, "it has no InitiateResume")
		return
	case !m.sleepCurrent(pid):
		return
	}
	cctx, cancel := context.WithTimeout(ctx, m.scaleCall)
	r, err := ui.Resume(cctx)
	cancel()
	switch {
	case errors.Is(err, steamui.ErrNoSleepAPI):
		m.restartAsleep(pid, "it has no InitiateResume")
	case err != nil:
		// It may have gone through: the next checks tell, and a Steam
		// still suspending then is restarted.
		s.woke = true
		m.sleepFailed("waking Steam's interface", err)
	case r == steamui.Awake:
		s.since = time.Time{} // it woke meanwhile
	default:
		s.woke = true
		log.Printf("display: Steam's interface was stuck asleep (VaporOS can't suspend); woke it")
	}
}

// restartAsleep asks for a Steam restart for a Steam stuck asleep, once
// per Steam process; RestartSteam waits for a quiet moment.
func (m *Manager) restartAsleep(pid int, why string) {
	s := &m.sc.sleep
	if s.asked {
		return
	}
	s.asked = true
	log.Printf("display: Steam (pid %d) is stuck asleep: %s; asking for a Steam restart", pid, why)
	m.RestartSteam("Steam is stuck asleep", false)
}

// sleepFailed logs a failed call of the check, once per Steam process and
// episode.
func (m *Manager) sleepFailed(what string, err error) {
	s := &m.sc.sleep
	if s.errLogged {
		return
	}
	s.errLogged = true
	log.Printf("display: %s: %v", what, err)
}
