package extensions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// A purge only moves an extension's data areas aside under the change lock,
// to a trash name beside them, and deletes them after, in the background
// (docs/CONTRACTS.md "Extensions", Control center): Star Citizen's prefix
// on the system drive can be 100 GB, and no change through the API may
// wait for that. Adding the extension again meanwhile gets new, empty
// areas.

// trashPrefix starts a trash name, which no extension id can be.
const trashPrefix = ".trash-"

// trashTimeout bounds deleting one purge's areas; what is left goes at the
// next start. A variable for tests.
var trashTimeout = 30 * time.Minute

// trashItem is an area moved aside: the system area's root deletes, a
// home area vapor does.
type trashItem struct {
	path    string
	asVapor bool
}

func trashName(id string) string {
	return fmt.Sprintf("%s%s-%d", trashPrefix, id, time.Now().UnixNano())
}

// trashSystemArea moves id's system data area aside (root's tree).
func trashSystemArea(id string) ([]trashItem, error) {
	to := filepath.Join(config.ExtDataDir(), trashName(id))
	if err := os.Rename(filepath.Join(config.ExtDataDir(), id), to); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return []trashItem{{path: to}}, nil
}

// trashHomeArea moves id's home data area aside, in vapor's tree through
// gamerfs: the rename never follows a link vapor placed.
func trashHomeArea(id string) ([]trashItem, error) {
	name := trashName(id)
	if err := gamerfs.Rename(config.GamerHome, path.Join(config.ExtGamerDataSubdir, id), name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return []trashItem{{path: filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, name), asVapor: true}}, nil
}

// emptyTrash deletes items in the background, counting as busy: a system
// area as root, a home area as vapor, whose tree it is (CONTRACTS Users).
func (s *Service) emptyTrash(items []trashItem) {
	if len(items) == 0 {
		return
	}
	s.helperBusy(1)
	s.cc.trash.Add(1)
	go func() {
		defer s.cc.trash.Done()
		defer s.helperBusy(-1)
		ctx, cancel := context.WithTimeout(context.Background(), trashTimeout)
		defer cancel()
		for _, t := range items {
			var err error
			if t.asVapor {
				_, err = s.cc.asGamer(ctx, "rm", "-rf", "--", t.path)
			} else {
				err = os.RemoveAll(t.path)
			}
			if err != nil {
				log.Printf("extensions: deleting %s: %v", t.path, err)
			}
		}
	}()
}

// waitTrash waits for the deletions under way.
func (s *Service) waitTrash() { s.cc.trash.Wait() }

// emptyLeftoverTrash deletes what purges moved aside before vosd stopped.
// It lists under the change lock, so a purge after it moves aside names it
// did not list, which that purge deletes itself.
func (s *Service) emptyLeftoverTrash() {
	s.cc.change.Lock()
	var items []trashItem
	if ents, err := os.ReadDir(config.ExtDataDir()); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), trashPrefix) {
				items = append(items, trashItem{path: filepath.Join(config.ExtDataDir(), e.Name())})
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("extensions: %v", err)
	}
	if names, err := gamerfs.ReadDirNames(config.GamerHome, config.ExtGamerDataSubdir, 0); err == nil {
		for _, n := range names {
			if strings.HasPrefix(n, trashPrefix) {
				items = append(items, trashItem{path: filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, n), asVapor: true})
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("extensions: %v", err)
	}
	s.cc.change.Unlock()
	if len(items) > 0 {
		log.Printf("extensions: deleting %d data areas a purge left", len(items))
	}
	s.emptyTrash(items)
}
