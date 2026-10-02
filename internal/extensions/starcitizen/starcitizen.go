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
// vapor download the launcher's installer into it. It refuses before it
// touches a drive when none is picked, and a drive that is not connected,
// has another filesystem or too little space.
func (helper) Install(ctx context.Context, x *extensions.Ext) error {
	p, err := placeFor(diskSetting(x))
	if err != nil {
		return err
	}
	ms, err := readMounts()
	if err != nil {
		log.Printf("star-citizen: reading the mount table: %v", err)
		return errors.New("VaporOS couldn't check which drives are connected. Try again.")
	}
	m, err := driveMount(ms, p)
	if err != nil {
		return errors.New(p.problem(err))
	}
	area := games(x.Desc)
	if !slices.Contains(area.FS, m.FSType) {
		return fmt.Errorf("%s is formatted as %s, which Star Citizen can't run from. Pick a drive formatted as %s.", capital(p.Name), m.FSType, orList(area.FS))
	}
	if !safeRe.MatchString(p.Prefix()) {
		return fmt.Errorf("The folder on %s has characters the RSI Launcher can't handle. Pick another drive.", p.Name)
	}
	uuid, err := fsUUID(m.Source)
	if err != nil {
		log.Printf("star-citizen: %v", err)
		return fmt.Errorf("VaporOS couldn't tell which drive %s is. Pick another drive.", p.Name)
	}
	installed := launcherInstalled(p)
	need := uint64(area.MinFreeGB) * 1e9
	if !installed {
		if free, err := freeBytes(m.Point); err == nil && free < need {
			return fmt.Errorf("%s has %d GB free, and Star Citizen needs %d GB. Free up space or pick another drive.", capital(p.Name), free/1e9, area.MinFreeGB)
		}
	}

	uid := gamerID()
	cantWrite := fmt.Errorf("VaporOS couldn't write to %s. Check that it has space, then try again.", p.Name)
	if err := gamerfs.MkdirAll(p.Base, p.Rel, 0o755, uid, uid); err != nil {
		log.Printf("star-citizen: making %s: %v", p.Prefix(), err)
		return cantWrite
	}
	if err := gamerfs.WriteFile(p.Base, path.Join(p.Rel, markerName), []byte(uuid+"\n"), 0o644, uid, uid); err != nil {
		log.Printf("star-citizen: marking %s: %v", p.Prefix(), err)
		return cantWrite
	}
	st := state{Disk: p.Disk, Prefix: p.Prefix(), UUID: uuid}
	if old, ok := readState(x.DataDir); ok && old.Prefix == st.Prefix {
		st.Installer, st.Version = old.Installer, old.Version // Steam keeps its shortcut meanwhile
	}
	if err := saveState(x.DataDir, st); err != nil {
		return err
	}
	if installed && st.Installer != "" && hasInstaller(p, st.Installer) {
		// The installer only runs on a first start; the shortcut needs it there.
		log.Printf("star-citizen: prefix %s on %s (%s): the RSI Launcher is installed, keeping %s", st.Prefix, p.Name, uuid, st.Installer)
		logMemory()
		return nil
	}

	out, err := asGamer(ctx, vosBin, "ext", ID, "fetch-installer", "--prefix", st.Prefix)
	if err != nil {
		log.Printf("star-citizen: %v", err)
		if line := lastLine(out); plainLine(line) {
			return errors.New(line)
		}
		return errFetch
	}
	var res fetchResult
	if err := json.Unmarshal([]byte(lastLine(out)), &res); err != nil || !installerRe.MatchString(res.Installer) {
		log.Printf("star-citizen: fetch-installer printed %q", lastLine(out))
		return errFetch
	}
	st.Installer, st.Version = res.Installer, res.Version
	if err := saveState(x.DataDir, st); err != nil {
		return err
	}
	log.Printf("star-citizen: prefix %s on %s (%s), RSI Launcher %s", st.Prefix, p.Name, uuid, st.Version)
	logMemory()
	return nil
}

