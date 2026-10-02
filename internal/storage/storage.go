// Package storage finds disks and Steam libraries, adopts game library
// disks (config.json -> mount units via the systemd generator, and the
// library into Steam's list), and implements the generator itself.
package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

type Disk struct {
	Path         string `json:"path"` // partition or disk device
	Parent       string `json:"parent,omitempty"`
	Type         string `json:"type,omitempty"` // lsblk TYPE: disk, part, rom, crypt, lvm, …
	Model        string `json:"model"`
	Size         int64  `json:"size"`
	Transport    string `json:"transport,omitempty"`
	Removable    bool   `json:"removable"`
	UUID         string `json:"uuid,omitempty"`
	Label        string `json:"label,omitempty"`
	FSType       string `json:"fstype,omitempty"`
	PartLabel    string `json:"partlabel,omitempty"`
	MountedAt    string `json:"mounted_at,omitempty"`
	IsSystem     bool   `json:"is_system"`
	SteamLibrary bool   `json:"steam_library"`
	LibraryDir   string `json:"library_dir,omitempty"` // library root relative to the filesystem root: "." or "SteamLibrary"
	Adopted      bool   `json:"adopted"`
	Missing      bool   `json:"missing,omitempty"` // adopted, but no such filesystem is attached
	Free         int64  `json:"free,omitempty"`
	// Registered: adopted, and Steam's library list has a library on it.
	// RegistrationPending: VaporOS adds it to that list once Steam is not
	// running (Steam rewrites the list when it exits).
	Registered          bool `json:"registered"`
	RegistrationPending bool `json:"registration_pending,omitempty"`
}

// ScanDisks lists block devices and filesystems (lsblk -J -b -o …): every
// disk (Parent == "") followed by what is on it. Filesystems get their
// Steam library status; unmounted ones are looked into through a brief
// read-only mount (root only). Adopted is left false: it is a property of
// the machine configuration, which Service fills in.
func ScanDisks(ctx context.Context) ([]Disk, error) {
	data, err := lsblkJSON(ctx)
	if err != nil {
		return nil, err
	}
	disks, err := parseLsblk(data, liveLabel(), config.IsLive())
	if err != nil {
		return nil, err
	}
	for i := range disks {
		detectLibrary(ctx, &disks[i])
	}
	return disks, nil
}

type Service struct {
	cfg *config.Config

	regMu     sync.Mutex      // Steam's library lists and steam-libraries.json
	regFailed map[string]bool // registrations whose failure was logged

	// Seams for tests.
	scan         func(context.Context) ([]Disk, error)
	systemctl    func(ctx context.Context, args ...string) error
	isActive     func(ctx context.Context, unit string) bool
	steamRunning func() bool
	mntBase      string
}

func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg:          cfg,
		regFailed:    map[string]bool{},
		scan:         ScanDisks,
		systemctl:    sysd.Systemctl,
		isActive:     func(ctx context.Context, unit string) bool { return sysd.IsActive(ctx, unit, false) },
		steamRunning: func() bool { return gamerSteamRunning("/proc", config.GamerUID) || gamescopeStarting() },
		mntBase:      "/var/mnt",
	}
}

// Routes registers /storage*.
func (s *Service) Routes(srv *api.Server) {
	srv.Handle("GET", "/storage", api.Authed, s.handleList)
	srv.Handle("POST", "/storage/libraries", api.Authed, s.handleAdopt)
	srv.Handle("DELETE", "/storage/libraries/{uuid}", api.Authed, s.handleRemove)
}

// handleList serves GET /storage: the filesystems on this machine, plus
// adopted libraries whose disk is not attached (so they can be removed).
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	disks, err := s.scan(r.Context())
	if err != nil {
		api.Error(w, http.StatusInternalServerError, "cannot list disks: %v", err)
		return
	}
	libs := s.cfg.Snapshot().Storage.Libraries
	listed := steamLibraries()
	s.regMu.Lock()
	pending := s.loadPending()
	s.regMu.Unlock()

	adopted := map[string]config.Library{}
	for _, l := range libs {
		adopted[l.UUID] = l
	}
	steamState := func(d *Disk, mountpoint string) {
		d.Registered = libraryListed(listed, mountpoint)
		d.RegistrationPending = !d.Registered && pendingOn(pending, mountpoint)
	}
	found := map[string]bool{}
	out := []Disk{}
	for _, d := range disks {
		if d.FSType == "" || d.FSType == "swap" {
			continue
		}
		if l, ok := adopted[d.UUID]; ok && d.UUID != "" {
			d.Adopted = true
			steamState(&d, l.Mountpoint)
		}
		found[d.UUID] = true
		out = append(out, d)
	}
	for _, l := range libs {
		if !found[l.UUID] {
			d := Disk{UUID: l.UUID, Label: l.Label, FSType: l.FSType, Adopted: true, Missing: true}
			steamState(&d, l.Mountpoint)
			out = append(out, d)
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"disks": out})
}

