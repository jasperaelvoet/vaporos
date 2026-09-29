// Package install writes VaporOS to a disk: the CLI `vos install` and the
// web installer (installer mode, live ISO) share Install. Port of the bash
// cmd_install with the A/B layout from docs/CONTRACTS.md.
//
// An install runs in fixed steps, each reported through Progress:
//
//	probe       check the system, the target disk and the signed image;
//	            fetch the kernel and initramfs (verified) to a temp dir
//	partition   erase: a new GPT with vos_esp, vos_a, vos_b, vos_data and
//	            fresh filesystems; repair: fsck vos_data, reformat the ESP
//	write       stream root.erofs into slot a while hashing it
//	verify      read slot a back from the disk and check it again
//	bootloader  systemd-boot, loader.conf and the slot a entry
//	configure   first-boot state on vos_data: hostname, timezone, admin
//	            password, machine kernel args, config.json
//	done
//
// The image comes through the update package's Source, the same verified
// fetch layer `vos update` uses, from the live medium, a directory, an
// http(s) server or an OCI registry (see source.go).
//
// Nothing is written to a disk before probe has accepted it, verified the
// image's signature and fetched the kernel and initramfs, so neither a bad
// image nor a network problem can leave a half-erased disk. On failure
// every mount is undone and a boot entry that was already written is
// removed again, so a half-installed disk is never booted.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Install modes.
const (
	ModeErase  = "erase"  // repartition the whole disk
	ModeRepair = "repair" // keep vos_data; rewrite slot a and the ESP
)

// Steps reported to Progress, in order.
const (
	StepProbe      = "probe"
	StepPartition  = "partition"
	StepWrite      = "write"
	StepVerify     = "verify"
	StepBootloader = "bootloader"
	StepConfigure  = "configure"
	StepDone       = "done"
)

type Options struct {
	Disk      string   `json:"disk"`
	Mode      string   `json:"mode"` // "erase" | "repair"
	Hostname  string   `json:"hostname"`
	Password  string   `json:"password"` // web admin password
	Timezone  string   `json:"timezone"`
	Libraries []string `json:"libraries"` // filesystem UUIDs to adopt
	Source    string   `json:"source"`    // "" = live medium; else a directory, http(s) URL or oci://registry/repo
	Channel   string   `json:"channel"`   // OCI tag when Source has none; "" = the live image's channel, else main
}

// Progress reports one step of an install. percent is the whole install's
// progress, 0-100, and never goes backwards.
type Progress func(step string, percent int, message string)

// allowAnywhere lets tests run an install outside the live ISO and as a
// normal user. Nothing else sets it.
var allowAnywhere = false

// Install performs a full install. It refuses to run outside live mode
// unless allowAnywhere is set (tests).
func Install(ctx context.Context, opts Options, progress Progress) error {
	return runInstall(ctx, defaultEnv(), opts, progress)
}

func runInstall(ctx context.Context, e *env, opts Options, progress Progress) error {
	in, err := newInstaller(e, opts, progress)
	if err != nil {
		return err
	}
	defer in.close()
	if err := in.prepare(ctx); err != nil {
		return err
	}
	return in.execute(ctx)
}

// installer carries one install from probe to done.
type installer struct {
	env      *env
	opts     Options
	progress Progress

	disk     string // kernel name of the target disk ("sda", "nvme0n1")
	src      *imageSource
	man      *manifest.Manifest
	unsigned bool    // accepted under unsignedRule (a debug live medium)
	slotMiB  int64   // erase: size of each slot
	parts    partSet // kernel names of the four partitions

	connector      string // virtual connector for this hardware ("" = none)
	machineCmdline string // kernel args for connector, and vos.disk once known
	keepCmdline    bool   // repair: vos_data already has exactly these machine args
	diskGUID       string // the target's GPT disk GUID ("" = unknown)
	hadConfig      bool   // repair: vos_data has a readable config.json
	libraries      []config.Library

	step         string
	percent      int
	mounts       []string // mount points, in mount order
	espDir       string
	rootDir      string
	bootDir      string // where InstallEntry copies vmlinuz and initramfs.img from
	workDir      string
	entryWritten bool
	unlock       func()
}

