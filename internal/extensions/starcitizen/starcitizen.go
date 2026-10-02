// Package starcitizen is the star-citizen extension's helper: it puts Star
// Citizen's Proton prefix on the drive the person picks, downloads the RSI
// Launcher's installer into it, and turns the "Star Citizen" Steam
// shortcut into the launcher's first or later start (docs/CONTRACTS.md
// "Extensions", Star Citizen).
package starcitizen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// ID is the extension's id.
const ID = "star-citizen"

const (
	shortcutKey  = "launcher"
	markerName   = ".vaporos-drive" // in the prefix: the UUID of the filesystem it was made on
	installerDir = "installer"
	umuID        = "umu-starcitizen"
	diskKey      = "disk"
)

// Seams for tests.
var (
	asGamer   = sysd.AsGamer
	vosBin    = "/usr/bin/vos"
	freeBytes = statfsFree
)

func init() {
	extensions.RegisterHelper(ID, helper{})
	extensions.RegisterCommand(ID, "fetch-installer --prefix DIR: as vapor, download the RSI Launcher's installer into DIR/installer", cli)
}

type helper struct{ extensions.NopHelper }

// games is the descriptor's library data area: how much space and which
// filesystems Star Citizen needs.
func games(d *descriptor.Descriptor) descriptor.Data {
	if d != nil {
		for _, a := range d.Data {
			if a.Where == "library" {
				return a
			}
		}
	}
	return descriptor.Data{MinFreeGB: 150, FS: []string{"ext4", "btrfs", "xfs", "f2fs"}}
}

func diskSetting(x *extensions.Ext) string {
	s, _ := x.Settings[diskKey].(string)
	return normalizeDisk(s)
}

// gamerID is the owner of what Install makes: vapor, or the caller in
// tests that do not run as root.
func gamerID() int {
	if os.Geteuid() != 0 {
		return -1
	}
	return config.GamerUID
}

// Install puts the prefix on the drive the disk setting names and has
// vapor download the launcher's installer into it. It refuses a drive that
// is not connected, has another filesystem or too little space.
func (helper) Install(ctx context.Context, x *extensions.Ext) error {
	p, err := placeFor(diskSetting(x))
	if err != nil {
		return err
	}
	ms, err := readMounts()
	if err != nil {
		return fmt.Errorf("reading the mount table: %w", err)
	}
	m, err := driveMount(ms, p)
	if err != nil {
		return fmt.Errorf("%s isn't connected. Connect it, or pick another drive for it.", p.Name)
	}
	area := games(x.Desc)
	if !slices.Contains(area.FS, m.FSType) {
		return fmt.Errorf("%s is formatted as %s, which Star Citizen can't run from. Pick a drive formatted as %s.", p.Name, m.FSType, orList(area.FS))
	}
	if !safeRe.MatchString(p.Prefix()) {
		return fmt.Errorf("the folder on %s has characters the RSI Launcher can't handle. Pick another drive.", p.Name)
	}
	uuid, err := fsUUID(m.Source)
	if err != nil {
		log.Printf("star-citizen: %v", err)
		return fmt.Errorf("VaporOS couldn't tell which drive %s is. Pick another drive.", p.Name)
	}
	need := uint64(area.MinFreeGB) * 1e9
	if !launcherInstalled(p) {
		if free, err := freeBytes(m.Point); err == nil && free < need {
			return fmt.Errorf("%s has %d GB free, and Star Citizen needs %d GB. Free up space or pick another drive.", p.Name, free/1e9, area.MinFreeGB)
		}
	}

	uid := gamerID()
	if err := gamerfs.MkdirAll(p.Base, p.Rel, 0o755, uid, uid); err != nil {
		return fmt.Errorf("making its folder on %s: %w", p.Name, err)
	}
	if err := gamerfs.WriteFile(p.Base, path.Join(p.Rel, markerName), []byte(uuid+"\n"), 0o644, uid, uid); err != nil {
		return fmt.Errorf("marking its folder on %s: %w", p.Name, err)
	}
	st := state{Disk: p.Disk, Prefix: p.Prefix(), UUID: uuid}
	if old, ok := readState(x.DataDir); ok && old.Prefix == st.Prefix {
		st.Installer, st.Version = old.Installer, old.Version // Steam keeps its shortcut meanwhile
	}
	if err := os.MkdirAll(x.DataDir, 0o755); err != nil {
		return err
	}
	if err := writeState(x.DataDir, st); err != nil {
		return err
	}

	out, err := asGamer(ctx, vosBin, "ext", ID, "fetch-installer", "--prefix", st.Prefix)
	if err != nil {
		log.Printf("star-citizen: %v", err)
		if line := lastLine(out); line != "" && !strings.HasPrefix(line, "{") {
			return errors.New(line)
		}
		return errors.New("the RSI Launcher's installer didn't download. Check the internet connection, then try again.")
	}
	var res fetchResult
	if err := json.Unmarshal([]byte(lastLine(out)), &res); err != nil || !installerRe.MatchString(res.Installer) {
		return fmt.Errorf("vos ext %s fetch-installer said %q", ID, lastLine(out))
	}
	st.Installer, st.Version = res.Installer, res.Version
	if err := writeState(x.DataDir, st); err != nil {
		return err
	}
	log.Printf("star-citizen: prefix %s on %s (%s), RSI Launcher %s", st.Prefix, p.Name, uuid, st.Version)
	if w := memoryWarning(); w != "" {
		log.Printf("star-citizen: %s", w)
	}
	return nil
}

