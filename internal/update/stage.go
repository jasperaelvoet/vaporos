package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

var (
	// WorkDir is where kernel and initrd wait between download and ESP.
	// It is on the data partition, so the ESP only ever receives verified
	// files.
	WorkDir = "/var/tmp"
	// syncEvery bounds the unwritten data the page cache holds while a
	// slot is written, so progress follows the disk and memory stays free.
	syncEvery int64 = 256 << 20
)

// SlotDevice is the partition of slot ("a" or "b") on the disk this system
// booted from, the one vos.disk= names (see boot.Partition). Another disk
// with VaporOS partitions must never receive the update.
func SlotDevice(slot string) (string, error) { return boot.Partition("vos_" + slot) }

// Progress is the update.progress event (docs/CONTRACTS.md). During Stage,
// Percent covers the whole update while Bytes and Total count the current
// phase: check, download (kernel and initrd), write (root into the slot),
// verify (read-back), install (ESP), done; or error, with Error set.
type Progress struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Bytes   int64  `json:"bytes"`
	Total   int64  `json:"total"`
	Version string `json:"version"`
	Error   string `json:"error,omitempty"`
}

// Options select what Stage and Check fetch and what they accept.
type Options struct {
	From           string // source spec (see OpenSource); "" = config.update.source
	Channel        string // OCI tag; "" = config.update.channel
	Version        string // exactly this version (also its OCI tag); "" = newest on the channel
	Force          bool   // accept the running version, older ones and failed ones
	AllowDowngrade bool   // accept a lower rollback_index
	Progress       func(Progress)
	// Accepted runs once the manifest has passed every check, before
	// anything is written.
	Accepted func(m *manifest.Manifest, slot string)
}

var (
	ErrUpToDate      = errors.New("already up to date")
	ErrAlreadyStaged = errors.New("already staged")
	ErrNotNewer      = errors.New("not newer than the running version")
	ErrFailedBefore  = errors.New("this version failed to start before")
	ErrTooBig        = errors.New("the image does not fit in the slot")
	ErrBusy          = errors.New("another update is in progress")
	ErrLive          = errors.New("updates are for installed systems, not the live ISO")
	// ErrHeld: the user rolled back (or went down) from this version, or
	// one newer; only picking it explicitly brings it back.
	ErrHeld = errors.New("held back after a rollback")
	// ErrOnTrial: the running version has not passed its first boot yet.
	// Its fallback is the idle slot, which a stage would overwrite.
	ErrOnTrial = errors.New("the running version is still on trial; try again once it has fully started")
	// ErrRollbackPending: the other slot boots next (a rollback or a
	// downgrade waits for a restart), and a stage would overwrite it.
	ErrRollbackPending = errors.New("the other slot starts next; restart first")
)

// IsBenign reports whether err means "nothing to do" rather than a failure.
func IsBenign(err error) bool {
	return errors.Is(err, ErrUpToDate) || errors.Is(err, ErrAlreadyStaged)
}

// isRejection reports whether err is a manifest this machine does not take.
func isRejection(err error) bool {
	return errors.Is(err, ErrUpToDate) || errors.Is(err, ErrNotNewer) || errors.Is(err, ErrFailedBefore) ||
		errors.Is(err, ErrHeld)
}

// isNotNow reports whether err refuses a stage only for the moment; it is
// not recorded as last_error, which would outlive the reason.
func isNotNow(err error) bool {
	return errors.Is(err, ErrOnTrial) || errors.Is(err, ErrRollbackPending)
}

// sourceFor opens the source opts or the configuration name.
func sourceFor(cfg *config.Config, opts Options) (*Source, error) {
	spec := opts.From
	if spec == "" {
		spec = cfg.Update.Source
	}
	if spec == "" {
		spec = config.DefaultUpdateSrc
	}
	ref := opts.Channel
	if ref == "" {
		ref = cfg.Update.Channel
	}
	if opts.Version != "" {
		ref = opts.Version // CI tags every image with its version too
	}
	return OpenSource(spec, ref)
}

