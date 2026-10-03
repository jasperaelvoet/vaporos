package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// The auto-restart (docs/CONTRACTS.md "Extensions", Control center): while
// a restart would try the pending set, VaporOS restarts by itself once the
// PC has been idle for autoIdle, with the boot settled, unless an idle
// shutdown comes first. A circuit breaker in autorestart.json allows one
// auto-restart per pending set (two when another VaporOS version booted in
// between: an update's trial does not try it) and autoPerDay a day.
const (
	autoIdle     = 2 * time.Minute
	autoBootAge  = 5 * time.Minute
	autoShutdown = 5 * time.Minute // an idle shutdown due this soon goes first
	autoPerDay   = 3
	autoKeep     = 32 // records kept in the file
	maxAutoFile  = 64 << 10
)

// The auto-restart runs in the idle policy's pass, which must go on: the
// wait for each lock and asking systemd whether `vos health` passed each
// get their own bound, and the reboot bounds itself (system.Service.Reboot,
// GoingDown). Variables for tests.
var (
	autoLockWait   = 5 * time.Second
	autoHealthWait = 5 * time.Second
)

// healthUnit is `vos health`'s unit; RemainAfterExit keeps it active once
// it passed.
const healthUnit = "vos-health.service"

// uptimePath is read for the boot's age; a variable for tests.
var uptimePath = "/proc/uptime"

// IdleTick is what one pass of the idle policy saw (power.Tick), fresh.
type IdleTick struct {
	Idle        time.Duration // how long nothing has kept the PC awake; 0 while busy
	ShutdownIn  time.Duration // until idle shutdown; negative while none is due
	PoweringOff bool          // an idle shutdown is on its way
}

// NextBoot is the VaporOS a restart starts instead of the running one: a
// staged update, or a rollback (an older version, or the same one again).
// Version is "" when a restart starts the running one.
type NextBoot struct {
	Version  string
	Rollback bool
}

type autoState struct {
	reboot     func(ctx context.Context, message string) (bool, error)
	nextBoot   func() NextBoot
	healthDone func(ctx context.Context) bool
	uptime     func() (time.Duration, error)
	tripped    string // the fingerprint the breaker last held back, logged once
}

func newAutoState() autoState {
	return autoState{
		healthDone: func(ctx context.Context) bool { return sysd.ActiveState(ctx, healthUnit, false) == "active" },
		uptime:     readUptime,
	}
}

// SetAutoRestart wires the auto-restart: reboot restarts the PC behind the
// guard the web UI's restart and power off share (system.Service.Reboot),
// and nextBoot names the VaporOS a restart starts instead of this one (a
// staged update or rollback), if any. Without it VaporOS never restarts by
// itself.
func (s *Service) SetAutoRestart(reboot func(ctx context.Context, message string) (bool, error), nextBoot func() NextBoot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cc.auto.reboot, s.cc.auto.nextBoot = reboot, nextBoot
}

// RestartNeeded reports whether a restart would try the pending set (the
// /status restart kind "extensions"): not while the next start mounts no
// extension (skip-once).
func (s *Service) RestartNeeded() bool {
	s.mu.Lock()
	restart := s.view.restart
	s.mu.Unlock()
	return restart && !skipOnce()
}

type autoRecord struct {
	At          time.Time `json:"at"`
	Set         string    `json:"set"`
	Fingerprint string    `json:"fingerprint"`
	Version     string    `json:"version"` // the booted VaporOS version it restarted from
}

type autoFile struct {
	Restarts []autoRecord `json:"restarts"`
}

func loadAuto() autoFile {
	var f autoFile
	fh, err := os.Open(config.ExtAutoRestartPath())
	if errors.Is(err, fs.ErrNotExist) {
		return f
	}
	if err == nil {
		err = json.NewDecoder(io.LimitReader(fh, maxAutoFile)).Decode(&f)
		fh.Close()
	}
	if err != nil {
		// The next auto-restart writes a good file.
		log.Printf("extensions: %s: %v", config.ExtAutoRestartPath(), err)
	}
	return f
}

func saveAuto(f autoFile) error {
	slices.SortFunc(f.Restarts, func(a, b autoRecord) int { return a.At.Compare(b.At) })
	if n := len(f.Restarts); n > autoKeep {
		f.Restarts = f.Restarts[n-autoKeep:]
	}
	return config.WriteJSONAtomic(config.ExtAutoRestartPath(), f, 0o644)
}

// allows reports whether the breaker lets the set with fingerprint fp
// restart the PC now, from version: fewer than autoPerDay auto-restarts in
// the last 24 hours (one timed in the future counts), and none for fp yet,
// or one from another version.
func (f autoFile) allows(fp, version string, at time.Time) bool {
	day := 0
	var same []autoRecord
	for _, r := range f.Restarts {
		if at.Sub(r.At) < 24*time.Hour {
			day++
		}
		if r.Fingerprint == fp {
			same = append(same, r)
		}
	}
	switch {
	case day >= autoPerDay:
		return false
	case len(same) == 0:
		return true
	}
	return len(same) == 1 && same[0].Version != version
}

func pendingFingerprint(v *view) string {
	if v.pending == nil {
		return ""
	}
	return store.Fingerprint(store.Pairs(v.cat, v.pending.IDs), v.pending.Options)
}

// autoAllowed is the document's restart.auto: VaporOS may still restart by
// itself for the pending set (not while the next start leaves the
// extensions out).
func (s *Service) autoAllowed(v *view) bool {
	s.mu.Lock()
	wired := s.cc.auto.reboot != nil
	s.mu.Unlock()
	return wired && v.restart && v.pending != nil && !skipOnce() &&
		loadAuto().allows(pendingFingerprint(v), v.version, now())
}