// launcherInstalled reports whether the RSI Launcher is in the prefix at p,
// looked at as root without following a symlink (CONTRACTS Users).
func launcherInstalled(p place) bool {
	f, err := gamerfs.Open(p.Base, path.Join(p.Rel, launcherRel))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Remove leaves the prefix alone unless purge: then vapor deletes it, game
// and all, from the drive it is on.
func (helper) Remove(ctx context.Context, x *extensions.Ext, purge bool) error {
	if !purge {
		return nil
	}
	var places []place
	if st, ok := readState(x.DataDir); ok {
		if p, err := placeFor(st.Disk); err == nil && p.Prefix() == st.Prefix {
			places = append(places, p)
		}
	}
	if p, err := placeFor(diskSetting(x)); err == nil && !slices.ContainsFunc(places, func(q place) bool { return q.Prefix() == p.Prefix() }) {
		places = append(places, p)
	}
	ms, err := readMounts()
	if err != nil {
		return fmt.Errorf("reading the mount table: %w", err)
	}
	var errs []error
	for _, p := range places {
		if _, err := driveMount(ms, p); err != nil {
			if st, ok := readState(x.DataDir); ok && st.Prefix == p.Prefix() {
				errs = append(errs, fmt.Errorf("%s isn't connected, so the files in VaporOS/%s on it stay.", p.Name, ID))
			}
			continue
		}
		if _, err := asGamer(ctx, "rm", "-rf", "--one-file-system", "--", p.Prefix()); err != nil {
			log.Printf("star-citizen: %v", err)
			errs = append(errs, fmt.Errorf("VaporOS couldn't delete its files on %s.", p.Name))
			continue
		}
		if !p.System {
			syscall.Rmdir(filepath.Dir(p.Prefix())) // <drive>/VaporOS, if nothing else is in it
		}
	}
	return errors.Join(errs...)
}

// Steam gives the shortcut its target once the installer is there: the
// installer itself, which is always there (the launch hook starts the
// launcher instead), in the prefix.
func (helper) Steam(x *extensions.Ext) extensions.SteamParts {
	st, ok := readState(x.DataDir)
	if !ok || st.Installer == "" {
		return extensions.SteamParts{}
	}
	return extensions.SteamParts{Shortcuts: map[string]extensions.ShortcutTarget{
		shortcutKey: {Exe: path.Join(st.Prefix, installerDir, st.Installer), StartDir: st.Prefix},
	}}
}

// Status says where Star Citizen is, how much room is left there, whether
// the launcher is in, and when the PC has less memory than it wants.
func (helper) Status(ctx context.Context, x *extensions.Ext) []extensions.StatusLine {
	var out []extensions.StatusLine
	if st, ok := readState(x.DataDir); ok {
		out = append(out, driveLines(x, st)...)
	}
	if w := memoryWarning(); w != "" {
		out = append(out, extensions.StatusLine{Text: w, Tone: "warning"})
	}
	return out
}

func driveLines(x *extensions.Ext, st state) []extensions.StatusLine {
	p, err := placeFor(st.Disk)
	if err != nil || p.Prefix() != st.Prefix {
		return nil
	}
	var out []extensions.StatusLine
	ms, _ := readMounts()
	m, err := driveMount(ms, p)
	if err == nil {
		if uuid, uerr := fsUUID(m.Source); uerr != nil || !strings.EqualFold(uuid, st.UUID) {
			err = errNotConnected
		}
	}
	switch {
	case err != nil && p.System:
		out = append(out, extensions.StatusLine{Text: "Its files on the system drive are missing. Remove Star Citizen and install it again.", Tone: "warning"})
	case err != nil:
		out = append(out, extensions.StatusLine{Text: fmt.Sprintf("Its drive, %s, isn't connected. Connect it to play.", p.Name), Tone: "warning"})
	default:
		where := fmt.Sprintf("Its files are on %s", p.Name)
		if free, err := freeBytes(m.Point); err == nil {
			where += fmt.Sprintf(", which has %d GB free", free/1e9)
		}
		out = append(out, extensions.StatusLine{Text: where + "."})
		switch {
		case launcherInstalled(p):
			out = append(out, extensions.StatusLine{Text: "The RSI Launcher is installed."})
		case st.Installer != "":
			out = append(out, extensions.StatusLine{Text: "Start Star Citizen in Steam: its first start installs the RSI Launcher."})
		}
	}
	if picked := diskSetting(x); picked != st.Disk {
		out = append(out, extensions.StatusLine{Text: fmt.Sprintf("You picked another drive. Star Citizen stays on %s until you remove it and install it again.", p.Name), Tone: "warning"})
	}
	return out
}

func statfsFree(p string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[strings.LastIndexByte(s, '\n')+1:])
}

// orList is "a, b or c".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