// accept applies docs/CONTRACTS.md "Acceptance" beyond the signature and
// schema checks, which Source.Manifest already did.
func accept(m *manifest.Manifest, booted *config.ImageInfo, st *State, opts Options) error {
	if opts.Version != "" && m.Version != opts.Version {
		return fmt.Errorf("the source offers %s, not %s", m.Version, opts.Version)
	}
	if m.Version == booted.Version {
		if opts.Force {
			return nil
		}
		return fmt.Errorf("%w (%s)", ErrUpToDate, m.Version)
	}
	if !opts.Force && !opts.AllowDowngrade && m.RollbackIndex <= booted.RollbackIndex {
		return fmt.Errorf("%w: %s (rollback index %d) vs running %s (%d)",
			ErrNotNewer, m.Version, m.RollbackIndex, booted.Version, booted.RollbackIndex)
	}
	// A rollback is a choice: the newest version on the channel, which the
	// timer and "check" pick, must not undo it. A newer build still passes.
	if !opts.Force && opts.Version == "" && st.Held != nil && m.RollbackIndex <= st.Held.RollbackIndex {
		return fmt.Errorf("%w: %s (you went back from %s; pick the version, or use --force, to install it again)",
			ErrHeld, m.Version, st.Held.Version)
	}
	if !opts.Force && st.HasFailed(m.Version) {
		return fmt.Errorf("%w: %s", ErrFailedBefore, m.Version)
	}
	return nil
}

// CheckResult is what a source offers and whether this machine takes it.
type CheckResult struct {
	Manifest  *manifest.Manifest
	Available *Available // non-nil when it is an update this machine would take
	Reason    error      // why not, when Available is nil
}

// Check fetches and verifies the source's manifest and records the result
// in update-state (available, checked). The error is for sources that
// cannot be reached or verified; a manifest that is not an update is a
// Reason.
func Check(ctx context.Context, cfg *config.Config, opts Options) (*CheckResult, error) {
	if config.IsLive() {
		return nil, ErrLive
	}
	var m *manifest.Manifest
	src, err := sourceFor(cfg, opts)
	if err == nil {
		m, err = src.Manifest(ctx)
	}
	if err != nil {
		msg := "check: " + err.Error()
		modifyState(func(st *State) error { st.LastError = msg; return nil })
		return nil, err
	}
	st, _ := LoadState()
	now := time.Now().UTC().Format(time.RFC3339)
	res := &CheckResult{Manifest: m, Reason: accept(m, bootedImage(), st, opts)}
	if res.Reason == nil {
		res.Available = &Available{Version: m.Version, Size: m.Artifact(manifest.Root).Size, Checked: now}
	}
	// Best effort: `vos update --check` also runs unprivileged.
	modifyState(func(st *State) error {
		st.Available, st.Checked = res.Available, now
		if strings.HasPrefix(st.LastError, "check: ") {
			st.LastError = ""
		}
		return nil
	})
	return res, nil
}

// Result describes a Stage that got as far as a manifest.
type Result struct {
	Manifest *manifest.Manifest
	Slot     string // the idle slot
}

