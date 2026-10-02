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
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Remove drops id from wanted and reconciles (docs/CONTRACTS.md
// "Extensions", Control center): the next pass proposes the set without
// it, and its files stay mounted until the restart. Then, holding id's
// helper lock (a setup under way is stopped and waited for), it undoes
// what runs now: the units this boot started stop, the helper's Remove
// runs when it set the extension up or this boot mounted it, and purge
// deletes its data. The removal stands when that fails; the card says so.
func (s *Service) Remove(ctx context.Context, id string, purge bool) error {
	// Counted before wanted changes: an install that ends meanwhile leaves
	// the undo to this removal.
	s.mu.Lock()
	s.cc.removing[id]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.cc.removing[id]--; s.cc.removing[id] <= 0 {
			delete(s.cc.removing, id)
		}
		s.mu.Unlock()
	}()
	d, err := s.unwant(ctx, id)
	if err != nil {
		return err
	}
	log.Printf("extensions: removing %s (purge %v)", id, purge)
	s.mu.Lock()
	if cancel := s.cc.installing[id]; cancel != nil {
		cancel() // it is undone anyway
	}
	s.mu.Unlock()

	// The undo outlives a page that goes away.
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), helperTimeout)
	defer cancel()
	err = s.undo(hctx, id, d, purge)
	back, _ := stillWanted(s.catalog(), id)
	s.mu.Lock()
	delete(s.cc.tries, id)
	if err != nil && !back {
		s.cc.notes[id] = refusalNote(id, err, removeNote(s.name(id)))
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

// unwant checks a removal and takes id out of wanted, one change at a
// time. It returns id's shipped descriptor (nil for one this version
// lacks).
func (s *Service) unwant(ctx context.Context, id string) (*descriptor.Descriptor, error) {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	e, _, wanted, err := s.known(id)
	if err != nil {
		return nil, err
	}
	name := s.name(id)
	if e.Core {
		return nil, refuse(http.StatusConflict, name+" is part of VaporOS and cannot be removed.")
	}
	cat := s.catalog()
	if deps := requiredBy(cat, wantSet(cat, wanted), id); len(deps) > 0 {
		var names []string
		for _, dep := range deps {
			names = append(names, s.name(dep))
		}
		return nil, refuse(http.StatusConflict, fmt.Sprintf("%s needs %s. Remove %s first.", joinNames(names), name, joinNames(names)))
	}
	if err := s.needDescs(cat, id); err != nil {
		return nil, err
	}

	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := store.Lock(lctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if wanted, err = store.Wanted(); err != nil {
		return nil, err
	}
	if err := store.WriteWanted(slices.DeleteFunc(wanted, func(w string) bool { return w == id })); err != nil {
		return nil, err
	}
	return s.desc(id), nil
}

// undo is a removal's work on what runs now, under id's helper lock. An
// extension added back before it starts is left as it is; one added back
// while it runs keeps its data and gets its units back at the end (Install
// starts what is stopped after that), and the next pass sets it up again.
func (s *Service) undo(ctx context.Context, id string, d *descriptor.Descriptor, purge bool) error {
	unlock, err := s.lockID(ctx, id)
	if err != nil {
		return fmt.Errorf("waiting for its helper: %w", err)
	}
	defer unlock()
	cat := s.catalog()
	if back, _ := stillWanted(cat, id); back {
		log.Printf("extensions: %s was added again before its removal ran; it stays", id)
		return nil
	}
	mounted := bootMounted(id)
	var errs []error
	if d != nil && mounted {
		s.stopUnits(ctx, id, d)
	}
	if d != nil && (mounted || isInstalled(id)) {
		back, _ := stillWanted(cat, id)
		s.helperBusy(1)
		err := HelperFor(id).Remove(ctx, s.ext(id, d), purge && !back)
		s.helperBusy(-1)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := unmarkInstalled(id); err != nil {
		errs = append(errs, err)
	}
	if purge {
		if err := s.purge(ctx, cat, id); err != nil {
			errs = append(errs, err)
		}
	}
	if back, _ := stillWanted(cat, id); back && mounted {
		log.Printf("extensions: %s was added again while its removal ran; starting it again", id)
		s.restore(ctx, id)
	}
	return errors.Join(errs...)
}

// stoppedUnits are the units of an extension a removal stopped this boot
// (template instances by name), which adding it back starts again.
type stoppedUnits struct {
	system, user []string
}

// stopUnits stops the extension's services now: removing it must stop
// what it runs, not leave it running until the restart. What was running
// is recorded first; without that list, the units that are not templates
// stand in for it.
func (s *Service) stopUnits(ctx context.Context, id string, d *descriptor.Descriptor) {
	var rec stoppedUnits
	for _, user := range []bool{false, true} {
		var patterns, plain []string
		for _, u := range d.Services {
			if (u.Scope == "user") != user {
				continue
			}
			unit := u.Unit
			if i := strings.Index(unit, "@."); i >= 0 {
				unit = unit[:i] + "@*" + unit[i+1:] // every instance of a template
			} else {
				plain = append(plain, unit)
			}
			patterns = append(patterns, unit)
		}
		if len(patterns) == 0 {
			continue
		}
		running, err := s.cc.listUnits(ctx, user, patterns...)
		if err != nil {
			log.Printf("extensions: %s: listing %v: %v", id, patterns, err)
			running = plain
		}
		if err := s.cc.systemctl(ctx, user, append([]string{"stop", "--"}, patterns...)...); err != nil {
			log.Printf("extensions: %s: stopping %v: %v", id, patterns, err)
		}
		if user {
			rec.user = running
		} else {
			rec.system = running
		}
	}
	s.mu.Lock()
	// A second removal finds them stopped: what the first stopped stays.
	old := s.cc.stopped[id]
	s.cc.stopped[id] = stoppedUnits{system: union(old.system, rec.system), user: union(old.user, rec.user)}
	s.mu.Unlock()
}

// union is a, then the names of b it lacks.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// startUnits starts the units a removal stopped this boot again: id is
// added back before the restart that would have dropped it.
func (s *Service) startUnits(ctx context.Context, id string) {
	s.mu.Lock()
	rec, ok := s.cc.stopped[id]
	delete(s.cc.stopped, id)
	s.mu.Unlock()
	if !ok {
		return
	}
	for _, units := range []struct {
		user  bool
		names []string
	}{{false, rec.system}, {true, rec.user}} {
		if len(units.names) == 0 {
			continue
		}
		if err := s.cc.systemctl(ctx, units.user, append([]string{"start", "--"}, units.names...)...); err != nil {
			log.Printf("extensions: %s: starting %v again: %v", id, units.names, err)
		}
	}
}

// listActiveUnits is the units matching patterns that run (or start or
// reload), by name.
func listActiveUnits(ctx context.Context, user bool, patterns ...string) ([]string, error) {
	args := []string{"list-units", "--plain", "--no-legend", "--full", "--state=active,activating,reloading", "--"}
	if user {
		args = append([]string{"--user", "-M", config.GamerUser + "@"}, args...)
	}
	out, err := sysd.Run(ctx, "systemctl", append(args, patterns...)...)
	if err != nil {
		return nil, err
	}
	var units []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	return units, nil
}

// purge deletes id's system and home data areas and its settings, unless
// it was added back: under the change lock, so no add comes in between.
// Library areas are the helper's to delete: only it knows the disk. The
// home area is deleted as vapor, whose tree it is (CONTRACTS Users).
func (s *Service) purge(ctx context.Context, cat *catalog.Catalog, id string) error {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	if back, _ := stillWanted(cat, id); back {
		log.Printf("extensions: %s was added again; its data stays", id)
		return nil
	}
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
	if err := s.needDescs(s.catalog(), id); err != nil {
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

// Action runs one of id's descriptor actions through its helper while the
// extension runs, one helper call for id at a time and counting as busy:
// as vapor through `vos ext action` (run_as vapor, the default), or in
// vosd (run_as root). A failure answers in the action's label alone.
func (s *Service) Action(ctx context.Context, id, name string, args json.RawMessage) error {
	if _, _, _, err := s.known(id); err != nil {
		return err
	}
	d := s.desc(id)
	var a descriptor.Action
	i := -1
	if d != nil {
		i = slices.IndexFunc(d.Actions, func(a descriptor.Action) bool { return a.Name == name })
	}
	if i < 0 {
		return refuse(http.StatusNotFound, fmt.Sprintf("%s has no action %q", s.name(id), name))
	}
	a = d.Actions[i]
	if !bootMounted(id) {
		return refuse(http.StatusConflict, fmt.Sprintf("%s is not running yet. Restart VaporOS first.", s.name(id)))
	}
	hctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	// Why it failed, and its args, go to the log only.
	failed := func(err error) error {
		log.Printf("extensions: %s: %s %s: %v", id, name, oneLine(string(args), maxLoggedArgs), err)
		if errors.Is(hctx.Err(), context.DeadlineExceeded) {
			return refuse(http.StatusInternalServerError, a.Label+" didn't finish in time. Try again.")
		}
		return refuse(http.StatusInternalServerError, a.Label+" didn't finish. Try again.")
	}
	unlock, err := s.lockID(hctx, id)
	if err != nil {
		return failed(fmt.Errorf("waiting for its helper: %w", err))
	}
	defer unlock()
	s.helperBusy(1)
	defer s.helperBusy(-1)
	if a.RunAs == "root" {
		err = HelperFor(id).Action(hctx, s.ext(id, d), name, args)
	} else {
		err = s.actionAsGamer(hctx, id, name, args)
	}
	if errors.Is(err, ErrNoAction) {
		return refuse(http.StatusNotFound, fmt.Sprintf("%s has no action %q", s.name(id), name))
	}
	if err != nil {
		return failed(err)
	}
	log.Printf("extensions: %s: ran %s", id, name)
	s.syncSteam() // a helper's Steam parts may follow its actions (TruckersMP's branch)
	return nil
}
