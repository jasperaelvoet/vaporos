package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage"
)

// mountTarget assembles the installed system the way the initramfs will
// (rootfs/usr/lib/initcpio/hooks/vos): slot a read-only at /, vos_data at
// /state, /var bound from /state/var and /etc overlaid from
// /state/etc/upper. First-boot state written through it lands exactly
// where the booted system looks for it.
//
// index=off matters: with the index on, overlayfs ties the upper layer to
// the lower it was created on, and the other slot's /etc would refuse to
// mount after the first update (ESTALE).
func (in *installer) mountTarget(ctx context.Context) error {
	root := filepath.Join(targetBase(), "root")
	if err := in.mount(ctx, true, "-t", "erofs", "-o", "ro", devPath(in.parts.a), root); err != nil {
		return err
	}
	state := filepath.Join(root, "state")
	if err := in.mount(ctx, false, "-t", "ext4", "-o", "rw,noatime", devPath(in.parts.data), state); err != nil {
		return err
	}
	upper, work := filepath.Join(state, "etc", "upper"), filepath.Join(state, "etc", "work")
	for _, d := range []string{filepath.Join(state, "var"), upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := in.mount(ctx, false, "--bind", filepath.Join(state, "var"), filepath.Join(root, "var")); err != nil {
		return err
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s,index=off", filepath.Join(root, "etc"), upper, work)
	if err := in.mount(ctx, false, "-t", "overlay", "overlay", "-o", opts, filepath.Join(root, "etc")); err != nil {
		return err
	}
	in.rootDir = root
	return nil
}

// configure writes the first-boot state into the mounted target. There is
// no Unix account to create: the gaming user comes from sysusers, root
// stays locked, and the web admin lives in auth.json.
func (in *installer) configure(ctx context.Context) error {
	root := in.rootDir
	in.report(StepConfigure, 95, "Configuring %s", orElse(in.opts.Hostname, "the system"))

	// tmpfiles creates these on boot as well; making them now keeps the
	// /home and /root symlinks valid from the very first moment.
	if err := os.MkdirAll(filepath.Join(root, "var", "home"), 0o755); err != nil {
		return err
	}
	roothome := filepath.Join(root, "var", "roothome")
	if err := os.MkdirAll(roothome, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(roothome, 0o700); err != nil {
		return err
	}

	if in.opts.Hostname != "" {
		if err := config.WriteFileAtomic(filepath.Join(root, "etc", "hostname"), []byte(in.opts.Hostname+"\n"), 0o644); err != nil {
			return fmt.Errorf("hostname: %w", err)
		}
	}
	if in.opts.Timezone != "" {
		if err := setTimezone(root, in.opts.Timezone); err != nil {
			return err
		}
	}
	if in.opts.Password != "" {
		if err := in.env.setAdminPassword(root, in.opts.Password); err != nil {
			return fmt.Errorf("admin password: %w", err)
		}
	}
	if !in.keepCmdline && in.machineCmdline != "" {
		if err := in.env.setMachineCmdline(root, in.machineCmdline); err != nil {
			return fmt.Errorf("machine kernel args: %w", err)
		}
	}
	if err := in.writeMachineConfig(root); err != nil {
		return fmt.Errorf("config.json: %w", err)
	}
	if in.opts.Mode == ModeRepair {
		if err := clearStaged(root); err != nil {
			return fmt.Errorf("update state: %w", err)
		}
	}
	in.report(StepConfigure, 99, "Finishing")
	return nil
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// setTimezone points /etc/localtime at the installed image's zone file.
func setTimezone(root, tz string) error {
	if err := checkTimezone(filepath.Join(root, "usr", "share", "zoneinfo"), tz); err != nil {
		return err
	}
	link := filepath.Join(root, "etc", "localtime")
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Symlink("../usr/share/zoneinfo/"+tz, link)
}

// writeMachineConfig writes config.json: defaults for a new install with
// the channel the image came from, or the existing file on a repair; plus
// the adopted libraries and the chosen virtual connector.
func (in *installer) writeMachineConfig(root string) error {
	path := filepath.Join(root, config.ConfigPath())
	cfg := config.Defaults()
	existing := false
	if in.opts.Mode == ModeRepair {
		switch err := config.ReadJSON(path, cfg); {
		case err == nil:
			existing = true
		case !errors.Is(err, fs.ErrNotExist):
			// Keep the unreadable file for reference; a repair starts over.
			in.env.logf("install: %v; starting from defaults", err)
			os.Rename(path, path+".broken")
			cfg = config.Defaults()
		}
	}
	if !existing && in.man.Channel != "" {
		cfg.Update.Channel = in.man.Channel
	}
	if cfg.Display.VirtualConnector == "" {
		cfg.Display.VirtualConnector = in.connector
	}
	cfg.Storage.Libraries = mergeLibraries(cfg.Storage.Libraries, in.libraries)
	if cfg.SSH.Keys == nil {
		cfg.SSH.Keys = []string{}
	}
	cfg.Schema = 1
	return config.WriteJSONAtomic(path, cfg, 0o644)
}

// mergeLibraries appends add to have, skipping UUIDs already present and
// keeping mount points unique.
func mergeLibraries(have, add []config.Library) []config.Library {
	out := append([]config.Library{}, have...)
	uuids, mps := map[string]bool{}, map[string]bool{}
	for _, l := range out {
		uuids[l.UUID], mps[l.Mountpoint] = true, true
	}
	for _, l := range add {
		if uuids[l.UUID] {
			continue
		}
		if mps[l.Mountpoint] {
			l.Mountpoint += "-" + shortID(l.UUID)
		}
		uuids[l.UUID], mps[l.Mountpoint] = true, true
		out = append(out, l)
	}
	return out
}

func shortID(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	return s[:min(8, len(s))]
}

// clearStaged drops update-state's "staged" record on a repair: slot b was
// wiped, and vosd would otherwise count the staged version as failed.
// Other fields (failed[], last_error) are kept untouched.
func clearStaged(root string) error {
	path := filepath.Join(root, config.UpdateStatePath())
	var st map[string]json.RawMessage
	if err := config.ReadJSON(path, &st); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, ok := st["staged"]; !ok {
		return nil
	}
	delete(st, "staged")
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return config.WriteJSONAtomic(path, st, perm)
}

// libraryFS are the filesystems a Steam library can be adopted from.
var libraryFS = map[string]bool{
	"ext4": true, "ext3": true, "ext2": true, "btrfs": true, "xfs": true,
	"f2fs": true, "ntfs": true, "ntfs3": true, "exfat": true, "vfat": true,
}

var unsafeLabelRE = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// libraryMountpoint is /var/mnt/<label>, or /var/mnt/<uuid> for an
// unlabelled filesystem (docs/CONTRACTS.md "Paths").
func libraryMountpoint(label, uuid string) string {
	name := strings.TrimLeft(unsafeLabelRE.ReplaceAllString(label, "_"), ".")
	if name == "" {
		name = uuid
	}
	return "/var/mnt/" + name
}

// resolveLibraries turns the chosen filesystem UUIDs into config entries.
// A library on the target disk would be erased (or is VaporOS itself), and
// one on the live medium goes away with it; both are refused.
func resolveLibraries(ctx context.Context, e *env, uuids []string, target string) ([]config.Library, error) {
	if len(uuids) == 0 {
		return nil, nil
	}
	disks, err := e.scanDisks(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing filesystems: %w", err)
	}
	mounts, _ := readMountinfo()
	live := liveDisks(mounts)
	var libs []config.Library
	for _, u := range uuids {
		d := findFS(disks, u)
		if d == nil {
			return nil, fmt.Errorf("no filesystem with UUID %s", u)
		}
		if !libraryFS[d.FSType] {
			return nil, fmt.Errorf("%s (%s) cannot hold a game library", d.Path, orElse(d.FSType, "no filesystem"))
		}
		for _, disk := range fsDisks(*d, mounts) {
			if disk == target {
				return nil, fmt.Errorf("%s is on the disk VaporOS is being installed to", d.Path)
			}
			if live[disk] {
				return nil, fmt.Errorf("%s is on the installation medium", d.Path)
			}
		}
		libs = append(libs, config.Library{
			UUID:       d.UUID,
			Label:      d.Label,
			Mountpoint: libraryMountpoint(d.Label, d.UUID),
			FSType:     d.FSType,
		})
	}
	return mergeLibraries(nil, libs), nil
}

func findFS(disks []storage.Disk, uuid string) *storage.Disk {
	for i := range disks {
		if strings.EqualFold(disks[i].UUID, uuid) {
			return &disks[i]
		}
	}
	return nil
}

// fsDisks returns the whole disks a scanned filesystem lives on.
func fsDisks(d storage.Disk, mounts []mountEntry) []string {
	out := disksOf(filepath.Base(d.Path), mounts, 0)
	if d.Parent != "" {
		out = append(out, filepath.Base(d.Parent))
	}
	return out
}
