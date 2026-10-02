package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
	"github.com/jasperaelvoet/vaporos/internal/steamlock"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Registering adopted libraries with Steam. Steam finds its libraries in
// ~/.local/share/Steam/steamapps/libraryfolders.vdf (with a copy in
// config/ that it also reads), so adopting a disk adds its library there
// and its games show up without anyone opening Steam's settings.
//
// Steam keeps that list in memory and writes it back when it exits, so an
// edit made while it runs would be lost. The list is only changed while
// no Steam (and no gamescope, which starts Steam) runs as the gaming user;
// until then the library waits in steam-libraries.json, and Run adds it
// the next time it sees Steam stopped, at the latest on the next boot.
// Everything is read and written through gamerfs: the home belongs to the
// gaming user, who could otherwise plant symlinks for vosd to follow. The
// lists are edited under the Steam lock (internal/steamlock), which `vos
// steam prepare` holds while it rewrites Steam's files.

// steamLists are Steam's library lists, relative to the gaming user's
// home. The first must exist (Steam creates it on its first start); the
// copy is updated only when it exists.
var steamLists = []string{
	".local/share/Steam/steamapps/libraryfolders.vdf",
	".local/share/Steam/config/libraryfolders.vdf",
}

const (
	// regInterval is how often Run looks for a stopped Steam while a
	// registration waits.
	regInterval = 15 * time.Second
	// maxSteamFile bounds what is read of a Steam file.
	maxSteamFile = 4 << 20
	// newLibraryDir is created on an adopted disk that holds no library
	// yet: the folder Steam itself creates when it adds a drive.
	newLibraryDir = "SteamLibrary"
	// steamLockWait bounds the wait for the Steam lock. Prepare holds it
	// for a few seconds at most; a registration that cannot have it waits
	// in steam-libraries.json for the next round.
	steamLockWait = 2 * time.Second
)

// lockSteam takes the Steam lock (a variable for tests). Without the
// gaming user's runtime directory its user manager is not running, so
// nobody holds the lock (prepare runs in that manager) and nobody can
// start Steam meanwhile: the lists are edited without it.
var lockSteam = func() (func(), error) {
	if _, err := os.Lstat(config.GamerRuntimeDir); errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), steamLockWait)
	defer cancel()
	return steamlock.Lock(ctx)
}

var (
	// errNoSteamList means Steam has never run, so there is no list to add to.
	errNoSteamList = errors.New("Steam has not created its library list yet")
	// errSteamStarting means Steam, or gamescope with prepare before it,
	// started while the registration waited for the lock.
	errSteamStarting = errors.New("Steam is starting")
)

// gamescopeUnit is the gaming user's unit that runs prepare, then Steam.
const gamescopeUnit = "vos-gamescope.service"

// gamescopeStarting reports whether gamescope's unit is anything but
// down: on its way up its prepare step runs and Steam starts right after,
// and on its way down Steam writes its lists back.
func gamescopeStarting() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch sysd.ActiveState(ctx, gamescopeUnit, true) {
	case "inactive", "failed", "":
		return false
	}
	return true
}

// pendingPath lists the libraries still to be added to Steam's list.
func pendingPath() string { return filepath.Join(config.StateDir, "steam-libraries.json") }

type pendingLibraries struct {
	Pending []string `json:"pending"`
	// Seeded lists adopted filesystems (by UUID) whose library was offered
	// to Steam once. One the user later removes in Steam stays removed.
	Seeded []string `json:"seeded,omitempty"`
}

// Run adds waiting libraries to Steam's list whenever Steam is not
// running. The live ISO has no Steam to register with.
func (s *Service) Run(ctx context.Context) {
	if config.IsLive() {
		return
	}
	s.seedAdopted()
	s.applyPending()
	t := time.NewTicker(regInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.seedAdopted()
			s.applyPending()
		}
	}
}

// seedAdopted offers every adopted library to Steam once. Libraries the
// installer adopted never went through the adopt route, and a disk that
// was not mounted yet is tried again on the next round.
func (s *Service) seedAdopted() {
	libs := s.cfg.Snapshot().Storage.Libraries
	s.regMu.Lock()
	seeded := s.loadPendingFile().Seeded
	s.regMu.Unlock()
	for _, lib := range libs {
		if lib.UUID == "" || slices.Contains(seeded, lib.UUID) {
			continue
		}
		rel, ok := steam.LibraryIn(lib.Mountpoint)
		if !ok {
			continue
		}
		if _, err := s.registerLibrary(filepath.Join(lib.Mountpoint, rel)); err != nil {
			log.Printf("storage: %v", err)
			continue
		}
		s.markSeeded(lib.UUID)
	}
}

