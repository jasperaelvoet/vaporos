package extensions

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// Shipped returns the descriptor the image ships for id
// (/usr/share/vos/extensions/<id>.json), with what the build verified.
func Shipped(id string) (*descriptor.Descriptor, error) {
	return descriptor.Load(filepath.Join(config.ExtDescriptorsDir, id+".json"))
}

// makeDataAreas creates the system and home data areas of every extension
// this boot mounted, as their descriptors declare. Extensions may not ship
// tmpfiles.d, so nothing else makes them: a system area is root's (its
// service may also use StateDirectory=), a home area vapor's, made through
// gamerfs because vapor controls the tree it is in. Library areas live on
// a disk the user picks, so the extension's install makes those.
func makeDataAreas(rep *store.BootReport) {
	if rep == nil {
		return
	}
	for _, m := range rep.Mounted {
		d, err := Shipped(m.ID)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Printf("extensions: %s: %v", m.ID, err)
			}
			continue
		}
		for _, area := range d.Data {
			if err := makeDataArea(m.ID, area.Where); err != nil {
				log.Printf("extensions: %s: data area %s: %v", m.ID, area.Name, err)
			}
		}
	}
}

func makeDataArea(id, where string) error {
	switch where {
	case "system":
		return os.MkdirAll(filepath.Join(config.ExtDataDir(), id), 0o755)
	case "home":
		return gamerfs.MkdirAll(config.GamerHome, path.Join(config.ExtGamerDataSubdir, id), 0o755,
			config.GamerUID, config.GamerUID)
	case "library":
		return nil
	}
	return fmt.Errorf("unknown place %q", where)
}
