package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// Remove drops id from wanted and reconciles (docs/CONTRACTS.md
// "Extensions", Control center): its units stop now, its helper undoes
// what it set up (with purge, also deleting its data), and the next pass
// proposes the set without it. Its files stay mounted until the restart.
func (s *Service) Remove(ctx context.Context, id string, purge bool) error {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	e, _, wanted, err := s.known(id)
	if err != nil {
		return err
	}
	name := s.name(id)
	if e.Core {
		return refuse(http.StatusConflict, name+" is part of VaporOS and cannot be removed.")
	}
	cat := s.catalog()
	if deps := requiredBy(cat, wantSet(cat, wanted), id); len(deps) > 0 {
		var names []string
		for _, dep := range deps {
			names = append(names, s.name(dep))
		}
		return refuse(http.StatusConflict, fmt.Sprintf("%s needs %s. Remove %s first.", joinNames(names), name, joinNames(names)))
	}

	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := store.Lock(lctx)
	if err != nil {
		return err
	}
	if wanted, err = store.Wanted(); err == nil {
		err = store.WriteWanted(slices.DeleteFunc(wanted, func(w string) bool { return w == id }))
	}
	unlock()
	if err != nil {
		return err
	}
	log.Printf("extensions: removing %s (purge %v)", id, purge)

	// The helper's undo outlives a page that goes away.
	hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), helperTimeout)
	defer hcancel()
	var errs []error
	d := s.desc(id)
	if d != nil && bootMounted(id) {
		s.stopUnits(hctx, d)
	}
	if d != nil {
		if err := HelperFor(id).Remove(hctx, s.ext(id, d), purge); err != nil {
			errs = append(errs, err)
		}
	}
	if err := unmarkInstalled(id); err != nil {
		errs = append(errs, err)
	}
	if purge {
		if err := s.purge(hctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	err = errors.Join(errs...)
	s.mu.Lock()
	delete(s.cc.tries, id)
	if err != nil {
		s.cc.notes[id] = removeNoteText + err.Error()
	} else {
		delete(s.cc.notes, id)
	}
	s.mu.Unlock()
	if err != nil {
		log.Printf("extensions: removing %s: %v", id, err)
	}
	s.syncSteam() // Steam drops it now, not at the restart
	s.syncPorts() // and the firewall its ports
	s.Reconcile()
	return nil
}

// stopUnits stops the extension's services now: removing it must stop
// what it runs, not leave it running until the restart.
func (s *Service) stopUnits(ctx context.Context, d *descriptor.Descriptor) {
	var system, user []string
	for _, u := range d.Services {
		unit := u.Unit
		if i := strings.Index(unit, "@."); i >= 0 {
			unit = unit[:i] + "@*" + unit[i+1:] // every instance of a template
		}
		if u.Scope == "user" {
			user = append(user, unit)
		} else {
			system = append(system, unit)
		}
	}
	for _, units := range []struct {
		user  bool
		names []string
	}{{false, system}, {true, user}} {
		if len(units.names) == 0 {
			continue
		}
		if err := s.cc.systemctl(ctx, units.user, append([]string{"stop", "--"}, units.names...)...); err != nil {
			log.Printf("extensions: %s: stopping %v: %v", d.ID, units.names, err)
		}
	}
}

// purge deletes id's system and home data areas and its settings. Library
// areas are the helper's to delete: only it knows the disk. The home area
// is deleted as vapor, whose tree it is (CONTRACTS Users).
func (s *Service) purge(ctx context.Context, id string) error {
	var errs []error
	if err := os.RemoveAll(filepath.Join(config.ExtDataDir(), id)); err != nil {
		errs = append(errs, err)
	}
	home := filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, id)
	if _, err := os.Lstat(home); err == nil {
		if _, err := s.cc.asGamer(ctx, "rm", "-rf", "--", home); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s: %w", home, err))
		}
	}
	if err := os.Remove(settingsPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SetSettings saves a change to id's settings and reconciles: a setting
// that feeds kernel module options changes the set a restart tries, and
// changing one needs the admin password (authorize).
func (s *Service) SetSettings(ctx context.Context, id string, change map[string]any, authorize func() error) error {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	if _, _, _, err := s.known(id); err != nil {
		return err
	}
	d := s.desc(id)
	values, err := checkSettings(d, change)
	if err != nil {
		return refuse(http.StatusBadRequest, err.Error())
	}
	cur, modules := loadSettings(id, d), moduleSettings(d)
	for k, v := range values {
		if modules[k] && !reflect.DeepEqual(cur[k], v) {
			if err := authorize(); err != nil {
				return err
			}
			break
		}
	}
	if len(values) == 0 {
		return nil
	}
	// Under the lock: the reconcile reads the settings for the options.
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := store.Lock(lctx)
	if err != nil {
		return err
	}
	err = saveSettings(id, d, values)
	unlock()
	if err != nil {
		return err
	}
	s.syncSteam() // a helper's Steam parts may follow its settings
	s.Reconcile()
	return nil
}

// Action runs one of id's descriptor actions through its helper, while
// the extension runs.
func (s *Service) Action(ctx context.Context, id, name string, args json.RawMessage) error {
	if _, _, _, err := s.known(id); err != nil {
		return err
	}
	d := s.desc(id)
	if d == nil || !slices.ContainsFunc(d.Actions, func(a descriptor.Action) bool { return a.Name == name }) {
		return refuse(http.StatusNotFound, fmt.Sprintf("%s has no action %q", s.name(id), name))
	}
	if !bootMounted(id) {
		return refuse(http.StatusConflict, fmt.Sprintf("%s is not running yet. Restart VaporOS first.", s.name(id)))
	}
	hctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	err := HelperFor(id).Action(hctx, s.ext(id, d), name, args)
	if errors.Is(err, ErrNoAction) {
		return refuse(http.StatusNotFound, fmt.Sprintf("%s has no action %q", s.name(id), name))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", s.name(id), err)
	}
	log.Printf("extensions: %s: ran %s", id, name)
	return nil
}
