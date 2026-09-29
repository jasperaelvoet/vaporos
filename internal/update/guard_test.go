package update

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// rolledBack is setup after `vos rollback` and the reboot into slot b.
func rolledBack(t *testing.T) *testEnv {
	e := setup(t)
	if _, err := Rollback(false); err != nil {
		t.Fatal(err)
	}
	e.bootSlot("b")
	e.write(config.ImageInfoPath, `{"version":"`+oldIdleVersion+`","channel":"main","rollback_index":50}`)
	if _, err := Reconcile(); err != nil {
		t.Fatal(err)
	}
	return e
}

// The version the user rolled back from is still the newest on the
// channel: the timer must not stage it again.
func TestAutoStageKeepsRollback(t *testing.T) {
	e := rolledBack(t)
	img := e.makeImage(bootedVersion, bootedRollback, 1000, nil)
	s := NewService(e.cfg(e.srcDir(img)))
	s.autoStage(context.Background())
	st := e.state()
	if st.Staged != nil || st.Available != nil {
		t.Fatalf("staged the version rolled back from: %+v", st)
	}
	if a := e.entry("a"); a == nil || a.Name() != "vos-"+bootedVersion+"+0-1.conf" {
		t.Fatalf("slot a: %+v", a)
	}
	res, err := Check(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if err != nil || !errors.Is(res.Reason, ErrHeld) {
		t.Fatalf("check: %+v %v", res, err)
	}

	// A newer build passes the hold and lifts it.
	next := e.makeImage(newVersion, 200, 1000, nil)
	s = NewService(e.cfg(e.srcDir(next)))
	s.autoStage(context.Background())
	if st := e.state(); st.Staged == nil || st.Staged.Version != newVersion || st.Held != nil {
		t.Fatalf("newer build: %+v", st)
	}
}

// Picking the held version explicitly (the UI's version list) installs it.
func TestStageHeldVersionExplicitly(t *testing.T) {
	e := rolledBack(t)
	img := e.makeImage(bootedVersion, bootedRollback, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); !errors.Is(err, ErrHeld) {
		t.Fatalf("newest on the channel: %v", err)
	}
	opts := Options{Version: bootedVersion, AllowDowngrade: true}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), opts); err != nil {
		t.Fatal(err)
	}
	if st := e.state(); st.Staged == nil || st.Staged.Version != bootedVersion || st.Held != nil {
		t.Fatalf("state %+v", st)
	}
}

// An explicit downgrade holds the version it left, like a rollback.
func TestDowngradeHolds(t *testing.T) {
	e := setup(t)
	old := e.makeImage("20260815.000000", bootedRollback-1, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(old)), Options{AllowDowngrade: true}); err != nil {
		t.Fatal(err)
	}
	if h := e.state().Held; h == nil || h.Version != bootedVersion {
		t.Fatalf("held %+v", h)
	}
}

// While the running entry is on trial, the idle slot is its only
// known-good fallback: a stage must not overwrite it.
func TestStageRefusesOnTrial(t *testing.T) {
	e := setup(t)
	a := e.entry("a")
	trial := filepath.Join(filepath.Dir(a.Path), "vos-"+bootedVersion+"+2-1.conf")
	e.must(os.Rename(a.Path, trial))
	img := e.makeImage(newVersion, 200, 1000, nil)
	_, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if !errors.Is(err, ErrOnTrial) {
		t.Fatalf("on trial: %v", err)
	}
	if b := e.entry("b"); b == nil || b.Version != oldIdleVersion {
		t.Fatalf("slot b: %+v", b)
	}
	if st := e.state(); st.LastError != "" {
		t.Fatalf("a passing refusal became last_error %q", st.LastError)
	}
	// Blessed: go ahead.
	e.must(os.Rename(trial, a.Path))
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
}