// IdleTick runs in the idle policy's pass, with what it just saw. It
// restarts the PC when a restart would try the pending set, the PC has
// been idle for autoIdle, `vos health` finished this boot (or the boot is
// older than autoBootAge), nothing is powering it off, no idle shutdown is
// due within autoShutdown, the next start does not leave the extensions
// out (skip-once), and the breaker allows it. The decision is taken again
// under the update lock and the store lock, which the reboot holds, so no
// stage or set change slips in between.
func (s *Service) IdleTick(ctx context.Context, t IdleTick) {
	if t.PoweringOff || t.Idle < autoIdle || (t.ShutdownIn >= 0 && t.ShutdownIn <= autoShutdown) {
		return
	}
	s.mu.Lock()
	a, restart := s.cc.auto, s.view.restart
	s.mu.Unlock()
	if a.reboot == nil || !restart || skipOnce() || !s.bootSettled(ctx, a) {
		return
	}
	uctx, cancel := context.WithTimeout(ctx, autoLockWait)
	defer cancel()
	err := update.WithLock(uctx, func() error {
		sctx, cancel := context.WithTimeout(ctx, autoLockWait)
		defer cancel()
		unlock, err := store.Lock(sctx)
		if err != nil {
			return err
		}
		defer unlock()
		return s.autoRestart(ctx, a)
	})
	if err != nil {
		log.Printf("extensions: auto-restart: %v", err)
	}
}

// bootSettled reports whether `vos health` finished this boot, or the boot
// is old enough that it never will.
func (s *Service) bootSettled(ctx context.Context, a autoState) bool {
	if up, err := a.uptime(); err == nil && up > autoBootAge {
		return true
	}
	hctx, cancel := context.WithTimeout(ctx, autoHealthWait)
	defer cancel()
	return a.healthDone(hctx)
}

// autoRestart is the decision under the locks, on the store as it is now.
func (s *Service) autoRestart(ctx context.Context, a autoState) error {
	rep, err := store.LoadBootReport()
	if err != nil {
		return err
	}
	pending, err := store.Pending()
	if err != nil {
		return err
	}
	cat := s.catalog()
	if skipOnce() || !store.RestartNeeded(rep, pending, func(id string) bool {
		e, ok := cat.Get(id)
		return ok && sealed(e)
	}) {
		return nil
	}
	s.mu.Lock()
	version := s.view.version
	s.mu.Unlock()
	fp := store.Fingerprint(store.Pairs(cat, pending.IDs), pending.Options)
	f := loadAuto()
	at := now()
	if !f.allows(fp, version, at) {
		s.mu.Lock()
		first := s.cc.auto.tripped != fp
		s.cc.auto.tripped = fp
		s.mu.Unlock()
		if first {
			log.Printf("extensions: not restarting by itself for set %s again; it waits for a restart from the control center", pending.Name)
			s.changed()
		}
		return nil
	}
	rec := autoRecord{At: at.UTC().Truncate(time.Second), Set: pending.Name, Fingerprint: fp, Version: version}
	if err := saveAuto(autoFile{Restarts: append(slices.Clone(f.Restarts), rec)}); err != nil {
		return fmt.Errorf("recording it: %w", err)
	}
	var next NextBoot
	if a.nextBoot != nil {
		next = a.nextBoot()
	}
	msg := s.restartMessage(rep, pending, next)
	log.Printf("extensions: idle for %v with set %s pending: %s", autoIdle, pending.Name, msg)
	ok, err := a.reboot(ctx, msg)
	if !ok || err != nil {
		// Not restarted: the record must not hold the next try back.
		if serr := saveAuto(f); serr != nil {
			log.Printf("extensions: %v", serr)
		}
	}
	return err
}

// restartMessage is what the welcome screen and the control center say
// as the PC restarts: what it adds or removes, or, when a staged update or
// rollback boots first, that the change follows the restart after.
func (s *Service) restartMessage(rep *store.BootReport, pending *store.Set, next NextBoot) string {
	var adding, removing []string
	for _, id := range pending.IDs {
		if !rep.IsMounted(id) {
			adding = append(adding, s.name(id))
		}
	}
	for _, m := range rep.Mounted {
		if !slices.Contains(pending.IDs, m.ID) {
			removing = append(removing, s.name(m.ID))
		}
	}
	if next.Version != "" {
		var after []string
		if len(adding) > 0 {
			after = append(after, joinNames(adding)+" "+be(adding)+" added")
		}
		if len(removing) > 0 {
			after = append(after, joinNames(removing)+" "+be(removing)+" removed")
		}
		tail := "the extension settings change"
		if len(after) > 0 {
			tail = joinNames(after)
		}
		verb := "install"
		if next.Rollback {
			verb = "go back to"
		}
		return fmt.Sprintf("Restarting to %s VaporOS %s; %s after the next restart", verb, next.Version, tail)
	}
	var parts []string
	if len(adding) > 0 {
		parts = append(parts, "adding "+joinNames(adding))
	}
	if len(removing) > 0 {
		parts = append(parts, "removing "+joinNames(removing))
	}
	if len(parts) == 0 {
		return "Restarting to finish changing extension settings"
	}
	return "Restarting to finish " + joinNames(parts)
}

func be(names []string) string {
	if len(names) == 1 {
		return "is"
	}
	return "are"
}

// readUptime is how long ago the kernel started.
func readUptime() (time.Duration, error) {
	b, err := os.ReadFile(uptimePath)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, errors.New("empty " + uptimePath)
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}