func newInstaller(e *env, opts Options, progress Progress) (*installer, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	return &installer{env: e, opts: opts, progress: progress}, nil
}

// report sends progress; percent is clamped so it never goes backwards.
func (in *installer) report(step string, percent int, format string, args ...any) {
	percent = min(max(percent, in.percent), 100)
	in.step, in.percent = step, percent
	if in.progress != nil {
		in.progress(step, percent, fmt.Sprintf(format, args...))
	}
}

// reportBytes maps done/total onto the percent range [from, to] and
// reports only when the whole-number percent moves.
func (in *installer) reportBytes(step string, from, to int, done, total int64, what string) {
	pct := from
	if total > 0 {
		pct = from + int(int64(to-from)*done/total)
	}
	if pct == in.percent && done != total {
		return
	}
	in.report(step, pct, "%s (%s of %s)", what, humanBytes(done), humanBytes(total))
}

func targetBase() string { return filepath.Join(config.RunDir, "target") }

// preflight checks the machine can run an install at all.
func preflight() error {
	if !allowAnywhere {
		if !config.IsLive() {
			return errors.New("the installer runs from the VaporOS live ISO")
		}
		if os.Geteuid() != 0 {
			return errors.New("the installer must run as root")
		}
	}
	if !exists(paths.EFI) {
		return errors.New("boot the installer in UEFI mode; VaporOS does not support legacy BIOS")
	}
	return nil
}