// Stage fetches, verifies and writes an image into the idle slot and makes
// it the next boot, in the order docs/CONTRACTS.md "Write order" requires:
// the idle slot's entries are removed first; root is streamed into the
// slot while hashing, then read back; kernel and initrd go to the ESP; the
// entry, with 3 boot tries, comes last; then update-state records it.
func Stage(ctx context.Context, cfg *config.Config, opts Options) (res *Result, err error) {
	if config.IsLive() {
		return nil, ErrLive
	}
	booted := config.BootedSlot()
	if booted != "a" && booted != "b" {
		return nil, errors.New("cannot tell which slot is running (no vos.slot= on the kernel command line)")
	}
	idle := config.OtherSlot(booted)

	lock, err := takeUpdateLock(ctx, lockPatience)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()

	progress := overall(opts.Progress)
	rep := &reporter{fn: progress}
	rep.report("check", 0, 0)
	defer func() {
		if err != nil && !IsBenign(err) && !isNotNow(err) {
			msg := err.Error()
			modifyState(func(st *State) error { st.LastError = msg; return nil })
		}
	}()

	src, err := sourceFor(cfg, opts)
	if err != nil {
		return nil, err
	}
	m, err := src.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	rep.version = m.Version
	res = &Result{Manifest: m, Slot: idle}
	st, _ := LoadState()
	if err := accept(m, bootedImage(), st, opts); err != nil {
		return res, err
	}
	esp := config.ESP
	if err := boot.EnsureESP(esp); err != nil {
		return res, err
	}
	if !opts.Force {
		if err := checkIdleSlotFree(esp, booted, idle); err != nil {
			return res, err
		}
	}
	if !opts.Force && isStaged(esp, st, m.Version, idle) {
		return res, fmt.Errorf("%w: %s in slot %s", ErrAlreadyStaged, m.Version, idle)
	}
	dev, err := SlotDevice(idle)
	if err != nil {
		return res, err
	}
	// Open the slot now, exclusively: a slot that is in use (mounted) or
	// too small is refused before anything is touched.
	slot, capacity, err := openSlot(dev)
	if err != nil {
		return res, err
	}
	defer slot.Close()
	if size := m.Artifact(manifest.Root).Size; size > capacity {
		return res, tooBig(size, dev, capacity)
	}
	if opts.Accepted != nil {
		opts.Accepted(m, idle)
	}

	// Kernel and initrd first: small, and a network problem shows up
	// before the idle slot is touched.
	if err := os.MkdirAll(WorkDir, 0o755); err != nil {
		return res, err
	}
	work, err := os.MkdirTemp(WorkDir, "vos-update-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)
	if err := FetchBootFiles(ctx, src, m, work, progress); err != nil {
		return res, err
	}

	// 1. Unhook the idle slot, so a half-written slot can never boot.
	if err := boot.RemoveSlotEntries(esp, idle); err != nil {
		return res, err
	}
	if _, err := modifyState(func(st *State) error {
		if st.Staged != nil && st.Staged.Slot == idle {
			st.Staged = nil
		}
		return nil
	}); err != nil {
		return res, err
	}
	if err := checkESPSpace(esp, m); err != nil {
		return res, err
	}

	// 2 and 3. Stream root into the slot while hashing; read it back.
	if err := writeRoot(ctx, src, m, slot, dev, progress); err != nil {
		return res, err
	}

	// Past this point nothing looks at ctx: a stage that shuts down now
	// stops before the ESP rather than between the entry and its record.
	if err := ctx.Err(); err != nil {
		return res, err
	}

	// 4 and 5. Kernel and initrd to /efi/vos/<ver>/, the entry last.
	rep.report("install", 0, 1)
	options := boot.Cmdline(idle, ImageCmdline(m), boot.MachineCmdline())
	if err := boot.InstallEntry(esp, m.Version, idle, work, options, boot.DefaultTries); err != nil {
		return res, err
	}
	if err := preferSlot(esp, booted, m.Version); err != nil {
		return res, err
	}
	clearLoaderOverrides()
	syscall.Sync()

	// 6. Record it.
	now := time.Now().UTC().Format(time.RFC3339)
	bi := bootedImage()
	if _, err := modifyState(func(st *State) error {
		st.Staged = &Staged{Version: m.Version, Slot: idle, At: now}
		st.LastError = ""
		switch {
		case m.RollbackIndex < bi.RollbackIndex:
			st.hold(bi) // an explicit downgrade is a rollback too
		case st.Held != nil && m.RollbackIndex >= st.Held.RollbackIndex:
			st.Held = nil // the held version picked explicitly, or a newer one
		}
		return nil
	}); err != nil {
		return res, err
	}
	rep.report("done", 1, 1)
	return res, nil
}

// checkIdleSlotFree refuses to overwrite the idle slot while it is the
// running system's only way back or the user's pending choice: while the
// running entry is still on trial (not blessed yet), the idle slot is its
// known-good fallback; while the running entry is marked bad and the idle
// one is bootable, a rollback or downgrade waits for a restart. A running
// entry with no tries left and nothing better (the last resort) may be
// replaced: staging a fix is how such a machine recovers.
func checkIdleSlotFree(esp, booted, idle string) error {
	cur, err := boot.EntryForSlot(esp, booted)
	if err != nil || cur == nil || cur.Version != bootedImage().Version {
		return err
	}
	if cur.Counting && cur.Bootable() {
		return fmt.Errorf("%w (%s, %d tries left)", ErrOnTrial, cur.Version, cur.Left)
	}
	if !cur.Bootable() {
		if other, err := boot.EntryForSlot(esp, idle); err == nil && other != nil && other.Bootable() {
			return fmt.Errorf("%w: slot %s (%s) starts next", ErrRollbackPending, idle, other.Version)
		}
	}
	return nil
}

// clearLoaderOverrides drops boot menu settings kept in NVRAM, which would
// otherwise override the entry order that stages and rollbacks set up.
func clearLoaderOverrides() {
	if err := boot.ClearLoaderOverrides(); err != nil {
		log.Printf("update: clearing systemd-boot's saved settings: %v", err)
	}
}

// isStaged reports whether version already sits in slot, bootable.
func isStaged(esp string, st *State, version, slot string) bool {
	if st.Staged == nil || st.Staged.Version != version || st.Staged.Slot != slot {
		return false
	}
	e, err := boot.EntryForSlot(esp, slot)
	return err == nil && e != nil && e.Version == version && e.Bootable()
}

func tooBig(size int64, dev string, capacity int64) error {
	return fmt.Errorf("%w: the image is %s (%d bytes), %s holds %s (%d bytes)",
		ErrTooBig, humanBytes(size), size, dev, humanBytes(capacity), capacity)
}

// checkESPSpace fails early, before the long slot write, if the kernel and
// initrd cannot fit on the ESP.
func checkESPSpace(esp string, m *manifest.Manifest) error {
	free, err := diskFree(esp)
	if err != nil || free < 0 {
		return nil // unknown: the copy itself will tell
	}
	need := m.Artifact(manifest.Kernel).Size + m.Artifact(manifest.Initrd).Size + 1<<20
	if free < need {
		return fmt.Errorf("not enough space on the ESP: %d MiB free, %d MiB needed", free>>20, need>>20)
	}
	return nil
}

// preferSlot makes sure the entry just written boots next while the running
// entry stays the fallback. systemd-boot boots the newest entry with tries
// left and, once none has any, the newest of the rest.
//
// A newer image wins on its own. The running entry must then keep tries
// (keepFallback): left at +0, it would sort after the new one among the bad
// entries, and a new image that fails would boot forever.
//
// A downgrade, or a forced reinstall of the same version, does not win on
// its own, so the running entry is marked bad (+0-1), as `vos rollback`
// does. Should the new slot fail, the running entry is still the first of
// the two bad ones (newer, or fewer tries done), so the fallback holds.
func preferSlot(esp, booted, version string) error {
	cur, err := boot.EntryForSlot(esp, booted)
	if err != nil || cur == nil {
		return err
	}
	if boot.CompareVersions(version, cur.Version) > 0 {
		return keepFallback(esp, cur)
	}
	if !cur.Bootable() {
		return nil
	}
	return boot.MarkBad(esp, booted)
}

// keepFallback re-blesses the running entry if something (an earlier
// rollback) left it without tries. It is running, so it boots, and an
// uncounted entry is a fallback systemd-boot never gives up on.
func keepFallback(esp string, running *boot.Entry) error {
	if running.Bootable() {
		return nil
	}
	return boot.Bless(esp, running.Slot)
}

// ImageCmdline is the image part of a new entry's options: the manifest's
// cmdline, or, for a manifest without one, the running image's.
func ImageCmdline(m *manifest.Manifest) string {
	if c := strings.TrimSpace(m.Cmdline); c != "" {
		return c
	}
	return config.ReadLine(config.ImageCmdlinePath)
}

// FetchBootFiles downloads m's kernel and initrd into dir under the names
// boot.InstallEntry expects (vmlinuz, initramfs.img), verified.
func FetchBootFiles(ctx context.Context, src *Source, m *manifest.Manifest, dir string, progress func(Progress)) error {
	rep := &reporter{fn: progress, version: m.Version}
	files := []struct {
		a    manifest.Artifact
		name string
	}{
		{m.Artifact(manifest.Kernel), boot.BootFiles[0]},
		{m.Artifact(manifest.Initrd), boot.BootFiles[1]},
	}
	total := files[0].a.Size + files[1].a.Size
	var base int64
	rep.report("download", 0, total)
	for _, f := range files {
		out, err := os.Create(filepath.Join(dir, f.name))
		if err != nil {
			return err
		}
		err = src.Fetch(ctx, f.a, out, func(done int64) error {
			rep.report("download", base+done, total)
			return ctx.Err()
		})
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		base += f.a.Size
	}
	return nil
}

// WriteRoot streams m's root artifact into dev (a slot partition) in
// ChunkSize writes while hashing it, fsyncs, drops the page cache, and then
// reads the slot back from the disk and verifies it again.
func WriteRoot(ctx context.Context, src *Source, m *manifest.Manifest, dev string, progress func(Progress)) error {
	f, capacity, err := openSlot(dev)
	if err != nil {
		return err
	}
	defer f.Close()
	if size := m.Artifact(manifest.Root).Size; size > capacity {
		return tooBig(size, dev, capacity)
	}
	return writeRoot(ctx, src, m, f, dev, progress)
}

// writeRoot is WriteRoot on a slot openSlot already opened; it closes f.
func writeRoot(ctx context.Context, src *Source, m *manifest.Manifest, f *os.File, dev string, progress func(Progress)) error {
	defer f.Close()
	root := m.Artifact(manifest.Root)
	rep := &reporter{fn: progress, version: m.Version}
	rep.report("write", 0, root.Size)
	var synced int64
	err := src.Fetch(ctx, root, f, func(done int64) error {
		if done-synced >= syncEvery {
			if err := f.Sync(); err != nil {
				return err
			}
			synced = done
		}
		rep.report("write", done, root.Size)
		return ctx.Err()
	})
	if err != nil {
		return fmt.Errorf("writing %s: %w", dev, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("writing %s: %w", dev, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", dev, err)
	}
	return verifySlot(ctx, dev, root, rep)
}

// openSlot opens a slot partition for writing and returns its size. A
// block device is opened exclusively, which fails while it is mounted.
func openSlot(dev string) (*os.File, int64, error) {
	fi, err := os.Stat(dev)
	if err != nil {
		return nil, 0, fmt.Errorf("slot partition: %w", err)
	}
	flags := os.O_WRONLY
	if fi.Mode()&os.ModeDevice != 0 {
		flags |= exclusiveFlag
	}
	f, err := os.OpenFile(dev, flags, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("opening %s: %w", dev, err)
	}
	capacity, err := f.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("slot partition %s: %w", dev, err)
	}
	return f, capacity, nil
}

// verifySlot reads a's bytes back from dev and checks their sha256.
func verifySlot(ctx context.Context, dev string, a manifest.Artifact, rep *reporter) error {
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	defer f.Close()
	dropCache(f)
	rep.report("verify", 0, a.Size)
	err = copyVerified(f, nil, a.Size, a.SHA256, func(done int64) error {
		rep.report("verify", done, a.Size)
		return ctx.Err()
	})
	if err != nil {
		return fmt.Errorf("verifying %s after writing: %w", dev, err)
	}
	return nil
}

// Rollback makes the other slot boot next and returns its version. If an
// earlier rollback marked the other slot bad, it gets fresh tries rather
// than a clean entry, so it still falls back here if it does not come up.
//
// When the other slot is older (a real rollback), the running entry is
// marked bad (+0-1), as in the bash vos: it drops to the end of the menu
// but stays there, first among the bad entries, so it is still the
// fallback. The version is held: automatic updates no longer bring it or
// anything older back (see accept). When the other slot is newer (rolling
// forward again, or a staged update), it sorts first on its own, and the
// running entry must keep its tries to stay reachable (keepFallback).
//
// A slot whose version failed before needs force.
func Rollback(force bool) (string, error) {
	if config.IsLive() {
		return "", ErrLive
	}
	cur := config.BootedSlot()
	if cur != "a" && cur != "b" {
		return "", errors.New("not running from an installed slot")
	}
	target := config.OtherSlot(cur)
	esp := config.ESP
	if err := boot.EnsureESP(esp); err != nil {
		return "", err
	}
	lock, err := takeUpdateLock(context.Background(), lockPatience)
	if err != nil {
		return "", err
	}
	defer lock.Unlock()

	e, err := boot.EntryForSlot(esp, target)
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", fmt.Errorf("slot %s has nothing bootable", target)
	}
	if !force {
		st, _ := LoadState()
		if st.HasFailed(e.Version) {
			return "", fmt.Errorf("%w: %s in slot %s", ErrFailedBefore, e.Version, target)
		}
		if !e.Bootable() && e.Done > 1 {
			return "", fmt.Errorf("slot %s (%s) used up its boot attempts without starting", target, e.Version)
		}
	}
	running, err := boot.EntryForSlot(esp, cur)
	if err != nil {
		return "", err
	}
	// The target first: with the same version in both slots, its
	// vos-<ver>+0-1.conf is the very name MarkBad gives the running entry.
	if !e.Bootable() {
		if err := boot.SetTries(esp, target, boot.DefaultTries); err != nil {
			return "", err
		}
	}
	forward := running != nil && boot.CompareVersions(e.Version, running.Version) > 0
	if forward {
		err = keepFallback(esp, running)
	} else {
		err = boot.MarkBad(esp, cur)
	}
	if err != nil {
		return "", err
	}
	clearLoaderOverrides()
	syscall.Sync()
	bi := bootedImage()
	if _, err := modifyState(func(st *State) error {
		switch {
		case boot.CompareVersions(e.Version, bi.Version) < 0:
			st.hold(bi)
		case st.Held != nil && boot.CompareVersions(e.Version, st.Held.Version) >= 0:
			st.Held = nil // back to the held version (or past it)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return e.Version, nil
}

// stageSpans maps each phase's own 0-100% onto Stage's overall percent.
var stageSpans = map[string][2]int{
	"check": {0, 0}, "download": {0, 2}, "write": {2, 80},
	"verify": {80, 98}, "install": {98, 100}, "done": {100, 100},
}

func overall(fn func(Progress)) func(Progress) {
	if fn == nil {
		return nil
	}
	return func(p Progress) {
		if s, ok := stageSpans[p.Phase]; ok {
			p.Percent = s[0] + (s[1]-s[0])*p.Percent/100
		}
		fn(p)
	}
}

// reporter turns byte counts into Progress, at most every 250 ms within a
// phase (and always for a new phase or its end).
type reporter struct {
	fn      func(Progress)
	version string
	phase   string
	pct     int
	at      time.Time
}

func (r *reporter) report(phase string, done, total int64) {
	if r == nil || r.fn == nil {
		return
	}
	pct := 100
	if total > 0 {
		pct = int(done * 100 / total)
	}
	now := time.Now()
	if phase == r.phase && done != total && (pct == r.pct || now.Sub(r.at) < 250*time.Millisecond) {
		return
	}
	r.phase, r.pct, r.at = phase, pct, now
	r.fn(Progress{Phase: phase, Percent: pct, Bytes: done, Total: total, Version: r.version})
}