// markSeeded records that uuid's library was offered to Steam.
func (s *Service) markSeeded(uuid string) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	p := s.loadPendingFile()
	if slices.Contains(p.Seeded, uuid) {
		return
	}
	p.Seeded = append(p.Seeded, uuid)
	if err := config.WriteJSONAtomic(pendingPath(), p, 0o644); err != nil {
		log.Printf("storage: cannot save %s: %v", pendingPath(), err)
	}
}

// registerLibrary adds dir to Steam's library list now if Steam is not
// running, else queues it for Run. It reports whether Steam lists dir
// (or a library inside it) now.
func (s *Service) registerLibrary(dir string) (bool, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if libraryListed(steamLibraries(), dir) {
		return true, s.unqueueLocked(dir)
	}
	if !s.steamRunning() {
		err := addToSteam(dir, s.steamRunning)
		if err == nil {
			log.Printf("storage: added %s to Steam's libraries", dir)
			return true, s.unqueueLocked(dir)
		}
		if !errors.Is(err, errNoSteamList) && !errors.Is(err, errSteamStarting) {
			log.Printf("storage: adding %s to Steam's libraries: %v (trying again later)", dir, err)
		}
	}
	pending := s.loadPending()
	if !slices.Contains(pending, dir) {
		pending = append(pending, dir)
	}
	return false, s.savePending(pending)
}

// applyPending adds every waiting library to Steam's list, if Steam is not
// running. Libraries it cannot add yet keep waiting.
func (s *Service) applyPending() {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	pending := s.loadPending()
	if len(pending) == 0 || s.steamRunning() {
		return
	}
	listed := steamLibraries()
	var keep []string
	for _, dir := range pending {
		if libraryListed(listed, dir) {
			continue // added in Steam meanwhile
		}
		if err := addToSteam(dir, s.steamRunning); err != nil {
			if errors.Is(err, errSteamStarting) {
				keep = append(keep, dir)
				continue
			}
			if !errors.Is(err, errNoSteamList) && !s.regFailed[dir] {
				log.Printf("storage: adding %s to Steam's libraries: %v (trying again while Steam is stopped)", dir, err)
			}
			s.regFailed[dir] = true
			keep = append(keep, dir)
			continue
		}
		delete(s.regFailed, dir)
		log.Printf("storage: added %s to Steam's libraries", dir)
	}
	if len(keep) != len(pending) {
		if err := s.savePending(keep); err != nil {
			log.Printf("storage: %v", err)
		}
	}
}

// forgetPending drops waiting libraries on a disk that is no longer adopted.
func (s *Service) forgetPending(mountpoint string) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	pending := s.loadPending()
	keep := slices.DeleteFunc(slices.Clone(pending), func(dir string) bool { return within(dir, mountpoint) })
	if len(keep) != len(pending) {
		if err := s.savePending(keep); err != nil {
			log.Printf("storage: %v", err)
		}
	}
}

// pendingOn reports whether a library on the disk at mountpoint waits to
// be added to Steam's list.
func pendingOn(pending []string, mountpoint string) bool {
	return slices.ContainsFunc(pending, func(dir string) bool { return within(dir, mountpoint) })
}

func (s *Service) unqueueLocked(dir string) error {
	pending := s.loadPending()
	if !slices.Contains(pending, dir) {
		return nil
	}
	return s.savePending(slices.DeleteFunc(slices.Clone(pending), func(p string) bool { return p == dir }))
}

func (s *Service) loadPendingFile() pendingLibraries {
	var p pendingLibraries
	if err := config.ReadJSON(pendingPath(), &p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("storage: %v", err)
	}
	return p
}

func (s *Service) loadPending() []string { return s.loadPendingFile().Pending }

func (s *Service) savePending(dirs []string) error {
	if dirs == nil {
		dirs = []string{}
	}
	p := s.loadPendingFile()
	p.Pending = dirs
	if err := config.WriteJSONAtomic(pendingPath(), p, 0o644); err != nil {
		return fmt.Errorf("cannot save %s: %w", pendingPath(), err)
	}
	return nil
}