// lockInstall makes installs exclusive across processes (the web installer
// and a CLI run on the serial console).
func lockInstall() (func(), error) {
	if err := os.MkdirAll(config.RunDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(config.RunDir, "install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another installation is already running")
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// prepare is the probe step: it checks everything it can without writing,
// so a refused install leaves the disk untouched.
func (in *installer) prepare(ctx context.Context) error {
	in.report(StepProbe, 0, "Checking the system")
	if err := preflight(); err != nil {
		return err
	}
	unlock, err := lockInstall()
	if err != nil {
		return err
	}
	in.unlock = unlock
	in.cleanStaleMounts(ctx)

	disk, err := resolveDisk(in.opts.Disk)
	if err != nil {
		return err
	}
	if err := checkTarget(disk); err != nil {
		return err
	}
	in.disk = disk

	spec, err := parseSource(in.opts.Source)
	if err != nil {
		return err
	}
	src, err := openSource(spec, in.opts.Channel)
	if err != nil {
		return err
	}
	in.src = src
	in.report(StepProbe, 1, "Reading the VaporOS image from %s", src)
	img, err := loadManifest(ctx, in.env, src)
	if err != nil {
		return err
	}
	m := img.man
	in.man, in.unsigned = m, img.unsigned
	if err := checkLocalRoot(src, m); err != nil {
		return err
	}

	if err := in.planDisk(); err != nil {
		return err
	}
	// The installed image brings its own tz database; checking against
	// the running one catches typos before anything is erased.
	if in.opts.Timezone != "" && exists(paths.Zoneinfo) {
		if err := checkTimezone(paths.Zoneinfo, in.opts.Timezone); err != nil {
			return err
		}
	}
	libs, err := resolveLibraries(ctx, in.env, in.opts.Libraries, disk)
	if err != nil {
		return err
	}
	in.libraries = libs
	in.chooseDisplay()
	in.report(StepProbe, 2, "Ready to install VaporOS %s on %s", m.Version, devName(disk))
	return nil
}

// planDisk sizes the slots (erase) or finds the existing ones (repair).
func (in *installer) planDisk() error {
	root := in.man.Artifact(manifest.Root)
	if in.opts.Mode == ModeRepair {
		parts, err := vosLayout(in.disk)
		if err != nil {
			return err
		}
		if have := sizeBytes(parts.a); have < root.Size {
			return fmt.Errorf("slot a (%s) is too small for this image (%s); erase and reinstall instead", humanBytes(have), humanBytes(root.Size))
		}
		in.parts = parts
		return nil
	}
	slotMiB, need, err := eraseLayout(root.Size)
	if err != nil {
		return err
	}
	in.slotMiB = slotMiB
	if have := sizeBytes(in.disk); have < need {
		return fmt.Errorf("%s is too small (%s): VaporOS needs at least %s", devName(in.disk), humanBytes(have), humanBytes(need))
	}
	return nil
}

// chooseDisplay picks the virtual connector on a supported GPU. It is
// best-effort: without one the system still installs, and vosd can set
// it later.
func (in *installer) chooseDisplay() {
	gpu := in.env.gpu()
	if !gpu.Supported {
		in.env.logf("install: no supported GPU (%s %s); no virtual display", gpu.Vendor, gpu.Name)
		return
	}
	conn, err := in.env.chooseConnector()
	if err != nil || conn == "" {
		in.env.logf("install: no connector for the virtual display: %v", err)
		return
	}
	in.connector, in.machineCmdline = conn, in.env.machineCmdlineFor(conn)
}

// execute runs every writing step; prepare must have succeeded.
func (in *installer) execute(ctx context.Context) error {
	if in.man == nil {
		return errors.New("install: nothing prepared")
	}
	// The CLI waits for a confirmation between prepare and execute.
	if err := checkTarget(in.disk); err != nil {
		return err
	}
	steps := []func(context.Context) error{
		in.fetchBoot, in.partition, in.writeRoot, in.installBootloader, in.inspectTarget, in.configure,
		in.configurePower, in.finish,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			in.abort()
			return fmt.Errorf("%s: %w", in.step, err)
		}
	}
	in.report(StepDone, 100, "VaporOS %s is installed on %s", in.man.Version, devName(in.disk))
	return nil
}

func (in *installer) run(ctx context.Context, name string, args ...string) error {
	_, err := in.env.run.Run(ctx, name, args...)
	return err
}

func (in *installer) partition(ctx context.Context) error {
	if in.opts.Mode == ModeRepair {
		return in.prepareRepair(ctx)
	}
	disk := devPath(in.disk)
	in.report(StepPartition, 3, "Erasing %s", devName(in.disk))
	// Old partitions first: a signature left inside one can resurface in
	// a new partition that starts at the same offset.
	for _, p := range partitions(in.disk) {
		if err := in.run(ctx, "wipefs", "-qa", devPath(p.Name)); err != nil {
			in.env.logf("install: %v", err)
		}
	}
	if err := in.run(ctx, "wipefs", "-qa", disk); err != nil {
		return err
	}
	// No -q: on a blank disk sgdisk 1.0.10 -q builds the table in memory,
	// exits 0 and never writes it.
	if err := in.run(ctx, "sgdisk", "--zap-all", disk); err != nil {
		return err
	}
	in.report(StepPartition, 5, "Creating partitions")
	if err := in.run(ctx, "sgdisk", sgdiskArgs(disk, in.slotMiB)...); err != nil {
		return err
	}
	if err := in.run(ctx, "blockdev", "--rereadpt", disk); err != nil {
		in.run(ctx, "partx", "-u", disk)
	}
	parts, err := in.waitPartitions(ctx)
	if err != nil {
		return err
	}
	in.parts = parts
	in.recordDisk()

	in.report(StepPartition, 8, "Creating filesystems")
	for _, p := range parts.list() {
		if err := in.run(ctx, "wipefs", "-qa", devPath(p)); err != nil {
			return err
		}
	}
	if err := in.run(ctx, "mkfs.vfat", "-F", "32", "-n", "VOS_ESP", devPath(parts.esp)); err != nil {
		return err
	}
	if err := in.run(ctx, "mkfs.ext4", "-q", "-F", "-L", "vos_data", devPath(parts.data)); err != nil {
		return err
	}
	in.report(StepPartition, 10, "Disk prepared")
	return nil
}

// waitPartitions waits for the new partitions. udev re-reads the table
// itself when sgdisk closes the disk, which briefly removes and re-adds
// the partitions, so poll until all four are there with the expected
// names and sizes; the sizes also prove the kernel dropped the old table.
func (in *installer) waitPartitions(ctx context.Context) (partSet, error) {
	want := []int64{espMiB * mib, in.slotMiB * mib, in.slotMiB * mib, 0}
	for tries := 0; ; tries++ {
		in.run(ctx, "udevadm", "settle")
		var names [4]string
		ok := true
		for i := range names {
			p := partByNumber(in.disk, i+1)
			names[i] = p.Name
			switch {
			case p.Name == "" || !isBlockDevice(devPath(p.Name)):
				ok = false
			case p.Label != "" && p.Label != partNames[i]:
				ok = false
			case want[i] != 0 && sizeBytes(p.Name) != want[i]:
				ok = false
			}
		}
		if ok {
			return partSet{esp: names[0], a: names[1], b: names[2], data: names[3]}, nil
		}
		if tries >= 50 {
			return partSet{}, fmt.Errorf("the new partitions did not appear on %s", devName(in.disk))
		}
		if err := in.env.sleep(ctx, 200*time.Millisecond); err != nil {
			return partSet{}, err
		}
	}
}

// prepareRepair keeps vos_data (after a filesystem check) and rebuilds
// everything else around it. Reformatting the ESP is safe: it holds only
// the bootloader and kernels, which are rewritten, and fstab and the
// firmware find it by partition label and GUID, which do not change.
func (in *installer) prepareRepair(ctx context.Context) error {
	in.report(StepPartition, 3, "Checking the data partition")
	data := devPath(in.parts.data)
	// e2fsck: 0 clean, 1 fixed, 2 fixed (reboot advised), 4+ not fixed.
	if err := in.run(ctx, "e2fsck", "-f", "-p", data); err != nil {
		if code := exitCode(err); code < 0 || code >= 4 {
			in.env.logf("install: e2fsck -p: %v; retrying with -y", err)
			if err := in.run(ctx, "e2fsck", "-f", "-y", data); err != nil {
				if code := exitCode(err); code < 0 || code >= 4 {
					return fmt.Errorf("the data partition has errors that could not be fixed: %w", err)
				}
			}
		}
	}
	in.report(StepPartition, 6, "Formatting the boot partition")
	for _, p := range []string{in.parts.esp, in.parts.b} {
		if err := in.run(ctx, "wipefs", "-qa", devPath(p)); err != nil {
			return err
		}
	}
	if err := in.run(ctx, "mkfs.vfat", "-F", "32", "-n", "VOS_ESP", devPath(in.parts.esp)); err != nil {
		return err
	}
	in.recordDisk()
	in.report(StepPartition, 10, "Disk prepared")
	return nil
}

// recordDisk reads the target's GPT disk GUID for vos.disk=: the initramfs
// and updates then find VaporOS's partitions on this disk only, even with
// another disk with VaporOS partitions attached. Without it they go by
// partition label, as installs before vos.disk do.
func (in *installer) recordDisk() {
	guid, err := in.env.diskGUID(devPath(in.disk))
	if err != nil {
		in.env.logf("install: reading the GPT disk GUID of %s: %v; partitions will be found by label", devName(in.disk), err)
		return
	}
	in.diskGUID = guid
}

// installBootloader installs systemd-boot and the entry for slot a. The
// target root is mounted here too: the entry needs the machine kernel
// args, which a repair keeps from vos_data.
func (in *installer) installBootloader(ctx context.Context) error {
	in.report(StepBootloader, 90, "Installing the bootloader")
	esp := filepath.Join(targetBase(), "esp")
	if err := in.mount(ctx, true, "-t", "vfat", "-o", "fmask=0077,dmask=0077", devPath(in.parts.esp), esp); err != nil {
		return err
	}
	in.espDir = esp
	if err := in.run(ctx, "bootctl", "install", "--esp-path="+esp, "--graceful", "--no-pager"); err != nil {
		return err
	}
	// NVRAM outlives the disk: a menu timeout or pinned entry from an
	// earlier systemd-boot on this PC would beat loader.conf.
	if err := in.env.clearLoaderVars(); err != nil {
		in.env.logf("install: clearing systemd-boot's saved settings: %v", err)
	}
	if err := in.env.writeLoaderConf(esp); err != nil {
		return fmt.Errorf("loader.conf: %w", err)
	}
	if err := in.mountTarget(ctx); err != nil {
		return err
	}
	in.report(StepBootloader, 92, "Writing the boot entry")
	imageCmdline := in.man.Cmdline
	if imageCmdline == "" {
		// Older manifests had no cmdline; the image carries its own.
		imageCmdline = config.ReadLine(filepath.Join(in.rootDir, config.ImageCmdlinePath))
	}
	options := in.env.bootCmdline("a", imageCmdline, in.entryMachineCmdline())
	if !hasWord(options, "vos.slot=a") {
		return fmt.Errorf("boot entry options %q do not select slot a", options)
	}
	// Entries are written last by InstallEntry, and removed again by abort
	// if a later step fails.
	in.entryWritten = true
	if err := in.env.installEntry(esp, in.man.Version, "a", in.bootDir, options, 0); err != nil {
		return fmt.Errorf("boot entry: %w", err)
	}
	for i, key := range []string{manifest.Kernel, manifest.Initrd} {
		if err := verifyFile(filepath.Join(esp, "vos", in.man.Version, boot.BootFiles[i]), in.man.Artifact(key)); err != nil {
			return fmt.Errorf("the ESP copy does not verify: %w", err)
		}
	}
	in.report(StepBootloader, 95, "Bootloader installed")
	return nil
}

func hasWord(s, w string) bool {
	for _, f := range strings.Fields(s) {
		if f == w {
			return true
		}
	}
	return false
}

// entryMachineCmdline is the machine part of the entry's options: what
// vos_data already has on a repair (the user may have changed the
// connector since), else what this hardware needs; with vos.disk= naming
// the target. configure writes the same to /var/lib/vos/cmdline.
func (in *installer) entryMachineCmdline() string {
	machine, existing, found := in.machineCmdline, "", false
	if in.opts.Mode == ModeRepair {
		if b, err := os.ReadFile(filepath.Join(in.rootDir, config.MachineCmdlinePath())); err == nil {
			existing, found = strings.TrimSpace(string(b)), true
			machine = existing
		}
	}
	if in.diskGUID != "" {
		machine = boot.WithDiskArg(machine, in.diskGUID)
	}
	in.machineCmdline = machine
	in.keepCmdline = found && machine == existing
	return machine
}

// inspectTarget reads what a repair keeps from vos_data, now that it is
// mounted. An empty hostname or timezone means "keep the installed
// system's", found in the /etc overlay's upper layer (what the user set,
// not the image's defaults).
func (in *installer) inspectTarget(ctx context.Context) error {
	if in.opts.Mode != ModeRepair {
		return nil
	}
	upper := filepath.Join(in.rootDir, "state", "etc", "upper")
	if in.opts.Hostname == "" {
		if h := config.ReadLine(filepath.Join(upper, "hostname")); checkHostname(h) == nil {
			in.opts.Hostname = h
		}
	}
	if in.opts.Timezone == "" {
		if link, err := os.Readlink(filepath.Join(upper, "localtime")); err == nil {
			// Only a zone the new image still has; otherwise the link stays.
			tz := zoneFromLink(link)
			if tz != "" && checkTimezone(filepath.Join(in.rootDir, "usr", "share", "zoneinfo"), tz) == nil {
				in.opts.Timezone = tz
			}
		}
	}
	in.hadConfig = config.ReadJSON(filepath.Join(in.rootDir, config.ConfigPath()), config.Defaults()) == nil
	return nil
}

// configurePower turns idle shutdown on in a new config.json when a wired
// NIC can wake the PC with a magic packet. Without one, a PC switched off
// for being idle stays off until someone presses its power button. A
// repair keeps the user's setting.
func (in *installer) configurePower(ctx context.Context) error {
	if in.hadConfig {
		return nil
	}
	nic := in.wakeOnLANNIC(ctx)
	if nic == "" {
		in.env.logf("install: no wired network interface supports Wake-on-LAN; idle shutdown stays off")
		return nil
	}
	path := filepath.Join(in.rootDir, config.ConfigPath())
	cfg := config.Defaults()
	if err := config.ReadJSON(path, cfg); err != nil {
		return fmt.Errorf("config.json: %w", err)
	}
	cfg.Power.IdleShutdown = true
	in.env.logf("install: %s wakes on a magic packet; idle shutdown is on", nic)
	return config.WriteJSONAtomic(path, cfg, 0o644)
}

// wakeOnLANNIC returns a wired network interface whose driver can wake the
// machine with a magic packet (ethtool's "Supports Wake-on:" lists g).
func (in *installer) wakeOnLANNIC(ctx context.Context) string {
	entries, err := os.ReadDir(paths.ClassNet)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(paths.ClassNet, name)
		// Hardware (a device), Ethernet (ARPHRD_ETHER), not Wi-Fi.
		if strings.HasPrefix(name, "-") || !exists(filepath.Join(dir, "device")) ||
			readSys(filepath.Join(dir, "type")) != "1" ||
			exists(filepath.Join(dir, "wireless")) || exists(filepath.Join(dir, "phy80211")) {
			continue
		}
		out, err := in.env.run.Run(ctx, "ethtool", name)
		if err == nil && supportsMagicPacket(out) {
			return name
		}
	}
	return ""
}

