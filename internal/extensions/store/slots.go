package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Slot is slots/<a|b>.json: the extension catalog of the image in that
// slot, from its signed manifest. GC keeps what it lists, so a rollback
// finds its images.
type Slot struct {
	Version    string                        `json:"version"`
	Extensions map[string]manifest.Extension `json:"extensions"`
}

func slotPath(slot string) (string, error) {
	if slot != "a" && slot != "b" {
		return "", fmt.Errorf("invalid slot %q", slot)
	}
	return filepath.Join(config.ExtSlotsDir(), slot+".json"), nil
}

// WriteSlot records the extensions of the image version in slot. Caller
// holds the update lock.
func WriteSlot(slot, version string, exts map[string]manifest.Extension) error {
	path, err := slotPath(slot)
	if err != nil {
		return err
	}
	if !manifest.ValidVersion(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	s := &Slot{Version: version, Extensions: exts}
	if s.Extensions == nil {
		s.Extensions = map[string]manifest.Extension{}
	}
	if err := s.validate(); err != nil {
		return err
	}
	return config.WriteJSONAtomic(path, s, 0o644)
}

// ReadSlot reads a slot file; a missing one is nil.
func ReadSlot(slot string) (*Slot, error) {
	path, err := slotPath(slot)
	if err != nil {
		return nil, err
	}
	s := &Slot{}
	err = config.ReadJSON(path, s)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// RemoveSlot removes a slot file (the slot is being rewritten or erased).
// Caller holds the update lock.
func RemoveSlot(slot string) error {
	path, err := slotPath(slot)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Catalog returns the slot's extensions as a catalog.
func (s *Slot) Catalog() *catalog.Catalog {
	if s == nil {
		return &catalog.Catalog{}
	}
	return catalog.FromManifest(&manifest.Manifest{Extensions: s.Extensions})
}

func (s *Slot) validate() error {
	for id, e := range s.Extensions {
		if !manifest.ValidExtensionID(id) || !isHex64(e.SHA256) || !isHex64(e.FSVerity) || e.Size <= 0 {
			return fmt.Errorf("bad extension entry %q", id)
		}
		for _, r := range e.Requires {
			if _, ok := s.Extensions[r]; !ok || r == id {
				return fmt.Errorf("extension %q requires %q, which is not listed", id, r)
			}
		}
	}
	return nil
}
