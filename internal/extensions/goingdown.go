package extensions

import (
	"context"
	"log"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Going back (docs/CONTRACTS.md "Extensions", Steam): an image built
// before extensions has neither `vos ext launch` nor proton-cachyos-slr,
// and nothing in it runs `vos steam prepare`, so Steam would keep mapping
// games to a tool it lacks and starting VaporOS's shortcuts through a
// command it lacks. What GoingDown reads and runs, and each step's bound;
// variables for tests.
var (
	readBootEntries = func() ([]boot.Entry, error) {
		if err := boot.EnsureESP(config.ESP); err != nil {
			return nil, err
		}
		return boot.Entries(config.ESP)
	}
	stopGamescope = func(ctx context.Context) error { return sysd.UserSystemctl(ctx, "stop", gamescopeUnit) }
	unwrapAsGamer = func(ctx context.Context) (string, error) {
		return sysd.AsGamer(ctx, vosBinary, "steam", "prepare", "--unwrap")
	}
	downReadWait    = 5 * time.Second
	downHoldWait    = 5 * time.Second
	downStopWait    = 30 * time.Second
	downPrepareWait = 10 * time.Second
	// downHoldFor is how long the display stays held after the restart
	// was asked for, should the PC still run.
	downHoldFor = 2 * time.Minute
)

// SetDisplayHold sets what keeps the display policy from starting or
// stopping units (display.Manager.HoldUnits) while GoingDown takes Steam
// down. Without it GoingDown holds nothing.
func (s *Service) SetDisplayHold(hold func(ctx context.Context) (release func(), err error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holdDisplay = hold
}

// GoingDown runs act, a restart or power off that vosd performs, and
// returns its error. When the next start boots the other slot and that
// image has no `vos ext launch`, it first holds the display's units, stops
// vos-gamescope.service (Steam writes its files and exits) and runs `vos
// steam prepare --unwrap` as vapor, each within its bound; act runs
// whatever they did. The hold ends when act fails, or downHoldFor after
// it: a gamescope started meanwhile would apply steam.json again, at its
// start and at its stop on the way down.
func (s *Service) GoingDown(act func() error) error {
	next := goingBack()
	if next == nil {
		return act()
	}
	log.Printf("extensions: the next start boots VaporOS %s (slot %s), built before extensions; taking what VaporOS set in Steam out first", next.Version, next.Slot)
	release := s.holdUnits()
	ctx, cancel := context.WithTimeout(context.Background(), downStopWait)
	err := stopGamescope(ctx)
	cancel()
	if err != nil {
		log.Printf("extensions: stopping %s: %v", gamescopeUnit, err)
	} else {
		log.Printf("extensions: stopped %s", gamescopeUnit)
	}
	ctx, cancel = context.WithTimeout(context.Background(), downPrepareWait)
	unwrap{prepare: unwrapAsGamer}.runPrepare(ctx)
	cancel()
	if err := act(); err != nil {
		release()
		return err
	}
	time.AfterFunc(downHoldFor, release)
	return nil
}

// goingBack returns the entry the next start boots when that is the other
// slot's and its image has no `vos ext launch`, else nil. It reads the ESP
// now, never a cached reading; one that fails or outlasts downReadWait
// counts as nil.
func goingBack() *boot.Entry {
	booted := config.BootedSlot()
	if booted != "a" && booted != "b" {
		return nil
	}
	other := config.OtherSlot(booted)
	type reading struct {
		back *boot.Entry
		err  error
	}
	read, done := readBootEntries, make(chan reading, 1)
	go func() {
		es, err := read()
		var r reading
		if r.err = err; err == nil {
			if e := boot.NextEntry(es, booted); e != nil && e.Slot == other && !hasDispatcher(other, e) {
				r.back = e
			}
		}
		done <- r
	}()
	timer := time.NewTimer(downReadWait)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			log.Printf("extensions: which VaporOS starts next: %v; Steam is left as it is", r.err)
		}
		return r.back
	case <-timer.C:
		log.Printf("extensions: which VaporOS starts next: the ESP did not answer within %v; Steam is left as it is", downReadWait)
		return nil
	}
}

// holdUnits holds the display's units, waiting at most downHoldWait for a
// switch under way; without the hold it goes on all the same.
func (s *Service) holdUnits() (release func()) {
	s.mu.Lock()
	hold := s.holdDisplay
	s.mu.Unlock()
	if hold == nil {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), downHoldWait)
	defer cancel()
	release, err := hold(ctx)
	if err != nil {
		log.Printf("extensions: the display kept switching for %v (%v); stopping Steam without holding it", downHoldWait, err)
		return func() {}
	}
	return release
}