// After a rollback and before the restart, a stage would delete the
// rollback's target; forced, it keeps the running entry a fallback.
func TestStageAfterPendingRollback(t *testing.T) {
	e := setup(t)
	if _, err := Rollback(false); err != nil {
		t.Fatal(err)
	}
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); !errors.Is(err, ErrRollbackPending) {
		t.Fatalf("pending rollback: %v", err)
	}
	if b := e.entry("b"); b == nil || b.Version != oldIdleVersion {
		t.Fatalf("slot b: %+v", b)
	}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	// The running entry, left at +0-1 by the rollback, is blessed again:
	// otherwise a failing new image would sort first among the bad ones.
	if a := e.entry("a"); a == nil || a.Name() != "vos-"+bootedVersion+".conf" {
		t.Fatalf("slot a: %+v", a)
	}
	if got := e.failUntilFallback(3); got.Slot != "a" {
		t.Fatalf("after the new image failed: boots %s", got.Name())
	}
}

// With every entry out of tries (the last resort), staging a fix is how
// the machine recovers, so it is allowed.
func TestStageLastResort(t *testing.T) {
	e := setup(t)
	for _, slot := range []string{"a", "b"} {
		en := e.entry(slot)
		e.must(os.Rename(en.Path, strings.TrimSuffix(en.Path, ".conf")+"+0-3.conf"))
	}
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	if got := e.failUntilFallback(3); got.Slot != "a" {
		t.Fatalf("after the new image failed: boots %s", got.Name())
	}
}

func TestStageClearsLoaderOverrides(t *testing.T) {
	e := setup(t)
	vendor := "-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
	pin := filepath.Join(boot.EFIVarsDir, "LoaderEntryDefault"+vendor)
	oneshot := filepath.Join(boot.EFIVarsDir, "LoaderEntryOneShot"+vendor)
	e.write(pin, "vos-"+bootedVersion+".conf")
	e.write(oneshot, "x")
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pin); !os.IsNotExist(err) {
		t.Fatalf("a pinned default entry survived the stage: %v", err)
	}
	if _, err := os.Stat(oneshot); err != nil {
		t.Fatalf("the one-shot entry was removed: %v", err)
	}
}

// gptDisk writes a disk image with a GPT header naming guid.
func (e *testEnv) gptDisk(path string, guid [16]byte) {
	e.t.Helper()
	h := make([]byte, 1024)
	copy(h[512:], "EFI PART")
	binary.LittleEndian.PutUint32(h[512+12:], 92)
	copy(h[512+56:], guid[:])
	binary.LittleEndian.PutUint32(h[512+16:], crc32.ChecksumIEEE(h[512:512+92]))
	e.write(path, string(h))
}

// addDisk adds a whole disk with the VaporOS partitions to the fake sysfs
// and /dev; the by-partlabel links are left as they are.
func (e *testEnv) addDisk(disk string, guid [16]byte) {
	e.t.Helper()
	dir := filepath.Join(boot.SysClassBlock, disk)
	e.gptDisk(filepath.Join(boot.DevDir, disk), guid)
	for i, label := range []string{"vos_esp", "vos_a", "vos_b", "vos_data"} {
		part := fmt.Sprintf("%s%d", disk, i+1)
		e.write(filepath.Join(dir, part, "partition"), fmt.Sprint(i+1))
		e.write(filepath.Join(dir, part, "uevent"), "PARTNAME="+label+"\n")
		e.must(os.WriteFile(filepath.Join(boot.DevDir, part), make([]byte, slotSize), 0o644))
	}
}

