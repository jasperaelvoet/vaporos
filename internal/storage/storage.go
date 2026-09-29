// Package storage finds disks and Steam libraries, adopts game library
// disks (config.json -> mount units via the systemd generator), and
// implements the generator itself.
package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

// cfgMu serializes this package's read-modify-save cycles on the shared
// configuration. See the contract note in the package tests: config has
// no lock of its own yet.
var cfgMu sync.Mutex

type Service struct {
	cfg *config.Config

	// Seams for tests.
	scan      func(context.Context) ([]Disk, error)
	systemctl func(ctx context.Context, args ...string) error
	isActive  func(ctx context.Context, unit string) bool
	mntBase   string
}

func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg:       cfg,
		scan:      ScanDisks,
		systemctl: sysd.Systemctl,
		isActive:  func(ctx context.Context, unit string) bool { return sysd.IsActive(ctx, unit, false) },
		mntBase:   "/var/mnt",
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
	cfgMu.Lock()
	libs := append([]config.Library(nil), s.cfg.Storage.Libraries...)
	cfgMu.Unlock()

	adopted := map[string]bool{}
	for _, l := range libs {
		adopted[l.UUID] = true
	}
	found := map[string]bool{}
	out := []Disk{}
	for _, d := range disks {
		if d.FSType == "" || d.FSType == "swap" {
			continue
		}
		d.Adopted = d.UUID != "" && adopted[d.UUID]
		found[d.UUID] = true
		out = append(out, d)
	}
	for _, l := range libs {
		if !found[l.UUID] {
			out = append(out, Disk{UUID: l.UUID, Label: l.Label, FSType: l.FSType, Adopted: true, Missing: true})
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"disks": out})
}

// handleAdopt serves POST /storage/libraries {"uuid"}: record the disk in
// config.json, regenerate the mount units and mount it now. The files on
// the disk are never touched (no chown): a library from another Linux
// install is owned by uid 1000, which is vapor here too.
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
	case !adoptable[disk.FSType]:
		api.Error(w, http.StatusBadRequest, "%s filesystems cannot hold a Steam library (use ext4, btrfs, xfs or NTFS)", disk.FSType)
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

	library := lib.Mountpoint
	if disk.LibraryDir != "" && disk.LibraryDir != "." {
		library = filepath.Join(lib.Mountpoint, disk.LibraryDir)
	}
	hint := fmt.Sprintf("In Steam, open Settings > Storage > Add Drive and choose %s.", library)
	if disk.SteamLibrary {
		hint = fmt.Sprintf("This disk already holds a Steam library. In Steam, open Settings > Storage > Add Drive and choose %s; its installed games reappear without downloading.", library)
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"mountpoint": lib.Mountpoint, "library": library, "hint": hint})
}

// addLibrary records d in the configuration. Adopting an adopted disk
// again is not an error: it retries the mount.
func (s *Service) addLibrary(d Disk) (config.Library, bool, error) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	for _, l := range s.cfg.Storage.Libraries {
		if l.UUID == d.UUID {
			return l, false, nil
		}
	}
	name := s.mountName(d)
	lib := config.Library{
		UUID:       d.UUID,
		Label:      firstNonEmpty(d.Label, name),
		Mountpoint: filepath.Join(s.mntBase, name),
		FSType:     mountType(d.FSType),
	}
	prev := s.cfg.Storage.Libraries
	s.cfg.Storage.Libraries = append(append([]config.Library(nil), prev...), lib)
	if err := s.cfg.Save(); err != nil {
		s.cfg.Storage.Libraries = prev
		return config.Library{}, false, fmt.Errorf("cannot save configuration: %w", err)
	}
	return lib, true, nil
}

func (s *Service) forgetLibrary(uuid string) (config.Library, bool, error) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	prev := s.cfg.Storage.Libraries
	var keep []config.Library
	var gone config.Library
	found := false
	for _, l := range prev {
		if l.UUID == uuid && !found {
			gone, found = l, true
			continue
		}
		keep = append(keep, l)
	}
	if !found {
		return config.Library{}, false, nil
	}
	if keep == nil {
		keep = []config.Library{}
	}
	s.cfg.Storage.Libraries = keep
	if err := s.cfg.Save(); err != nil {
		s.cfg.Storage.Libraries = prev
		return gone, true, fmt.Errorf("cannot save configuration: %w", err)
	}
	return gone, true, nil
}

// mountName picks the directory under /var/mnt: the filesystem label made
// safe for a path and a unit name, else "disk-<uuid prefix>"; with a
// numeric suffix when another library or a non-empty directory has it.
// Called with cfgMu held.
func (s *Service) mountName(d Disk) string {
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
		for _, l := range s.cfg.Storage.Libraries {
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
	cfgMu.Lock()
	var lib *config.Library
	for _, l := range s.cfg.Storage.Libraries {
		if l.UUID == uuid {
			lib = &l
			break
		}
	}
	cfgMu.Unlock()
	if lib == nil {
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