// handleAdopt serves POST /storage/libraries {"uuid"}: record the disk in
// config.json, regenerate the mount units, mount it now and add its
// library to Steam's list (now, or once Steam is not running). Existing
// files on the disk are never changed (no chown): a library from another
// Linux install is owned by uid 1000, which is vapor here too. A disk
// without a library gets an empty SteamLibrary folder owned by vapor.
func (s *Service) handleAdopt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UUID string `json:"uuid"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if !validUUID(req.UUID) {
		api.Error(w, http.StatusBadRequest, "invalid filesystem uuid")
		return
	}
	ctx := r.Context()
	disks, err := s.scan(ctx)
	if err != nil {
		api.Error(w, http.StatusInternalServerError, "cannot list disks: %v", err)
		return
	}
	var disk *Disk
	for i := range disks {
		if disks[i].UUID == req.UUID && disks[i].FSType != "" {
			disk = &disks[i]
			break
		}
	}
	switch {
	case disk == nil:
		api.Error(w, http.StatusNotFound, "no filesystem with uuid %s is attached", req.UUID)
		return
	case disk.IsSystem:
		api.Error(w, http.StatusBadRequest, "%s is part of the VaporOS system disk", disk.Path)
		return
	case !LibraryFS(disk.FSType):
		api.Error(w, http.StatusBadRequest, "%s filesystems cannot hold a Steam library (use ext4, btrfs, xfs, f2fs or NTFS)", disk.FSType)
		return
	}

	lib, added, err := s.addLibrary(*disk)
	if err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if err := s.mountLibrary(ctx, lib); err != nil {
		if added {
			s.forgetLibrary(lib.UUID)
			if rerr := s.systemctl(context.WithoutCancel(ctx), "daemon-reload"); rerr != nil {
				log.Printf("storage: daemon-reload after failed adoption: %v", rerr)
			}
		}
		api.Error(w, http.StatusInternalServerError, "cannot mount %s: %v", disk.Path, err)
		return
	}

	library, ready := lib.Mountpoint, disk.SteamLibrary
	switch {
	case disk.SteamLibrary && disk.LibraryDir != "" && disk.LibraryDir != ".":
		library = filepath.Join(lib.Mountpoint, disk.LibraryDir)
	case !disk.SteamLibrary:
		if dir, err := makeLibrary(lib.Mountpoint); err != nil {
			log.Printf("storage: making a Steam library on %s: %v", lib.Mountpoint, err)
		} else {
			library, ready = dir, true
		}
	}
	registered, pending := false, false
	if ready {
		var err error
		registered, err = s.registerLibrary(library)
		if err != nil {
			log.Printf("storage: %v", err)
		} else {
			s.markSeeded(lib.UUID)
		}
		pending = !registered && err == nil
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"mountpoint":           lib.Mountpoint,
		"library":              library,
		"registered":           registered,
		"registration_pending": pending,
		"hint":                 adoptHint(library, disk.SteamLibrary, registered, pending),
	})
}

// adoptHint tells the user what happens next with an adopted library.
func adoptHint(library string, hadGames, registered, pending bool) string {
	games := ""
	if hadGames {
		games = " Its installed games appear in Steam without downloading."
	}
	switch {
	case registered:
		return fmt.Sprintf("%s is one of Steam's game libraries.%s", library, games)
	case pending:
		// Steam rewrites its library list when it exits, so it is only
		// changed while Steam is not running.
		return fmt.Sprintf("VaporOS adds %s to Steam's game libraries the next time Steam is not running (at the latest after a restart).%s "+
			"To use it right away, open Steam > Settings > Storage > Add Drive and choose %s.", library, games, library)
	}
	return fmt.Sprintf("In Steam, open Settings > Storage > Add Drive and choose %s.%s", library, games)
}

// addLibrary records d in the configuration. Adopting an adopted disk
// again is not an error: it retries the mount.
func (s *Service) addLibrary(d Disk) (config.Library, bool, error) {
	var lib config.Library
	var have bool
	s.cfg.View(func(c *config.Config) { lib, have = findLibrary(c.Storage.Libraries, d.UUID) })
	if have {
		return lib, false, nil
	}
	added := false
	err := s.cfg.Mutate(func(c *config.Config) {
		if lib, have = findLibrary(c.Storage.Libraries, d.UUID); have {
			return // adopted by a concurrent request
		}
		name := s.mountName(c.Storage.Libraries, d)
		lib = config.Library{
			UUID:       d.UUID,
			Label:      firstNonEmpty(d.Label, name),
			Mountpoint: filepath.Join(s.mntBase, name),
			FSType:     mountType(d.FSType),
		}
		c.Storage.Libraries = append(slices.Clone(c.Storage.Libraries), lib)
		added = true
	})
	if err != nil {
		if added {
			// config.json still has the old list; take the library out of
			// memory again (saving may fail again, which changes nothing).
			s.cfg.Mutate(func(c *config.Config) { c.Storage.Libraries = withoutLibrary(c.Storage.Libraries, d.UUID) })
		}
		return config.Library{}, false, fmt.Errorf("cannot save configuration: %w", err)
	}
	return lib, added, nil
}

func (s *Service) forgetLibrary(uuid string) (config.Library, bool, error) {
	var gone config.Library
	var found bool
	s.cfg.View(func(c *config.Config) { gone, found = findLibrary(c.Storage.Libraries, uuid) })
	if !found {
		return config.Library{}, false, nil
	}
	err := s.cfg.Mutate(func(c *config.Config) {
		if gone, found = findLibrary(c.Storage.Libraries, uuid); found {
			c.Storage.Libraries = withoutLibrary(c.Storage.Libraries, uuid)
		}
	})
	if !found {
		return config.Library{}, false, err // removed by a concurrent request
	}
	if err != nil {
		// config.json still lists it; put it back in memory too.
		s.cfg.Mutate(func(c *config.Config) {
			if _, ok := findLibrary(c.Storage.Libraries, uuid); !ok {
				c.Storage.Libraries = append(slices.Clone(c.Storage.Libraries), gone)
			}
		})
		return gone, true, fmt.Errorf("cannot save configuration: %w", err)
	}
	return gone, true, nil
}

func findLibrary(libs []config.Library, uuid string) (config.Library, bool) {
	for _, l := range libs {
		if l.UUID == uuid {
			return l, true
		}
	}
	return config.Library{}, false
}

// withoutLibrary returns a new list without uuid, never nil (config.json
// keeps "libraries": []).
func withoutLibrary(libs []config.Library, uuid string) []config.Library {
	out := []config.Library{}
	for _, l := range libs {
		if l.UUID != uuid {
			out = append(out, l)
		}
	}
	return out
}

// mountName picks the directory under /var/mnt: the filesystem label made
// safe for a path and a unit name, else "disk-<uuid prefix>"; with a
// numeric suffix when another library in libs or a non-empty directory
// has it.
func (s *Service) mountName(libs []config.Library, d Disk) string {
	base := sanitizeLabel(d.Label)
	if base == "" {
		u := strings.ToLower(strings.ReplaceAll(d.UUID, "-", ""))
		if len(u) > 8 {
			u = u[:8]
		}
		base = "disk-" + u
	}
	taken := func(name string) bool {
		p := filepath.Join(s.mntBase, name)
		for _, l := range libs {
			if l.Mountpoint == p {
				return true
			}
		}
		entries, err := os.ReadDir(p)
		return err == nil && len(entries) > 0
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// sanitizeLabel keeps [A-Za-z0-9._-], turns anything else into "_", and
// drops leading dots so the directory is never hidden.
func sanitizeLabel(label string) string {
	var b strings.Builder
	for _, c := range label {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if strings.Trim(s, "_") == "" {
		return ""
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// mountLibrary lets the generator write the unit, then starts it.
func (s *Service) mountLibrary(ctx context.Context, lib config.Library) error {
	unit, err := MountUnitName(lib.Mountpoint)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.mntBase, 0o755); err != nil {
		return err
	}
	if err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return s.systemctl(ctx, "start", unit)
}

// handleRemove serves DELETE /storage/libraries/{uuid}: unmount, then
// forget. A library in use (a game running from it) stays adopted.
func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	if !validUUID(uuid) {
		api.Error(w, http.StatusBadRequest, "invalid filesystem uuid")
		return
	}
	var lib config.Library
	var found bool
	s.cfg.View(func(c *config.Config) { lib, found = findLibrary(c.Storage.Libraries, uuid) })
	if !found {
		api.Error(w, http.StatusNotFound, "no adopted library with uuid %s", uuid)
		return
	}

	ctx := r.Context()
	if unit, err := MountUnitName(lib.Mountpoint); err == nil {
		// Stopping a unit that was never loaded fails too, so ask whether
		// the mount is still there rather than trusting the exit status.
		if err := s.systemctl(ctx, "stop", unit); err != nil && s.isActive(ctx, unit) {
			api.Error(w, http.StatusConflict, "%s is in use; quit the game running from it and try again", lib.Mountpoint)
			return
		}
	}
	if _, _, err := s.forgetLibrary(uuid); err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.forgetPending(lib.Mountpoint)
	if err := s.systemctl(ctx, "daemon-reload"); err != nil {
		log.Printf("storage: daemon-reload after removing %s: %v", lib.Mountpoint, err)
	}
	// Only ever an empty directory: os.Remove refuses anything else, and
	// a still-mounted filesystem.
	if filepath.Dir(lib.Mountpoint) == filepath.Clean(s.mntBase) {
		if err := os.Remove(lib.Mountpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("storage: leaving %s: %v", lib.Mountpoint, err)
		}
	}
	api.OK(w)
}