// With a second VaporOS disk attached, the update goes to the disk
// vos.disk names, not wherever /dev/disk/by-partlabel points.
func TestStageToBootDisk(t *testing.T) {
	e := setup(t)
	mine, theirs := [16]byte{1, 2, 3}, [16]byte{9, 9, 9}
	e.addDisk("sda", mine)
	e.addDisk("sdb", theirs)
	guid := "00030201-0000-0000-0000-000000000000"
	e.write(config.ProcCmdline, "vos.slot=a vos.disk="+guid+" "+imageCmdline+"\n")
	labelled, err := os.ReadFile(filepath.Join(boot.PartLabelDir, "vos_b"))
	e.must(err)

	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(boot.DevDir, "sda3"))
	if !strings.HasPrefix(string(got), string(img.root())) {
		t.Fatal("the boot disk's slot b was not written")
	}
	for _, other := range []string{filepath.Join(boot.DevDir, "sdb3"), filepath.Join(boot.PartLabelDir, "vos_b")} {
		b, _ := os.ReadFile(other)
		if strings.HasPrefix(string(b), string(img.root())) {
			t.Fatalf("%s was written", other)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(boot.PartLabelDir, "vos_b")); string(b) != string(labelled) {
		t.Fatal("the by-label slot changed")
	}

	// A vos.disk no disk has: refused, nothing written.
	e = setup(t)
	e.addDisk("sdb", theirs)
	e.write(config.ProcCmdline, "vos.slot=a vos.disk="+guid+" "+imageCmdline+"\n")
	img = e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err == nil || !strings.Contains(err.Error(), guid) {
		t.Fatalf("unknown boot disk: %v", err)
	}
	if b := e.entry("b"); b == nil || b.Version != oldIdleVersion {
		t.Fatalf("slot b entry: %+v", b)
	}
}

func TestApplyMachineCmdline(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := ApplyMachineCmdline(ctx, "video=DP-2:e vos.disk=abc"); err != nil {
		t.Fatal(err)
	}
	// A display change keeps the boot disk and replaces the rest.
	if err := ApplyMachineCmdline(ctx, " video=HDMI-A-1:e firmware_class.path=/x "); err != nil {
		t.Fatal(err)
	}
	want := "video=HDMI-A-1:e firmware_class.path=/x vos.disk=abc"
	if got := boot.MachineCmdline(); got != want {
		t.Fatalf("machine cmdline %q, want %q", got, want)
	}
	for _, slot := range []string{"a", "b"} {
		if en := e.entry(slot); en.Options != "vos.slot="+slot+" "+imageCmdline+" "+want {
			t.Fatalf("slot %s options %q", slot, en.Options)
		}
	}
	if err := ApplyMachineCmdline(ctx, "a\nb"); err == nil {
		t.Fatal("accepted a line break")
	}
	// It waits for a running update, until ctx gives up.
	l, err := lockFile(updateLockPath(), false)
	e.must(err)
	defer l.Unlock()
	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := ApplyMachineCmdline(tctx, "video=DP-3:e"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("while an update runs: %v", err)
	}
	if got := boot.MachineCmdline(); got != want {
		t.Fatalf("changed while locked: %q", got)
	}
}

// Busy probes the update lock by taking it for an instant; a stage or a
// rollback that collides with a probe must not fail with ErrBusy.
func TestLockOutlastsAProbe(t *testing.T) {
	e := setup(t)
	l, err := lockFile(updateLockPath(), false)
	e.must(err)
	go func() {
		time.Sleep(30 * time.Millisecond)
		l.Unlock()
	}()
	if _, err := Rollback(false); err != nil {
		t.Fatalf("rollback next to a probe: %v", err)
	}
}

// A stage started from the web UI finishes (or stops) before Run returns,
// so vosd never exits between the boot entry and its record.
func TestRunWaitsForBackgroundStage(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	s := NewService(e.cfg(e.srcDir(img)))
	oldFirst := firstCheckDelay
	firstCheckDelay = time.Hour
	defer func() { firstCheckDelay = oldFirst }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for s.lifetime() != ctx {
		time.Sleep(time.Millisecond)
	}
	started, release := make(chan struct{}), make(chan struct{})
	e.must(s.StartStage(Options{Accepted: func(*manifest.Manifest, string) {
		close(started)
		<-release
	}}))
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while a stage was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never returned")
	}
	if err := s.StartStage(Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a stage started after shutdown: %v", err)
	}
	if st := e.state(); st.Staged != nil {
		t.Fatalf("a stage cancelled before the ESP was recorded: %+v", st)
	}
}