// supportsMagicPacket reads ethtool's "Supports Wake-on: pumbg" line.
func supportsMagicPacket(ethtool string) bool {
	for _, line := range strings.Split(ethtool, "\n") {
		if modes, ok := strings.CutPrefix(strings.TrimSpace(line), "Supports Wake-on:"); ok {
			return strings.Contains(strings.TrimSpace(modes), "g")
		}
	}
	return false
}

// mount runs mount(8) with args (the mount point last) and records the
// mount point for unmountAll. mkdir creates the mount point first; mount
// points inside the read-only image already exist.
func (in *installer) mount(ctx context.Context, mkdir bool, args ...string) error {
	target := args[len(args)-1]
	if mkdir {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
	}
	if err := in.run(ctx, "mount", args...); err != nil {
		return err
	}
	in.mounts = append(in.mounts, target)
	return nil
}

// unmountAll undoes every mount in reverse order, falling back to a lazy
// unmount so a busy mount point cannot wedge the installer.
func (in *installer) unmountAll(ctx context.Context) error {
	var errs []error
	for i := len(in.mounts) - 1; i >= 0; i-- {
		mp := in.mounts[i]
		if err := in.run(ctx, "umount", mp); err != nil {
			if err2 := in.run(ctx, "umount", "-l", mp); err2 != nil {
				errs = append(errs, fmt.Errorf("unmounting %s: %w", mp, err))
			}
		}
	}
	in.mounts = nil
	return errors.Join(errs...)
}

