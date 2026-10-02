// Package starcitizen is the star-citizen extension's helper: it puts Star
// Citizen's Proton prefix on the drive the person picks, downloads the RSI
// Launcher's installer into it, and turns the "Star Citizen" Steam
// shortcut into the launcher's first or later start (docs/CONTRACTS.md
// "Extensions", Star Citizen).
package starcitizen

import (
	"context"
	"encoding/json"
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
// has another filesystem or too little space. What it refuses with is a
// code the person reads as its sentence (MessageText); the rest of the
// error goes to the journal.
func (helper) Install(ctx context.Context, x *extensions.Ext) error {
	p, err := placeFor(diskSetting(x))
	if err != nil {
		return err
	}
	ms, err := readMounts()
	if err != nil {
		return fmt.Errorf("reading the mount table: %w", err)
	}
	m, err := locate(ms, p, "")
	if err != nil {
		return err
	}
	area := games(x.Desc)
	if !slices.Contains(area.FS, m.FSType) {
		return refuse(codeWrongFS, "the filesystem at %s is %s", m.Point, m.FSType)
	}
	if !safeRe.MatchString(p.Prefix()) {
		return refuse(codeUnknownDrive, "%s has characters the RSI Launcher can't handle", p.Prefix())
	}
	uuid, err := fsUUID(m.Source)
	if err != nil {
		return extensions.Refuse(codeUnknownDrive, err)
	}
	installed := launcherInstalled(p)
	need := uint64(area.MinFreeGB) * 1e9
	if !installed {
		if free, err := freeBytes(m.Point); err == nil && free < need {
			return refuse(codeNoSpace, "%d GB free at %s, %d GB needed", free/1e9, m.Point, area.MinFreeGB)
		}
	}

	uid := gamerID()
	if err := gamerfs.MkdirAll(p.Base, p.Rel, 0o755, uid, uid); err != nil {
		return extensions.Refuse(codeCantWrite, fmt.Errorf("making %s: %w", p.Prefix(), err))
	}
	if err := gamerfs.WriteFile(p.Base, path.Join(p.Rel, markerName), []byte(uuid+"\n"), 0o644, uid, uid); err != nil {
		return extensions.Refuse(codeCantWrite, fmt.Errorf("marking %s: %w", p.Prefix(), err))
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

	res, err := fetchAsGamer(ctx, st.Prefix)
	if err != nil {
		return err
	}
	st.Installer, st.Version = res.Installer, res.Version
	if err := saveState(x.DataDir, st); err != nil {
		return err
	}
	log.Printf("star-citizen: prefix %s on %s (%s), RSI Launcher %s", st.Prefix, p.Name, uuid, st.Version)
	logMemory()
	return nil
}

// fetchAsGamer runs fetch-installer as vapor. Its one line on stdout,
// after anything on stderr, is the installer it fetched or the code it
// refused with. A code it does not print is no reason the person reads,
// and its other output goes to the journal only.
func fetchAsGamer(ctx context.Context, prefix string) (fetchResult, error) {
	out, err := asGamer(ctx, vosBin, "ext", ID, "fetch-installer", "--prefix", prefix)
	var res fetchResult
	jerr := json.Unmarshal([]byte(lastLine(out)), &res)
	switch {
	case err != nil && jerr == nil && fetchCodes[res.Refused]:
		return fetchResult{}, extensions.Refuse(res.Refused, err)
	case err != nil:
		return fetchResult{}, fmt.Errorf("fetching the RSI Launcher's installer: %w", err)
	case jerr != nil || !installerRe.MatchString(res.Installer):
		return fetchResult{}, fmt.Errorf("fetch-installer printed %q", lastLine(out))
	}
	return res, nil
}

// saveState writes state.json.
func saveState(dataDir string, st state) error {
	err := os.MkdirAll(dataDir, 0o755)
	if err == nil {
		err = writeState(dataDir, st)
	}
	if err != nil {
		return fmt.Errorf("saving where Star Citizen is: %w", err)
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
		return extensions.Refuse(codeFilesStay, err)
	}
	if _, err := asGamer(ctx, "rm", "-rf", "--one-file-system", "--", p.Prefix()); err != nil {
		return extensions.Refuse(codeCantDelete, fmt.Errorf("deleting %s: %w", p.Prefix(), err))
	}
	// <drive>/VaporOS goes too when nothing else is in it.
	if _, err := asGamer(ctx, "rmdir", "--ignore-fail-on-non-empty", "--", filepath.Dir(p.Prefix())); err != nil {
		log.Printf("star-citizen: %v", err)
	}
	return nil
}

// Steam gives the shortcut its target once an installer is there: the
// installer Install recorded, in the prefix. The launch hook starts the
// launcher instead, so the file may be gone by then: a newer download
// replaced it before Steam picked up the new target.
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
	if m, err := locate(ms, p, st.UUID); err != nil {
		out = append(out, extensions.StatusLine{Text: fmt.Sprintf("It's set up on %s.", p.Name)},
			extensions.StatusLine{Text: said(err), Tone: "warning"})
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

// orList is "a, b or c".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
