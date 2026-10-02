package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/power"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// fakeExt records what wireAutoRestart hands the extensions.
type fakeExt struct {
	reboot   func(context.Context, string) (bool, error)
	nextBoot func() extensions.NextBoot
	ticks    []extensions.IdleTick
}

func (f *fakeExt) SetAutoRestart(reboot func(context.Context, string) (bool, error), nextBoot func() extensions.NextBoot) {
	f.reboot, f.nextBoot = reboot, nextBoot
}

func (f *fakeExt) IdleTick(_ context.Context, t extensions.IdleTick) { f.ticks = append(f.ticks, t) }

// The idle policy's passes reach the extensions with what they saw, the
// reboot is the system service's, and a staged update or rollback in the
// update service's view is the VaporOS a restart starts first.
func TestWireAutoRestart(t *testing.T) {
	var ext fakeExt
	var tick func(context.Context, power.Tick)
	rebooted := ""
	view := update.View{State: update.State{Booted: "20260929.120000"}, BootedSlot: "a"}
	wireAutoRestart(&ext, func(_ context.Context, msg string) (bool, error) { rebooted = msg; return true, nil },
		func() update.View { return view }, func(f func(context.Context, power.Tick)) { tick = f })

	if tick == nil || ext.reboot == nil || ext.nextBoot == nil {
		t.Fatal("not wired")
	}
	tick(t.Context(), power.Tick{Idle: 3 * time.Minute, ShutdownIn: -1, PoweringOff: true})
	if want := (extensions.IdleTick{Idle: 3 * time.Minute, ShutdownIn: -1, PoweringOff: true}); len(ext.ticks) != 1 || ext.ticks[0] != want {
		t.Fatalf("ticks %+v", ext.ticks)
	}
	if ok, _ := ext.reboot(t.Context(), "Restarting"); !ok || rebooted != "Restarting" {
		t.Fatal("the reboot is not the one given")
	}
	for _, c := range []struct {
		next string
		want extensions.NextBoot
	}{
		{"", extensions.NextBoot{}},
		{"20260930.101010", extensions.NextBoot{Version: "20260930.101010"}},
		{"20260928.090000", extensions.NextBoot{Version: "20260928.090000", Rollback: true}},
		{"20260929.120000", extensions.NextBoot{Version: "20260929.120000", Rollback: true}},
	} {
		view.NextBoot = nil
		if c.next != "" {
			view.NextBoot = &update.NextBoot{Slot: "b", Version: c.next}
		}
		if got := ext.nextBoot(); got != c.want {
			t.Errorf("next boot %q: %+v, want %+v", c.next, got, c.want)
		}
	}
}