var errFetch = errors.New("The RSI Launcher's installer didn't download. Check the internet connection, then try again.")

// plainLine reports whether fetch-installer's last line is one of its
// sentences for the person, not JSON or Go's words.
func plainLine(line string) bool {
	return line != "" && line[0] >= 'A' && line[0] <= 'Z' && strings.HasSuffix(line, ".")
}

// saveState writes state.json, logging why it could not.
func saveState(dataDir string, st state) error {
	err := os.MkdirAll(dataDir, 0o755)
	if err == nil {
		err = writeState(dataDir, st)
	}
	if err != nil {
		log.Printf("star-citizen: %v", err)
		return errors.New("VaporOS couldn't save where Star Citizen is. Try again.")
	}
	return nil
}

func logMemory() {
	if w := memoryWarning(); w != "" {
		log.Printf("star-citizen: %s", w)
	}
}

// launcherInstalled reports whether the RSI Launcher is in the prefix at p,
// looked at as root without following a symlink (CONTRACTS Users).
func launcherInstalled(p place) bool { return isFileAt(p, launcherRel) }

// hasInstaller reports whether the installer file is in p's installer
// folder, which fetch-installer gives that name only once it checked it.
func hasInstaller(p place, file string) bool { return isFileAt(p, path.Join(installerDir, file)) }

func isFileAt(p place, rel string) bool {
	f, err := gamerfs.Open(p.Base, path.Join(p.Rel, rel))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Remove leaves the prefix alone unless purge: then vapor deletes the
// recorded one, game and all, while the drive that holds it is the one
// Install recorded. On the system drive the prefix is the home data area,
// which vosd's purge deletes.
func (helper) Remove(ctx context.Context, x *extensions.Ext, purge bool) error {
	if !purge {
		return nil
	}
	st, ok := readState(x.DataDir)
	if !ok {
		return nil
	}
	p, ok := placeOf(st.Prefix)
	if !ok || p.System {
		return nil
	}
	ms, err := readMounts()
	if err != nil {
		log.Printf("star-citizen: reading the mount table: %v", err)
	}
	if _, err := onDrive(ms, p, st.UUID); err != nil {
		return fmt.Errorf("Star Citizen's drive, %s, isn't connected, so its files stay on it.", p.Name)
	}
	if _, err := asGamer(ctx, "rm", "-rf", "--one-file-system", "--", p.Prefix()); err != nil {
		log.Printf("star-citizen: deleting %s: %v", p.Prefix(), err)
		return fmt.Errorf("VaporOS couldn't delete Star Citizen's files on %s.", p.Name)
	}
	// <drive>/VaporOS goes too when nothing else is in it.
	if _, err := asGamer(ctx, "rmdir", "--ignore-fail-on-non-empty", "--", filepath.Dir(p.Prefix())); err != nil {
		log.Printf("star-citizen: %v", err)
	}
	return nil
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
	p, ok := placeOf(st.Prefix)
	if !ok {
		return nil
	}
	var out []extensions.StatusLine
	ms, err := readMounts()
	if err != nil {
		log.Printf("star-citizen: reading the mount table: %v", err)
	}
	if m, err := reach(ms, p, st.UUID); err != nil {
		out = append(out, extensions.StatusLine{Text: p.problem(err), Tone: "warning"})
	} else {
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
	if picked := diskSetting(x); picked != "" && picked != p.Disk {
		out = append(out, extensions.StatusLine{Text: fmt.Sprintf("You picked another drive. Star Citizen stays on %s until you remove it and add it again.", p.Name), Tone: "warning"})
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

// capital starts s, a drive's name at the head of a sentence, with a
// capital letter.
func capital(s string) string {
	if s != "" && s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-'a'+'A') + s[1:]
	}
	return s
}

// orList is "a, b or c".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