// cleanStaleMounts unmounts what a crashed earlier run left under the
// target directory, deepest first.
func (in *installer) cleanStaleMounts(ctx context.Context) {
	mounts, err := readMountinfo()
	if err != nil {
		return
	}
	base := targetBase()
	for i := len(mounts) - 1; i >= 0; i-- {
		mp := mounts[i].Mountpoint
		if mp != base && !strings.HasPrefix(mp, base+"/") {
			continue
		}
		in.env.logf("install: unmounting stale %s", mp)
		if err := in.run(ctx, "umount", mp); err != nil {
			in.run(ctx, "umount", "-l", mp)
		}
	}
}

// finish flushes and unmounts everything; an install is only done when
// its filesystems are cleanly unmounted.
func (in *installer) finish(ctx context.Context) error {
	in.run(ctx, "sync")
	if err := in.unmountAll(ctx); err != nil {
		return err
	}
	in.run(ctx, "sync")
	return nil
}

// abort cleans up after a failed step: the boot entry goes first, so the
// firmware can never pick a half-installed system, then every mount.
func (in *installer) abort() {
	ctx := context.Background()
	if in.entryWritten && in.espDir != "" {
		entries, _ := filepath.Glob(filepath.Join(in.espDir, "loader", "entries", "vos-*.conf"))
		for _, e := range entries {
			if err := os.Remove(e); err != nil {
				in.env.logf("install: %v", err)
			}
		}
		in.run(ctx, "sync")
	}
	if err := in.unmountAll(ctx); err != nil {
		in.env.logf("install: cleanup: %v", err)
	}
}

// close releases what the installer holds; call it after execute (or
// after a failed prepare).
func (in *installer) close() {
	if len(in.mounts) > 0 {
		in.abort()
	}
	if in.workDir != "" {
		os.RemoveAll(in.workDir)
		in.workDir = ""
	}
	if in.unlock != nil {
		in.unlock()
		in.unlock = nil
	}
}