// steamLibraries returns the library folders in Steam's lists.
func steamLibraries() []string {
	var out []string
	for _, rel := range steamLists {
		data, err := gamerfs.ReadFile(config.GamerHome, rel, maxSteamFile)
		if err != nil {
			continue
		}
		if paths, err := steam.ParseLibraryFolders(data); err == nil {
			out = append(out, paths...)
		}
	}
	return out
}

// libraryListed reports whether Steam lists dir or a library inside it
// (one Steam made on the disk itself, such as <disk>/SteamLibrary).
func libraryListed(listed []string, dir string) bool {
	return slices.ContainsFunc(listed, func(p string) bool { return within(p, dir) })
}

// within reports whether p is dir or lies below it. On VaporOS /mnt is a
// symlink to /var/mnt, and a list copied from another install may use it.
func within(p, dir string) bool {
	p, dir = canonMnt(p), canonMnt(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func canonMnt(p string) string {
	p = filepath.Clean(p)
	if p == "/mnt" || strings.HasPrefix(p, "/mnt/") {
		return "/var" + p
	}
	return p
}

// addToSteam adds dir to Steam's library lists, under the Steam lock,
// unless running says Steam started meanwhile: prepare may hold the lock
// until just before Steam starts, and Steam would write its own copy of
// the lists back over the edit.
func addToSteam(dir string, running func() bool) error {
	unlock, err := lockSteam()
	if err != nil {
		return fmt.Errorf("Steam's files are in use: %w", err)
	}
	defer unlock()
	if running() {
		return errSteamStarting
	}
	label, contentID := libraryMarker(dir)
	uid, gid := -1, -1
	if os.Geteuid() == 0 {
		uid, gid = config.GamerUID, config.GamerUID
	}
	for i, rel := range steamLists {
		data, perm, err := readSteamFile(rel)
		if errors.Is(err, fs.ErrNotExist) {
			if i == 0 {
				return errNoSteamList
			}
			continue
		}
		if err != nil {
			return err
		}
		out, added, err := steam.AddLibraryFolder(data, dir, label, contentID)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if added {
			if err := gamerfs.WriteFile(config.GamerHome, rel, out, perm, uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}

// readSteamFile reads a Steam file in the gaming user's home with its mode.
func readSteamFile(rel string) ([]byte, os.FileMode, error) {
	f, err := gamerfs.Open(config.GamerHome, rel)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSteamFile+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxSteamFile {
		return nil, 0, fmt.Errorf("%s: too large", rel)
	}
	return data, fi.Mode().Perm(), nil
}

// libraryMarker reads the label and content id from the libraryfolder.vdf
// Steam keeps in a library, without following a symlink out of it.
func libraryMarker(dir string) (label, contentID string) {
	data, err := gamerfs.ReadFile(dir, "libraryfolder.vdf", maxSteamFile)
	if err != nil {
		return "", "0"
	}
	return steam.ParseLibraryFolder(data)
}

// makeLibrary turns an adopted disk without a Steam library into one:
// <mountpoint>/SteamLibrary with steamapps/ and the libraryfolder.vdf
// marker, owned by the gaming user, which is what Steam creates when it
// adds a drive (and cannot, on a filesystem whose root only root may
// write to). Nothing that exists is changed.
func makeLibrary(mountpoint string) (string, error) {
	uid := config.GamerUID
	if os.Geteuid() != 0 {
		uid = -1
	}
	if err := gamerfs.MkdirAll(mountpoint, newLibraryDir+"/steamapps", 0o755, uid, uid); err != nil {
		return "", err
	}
	marker := newLibraryDir + "/libraryfolder.vdf"
	if f, err := gamerfs.Open(mountpoint, marker); err == nil {
		f.Close()
	} else if errors.Is(err, fs.ErrNotExist) {
		data := []byte("\"libraryfolder\"\n{\n\t\"contentid\"\t\t\"0\"\n\t\"label\"\t\t\"\"\n}\n")
		if err := gamerfs.WriteFile(mountpoint, marker, data, 0o644, uid, uid); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	return filepath.Join(mountpoint, newLibraryDir), nil
}

// gamerSteamRunning reports whether Steam, or the gamescope session that
// starts it, runs as uid. When /proc cannot be read it says yes: an edit
// under a running Steam is lost, a wait is not.
func gamerSteamRunning(procDir string, uid int) bool {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !isPID(e.Name()) {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		fi, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil {
			continue
		}
		switch c := strings.TrimSpace(string(comm)); {
		case c == "steam", c == "steam.sh", c == "steamwebhelper", strings.HasPrefix(c, "gamescope"):
			return true
		}
	}
	return false
}

func isPID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
