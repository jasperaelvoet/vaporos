package extensions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// lockWait bounds how long a change through the API waits for the store
// lock; a pass holds it only for its store writes.
const lockWait = 10 * time.Second

// maxHelperInstalls bounds the tries of one extension's helper Install per
// boot; "Try again" allows more.
const maxHelperInstalls = 3

// catalog is the booted catalog: the last pass's, or read now before the
// first pass.
func (s *Service) catalog() *catalog.Catalog {
	s.mu.Lock()
	cat := s.view.cat
	s.mu.Unlock()
	if cat != nil {
		return cat
	}
	cat, err := catalog.Load(config.ExtCatalogPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("extensions: %v", err)
	}
	if cat == nil {
		cat = &catalog.Catalog{}
	}
	return cat
}

// known looks id up: its catalog entry, and whether it is in the catalog
// or at least wanted (an extension this version lacks). Anything else is
// 404.
func (s *Service) known(id string) (catalog.Entry, bool, []string, error) {
	if !manifest.ValidExtensionID(id) {
		return catalog.Entry{}, false, nil, refuse(http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	wanted, err := store.Wanted()
	if err != nil {
		return catalog.Entry{}, false, nil, err
	}
	e, ok := s.catalog().Get(id)
	if !ok && !slices.Contains(wanted, id) && !bootMounted(id) {
		return e, false, wanted, refuse(http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	return e, ok, wanted, nil
}

// bootMounted reports whether this boot mounted id.
func bootMounted(id string) bool {
	rep, err := store.LoadBootReport()
	return err == nil && rep.IsMounted(id)
}

// wantSet is wanted ∪ core with their requirements, as cat lists them.
func wantSet(cat *catalog.Catalog, wanted []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range cat.Closure(append(slices.Clone(wanted), cat.Core()...)) {
		out[id] = true
	}
	return out
}

// Install adds id, with what it requires, to wanted and reconciles
// (docs/CONTRACTS.md "Extensions", Control center): the next pass fetches
// the images and proposes the set a restart tries. options are its first
// settings. authorize checks the admin password, which adding an
// extension that runs as root or sets kernel module options needs. One
// that is still mounted, removed before the restart, gets back its data
// areas and the units its removal stopped.
func (s *Service) Install(ctx context.Context, id string, options map[string]any, authorize func() error) error {
	ids, err := s.want(ctx, id, options, authorize)
	if err != nil {
		return err
	}
	cat := s.catalog()
	for _, a := range ids {
		if bootMounted(a) {
			s.addBack(ctx, cat, a)
		}
	}
	s.syncSteam() // one removed until the restart is back in Steam at once
	s.syncPorts()
	s.Reconcile()
	return nil
}

// addBack restores what a removal this boot took from id, which is
// mounted and wanted again. A removal under way is waited for (it holds
// the helper lock): what it stops before it sees id wanted again is
// started here, and what it stops after that it starts itself (undo).
func (s *Service) addBack(ctx context.Context, cat *catalog.Catalog, id string) {
	s.mu.Lock()
	_, stopped := s.cc.stopped[id]
	removing := s.cc.removing[id] > 0
	s.mu.Unlock()
	if !stopped && !removing {
		return
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), helperTimeout)
	defer cancel()
	unlock, err := s.lockID(uctx, id)
	if err != nil {
		log.Printf("extensions: %s: starting its units again: %v", id, err)
		return
	}
	defer unlock()
	if ok, _ := stillWanted(cat, id); ok {
		s.restore(uctx, id)
	}
}

// restore makes id's data areas (a purge deleted them) and starts the
// units a removal stopped, under its helper lock: id was added back
// before the restart that would have dropped it.
func (s *Service) restore(ctx context.Context, id string) {
	makeDataAreasOf(id, s.desc(id))
	s.startUnits(ctx, id)
}

// want checks an addition and writes it, one change at a time: id and
// what it requires join wanted, and each of them with settings but no
// settings file gets one. It returns the ids it added.
func (s *Service) want(ctx context.Context, id string, options map[string]any, authorize func() error) ([]string, error) {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	ids, err := s.checkAdd(id, options, authorize)
	if err != nil {
		return nil, err
	}
	opts, _ := checkSettings(s.desc(id), options)
	cat := s.catalog()

	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := store.Lock(lctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	wanted, err := store.Wanted()
	if err != nil {
		return nil, err
	}
	for _, a := range ids {
		if ae, _ := cat.Get(a); !ae.Core && !slices.Contains(wanted, a) {
			wanted = append(wanted, a)
		}
	}
	if err := store.WriteWanted(wanted); err != nil {
		return nil, err
	}
	for _, a := range ids {
		ad := s.desc(a)
		if ad == nil || len(ad.Settings) == 0 {
			continue
		}
		change := map[string]any{}
		if a == id {
			change = opts
		}
		if len(change) == 0 && readSettingsFile(a) != nil {
			continue
		}
		if err := saveSettings(a, ad, change); err != nil {
			return nil, err
		}
	}
	s.forget(ids...)
	log.Printf("extensions: adding %v", ids)
	return ids, nil
}

// checkAdd refuses what adding id may not do, and returns what it adds:
// id and its requirements not wanted yet.
func (s *Service) checkAdd(id string, options map[string]any, authorize func() error) ([]string, error) {
	e, inCat, wanted, err := s.known(id)
	if err != nil {
		return nil, err
	}
	name := s.name(id)
	if !inCat {
		return nil, refuse(http.StatusConflict, fmt.Sprintf("This version of VaporOS does not have %s.", name))
	}
	if e.Core {
		return nil, refuse(http.StatusConflict, name+" is part of VaporOS and always on.")
	}
	cat := s.catalog()
	if err := s.needDescs(cat, id); err != nil {
		return nil, err
	}
	d := s.desc(id)
	opts, err := checkSettings(d, options)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, err.Error())
	}
	want := wantSet(cat, wanted)
	var adding []string
	for _, a := range cat.Closure([]string{id}) {
		if !want[a] {
			adding = append(adding, a)
		}
	}
	if err := s.checkConflicts(cat, want, adding); err != nil {
		return nil, err
	}
	if err := s.checkDrives(id, opts, append(slices.Clone(adding), id)); err != nil {
		return nil, err
	}
	if err := s.checkSpace(cat, adding); err != nil {
		return nil, err
	}
	needsPassword := false
	modules := moduleSettings(d)
	for k := range opts {
		needsPassword = needsPassword || modules[k]
	}
	for _, a := range adding {
		needsPassword = needsPassword || passwordFor(s.desc(a))
	}
	if needsPassword {
		if err := authorize(); err != nil {
			return nil, err
		}
	}
	if !slices.Contains(adding, id) {
		adding = append(adding, id)
	}
	return adding, nil
}

// checkConflicts refuses adding what cannot run beside an extension in
// want or beside another of adding: one names the other, or a capability
// it provides, in conflicts, or both provide the same capability. Every
// descriptor it compares must read (needDescs).
func (s *Service) checkConflicts(cat *catalog.Catalog, want map[string]bool, adding []string) error {
	others := slices.Clone(adding)
	for _, id := range cat.IDs() {
		if want[id] {
			others = append(others, id)
		}
	}
	if err := s.needDescs(cat, others...); err != nil {
		return err
	}
	for _, a := range adding {
		for _, o := range others {
			if a != o && clashes(s.desc(a), s.desc(o)) {
				return refuse(http.StatusConflict, fmt.Sprintf("%s cannot run together with %s.", s.name(a), s.name(o)))
			}
		}
	}
	return nil
}

// checkDrives refuses adding an extension with a required disk setting and
// no drive for it: in opts for id, else in its settings file (one kept
// from an earlier add). The install dialog asks for the drive.
func (s *Service) checkDrives(id string, opts map[string]any, adding []string) error {
	for _, a := range adding {
		d := s.desc(a)
		values := loadSettings(a, d)
		if a == id {
			maps.Copy(values, opts)
		}
		if _, missing := missingDrive(d, values); !missing {
			continue
		}
		if a == id {
			return refuse(http.StatusBadRequest, s.name(a)+" needs a game drive. Pick one to add it.")
		}
		return refuse(http.StatusBadRequest, fmt.Sprintf("%s needs %s, which needs a game drive. Add %s first.", s.name(id), s.name(a), s.name(a)))
	}
	return nil
}

func clashes(a, b *descriptor.Descriptor) bool {
	if a == nil || b == nil {
		return false
	}
	names := func(x, y *descriptor.Descriptor) bool {
		return slices.Contains(x.Conflicts, y.ID) ||
			slices.ContainsFunc(x.Conflicts, func(c string) bool { return slices.Contains(y.Provides, c) })
	}
	return names(a, b) || names(b, a) ||
		slices.ContainsFunc(a.Provides, func(p string) bool { return slices.Contains(b.Provides, p) })
}

// checkSpace refuses adding when the images it needs do not fit on the
// data partition with store.ExtReserve to spare (the store's own check
// before each download).
func (s *Service) checkSpace(cat *catalog.Catalog, adding []string) error {
	s.mu.Lock()
	noVerity := s.noVerity
	s.mu.Unlock()
	if noVerity {
		return refuse(http.StatusConflict, noVerityText)
	}
	var need int64
	for _, a := range adding {
		if e, ok := cat.Get(a); ok && !sealed(e) {
			need += e.Size
		}
	}
	if need == 0 {
		return nil
	}
	free, err := storeFree()
	if err != nil || free < 0 || free >= need+store.ExtReserve {
		return nil
	}
	return refuse(http.StatusConflict, fmt.Sprintf("There is not enough free space: it needs %s, and %s is free after the %s VaporOS keeps free.",
		gib(need), gib(max(free-store.ExtReserve, 0)), gib(store.ExtReserve)))
}

func gib(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MiB", (n+1<<20-1)>>20)
}

// forget clears what the API and the helper installs remember about ids:
// a change starts them over.
func (s *Service) forget(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.cc.notes, id)
		delete(s.cc.tries, id)
	}
}

// Retry is "Try again": the desired set's failed fingerprint is forgotten,
// so the next pass proposes it again, and the helper's install may run
// again.
func (s *Service) Retry(ctx context.Context, id string) error {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	if _, _, _, err := s.known(id); err != nil {
		return err
	}
	rep, err := store.LoadBootReport()
	if err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := store.Lock(lctx)
	if err != nil {
		return err
	}
	in, err := s.inputs(s.catalog(), rep)
	if err == nil {
		if p := store.PlanReconcile(in); p.Blocked {
			log.Printf("extensions: trying set %s… again", p.Fingerprint[:12])
			err = store.RemoveFailed(p.Fingerprint)
		}
	}
	unlock()
	if err != nil {
		return err
	}
	s.forget(id)
	s.Reconcile()
	return nil
}

// startInstalls has the helpers' installs run beside the passes, so a
// slow one holds back no download or proposal: one goroutine at a time,
// which goes over them again when a pass asked while it ran. A failure
// with tries left brings another pass after retryDelay.
func (s *Service) startInstalls(ctx context.Context, cat *catalog.Catalog, rep *store.BootReport) {
	s.mu.Lock()
	if s.cc.installer.running {
		s.cc.installer.again = true
		s.mu.Unlock()
		return
	}
	s.cc.installer.running = true
	s.cc.installs.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.cc.installs.Done()
		for {
			if tries := s.runInstalls(ctx, cat, rep); tries > 0 && ctx.Err() == nil {
				time.AfterFunc(retryDelay(tries), func() { s.signal(s.wake) })
			}
			s.mu.Lock()
			again := s.cc.installer.again && ctx.Err() == nil
			s.cc.installer.again, s.cc.installer.running = false, again
			s.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}

// waitInstalls waits for the installs' goroutine to end.
func (s *Service) waitInstalls() { s.cc.installs.Wait() }

// runInstalls runs the helper's Install, once, for every extension this
// boot mounted that is wanted and has not been set up (no installed
// marker), as root, one at a time and each within helperTimeout, under
// its helper lock. A failure with tries left is tried again by itself (the
// card stays installing); after maxHelperInstalls a boot it is the card's
// reason, and only "Try again" tries more. The error itself goes to the
// log. An extension removed while its Install ran gets no marker, and its
// helper's Remove undoes it unless the removal does. It returns the most
// tries of one that failed with tries left, or 0.
func (s *Service) runInstalls(ctx context.Context, cat *catalog.Catalog, rep *store.BootReport) (retry int) {
	for _, m := range rep.Mounted {
		if ctx.Err() != nil {
			return 0
		}
		if tries, failed := s.install(ctx, cat, m.ID); failed && tries < maxHelperInstalls {
			retry = max(retry, tries)
		}
	}
	return retry
}

// stillWanted reports whether id is in wanted ∪ core with their
// requirements now.
func stillWanted(cat *catalog.Catalog, id string) (bool, error) {
	w, err := store.Wanted()
	return err == nil && wantSet(cat, w)[id], err
}

// install runs id's helper Install when it is due, and reports its tries
// this boot and whether it failed.
func (s *Service) install(ctx context.Context, cat *catalog.Catalog, id string) (int, bool) {
	d := s.desc(id)
	if ok, _ := stillWanted(cat, id); !ok || d == nil || isInstalled(id) {
		return 0, false
	}
	unlock, err := s.lockID(ctx, id)
	if err != nil {
		return 0, false
	}
	defer unlock()
	s.mu.Lock()
	tries := s.cc.tries[id]
	s.mu.Unlock()
	// Again under the lock: a removal or another install may have come first.
	if ok, _ := stillWanted(cat, id); !ok || isInstalled(id) || tries >= maxHelperInstalls {
		return 0, false
	}
	x := s.ext(id, d)
	if st, missing := missingDrive(d, x.Settings); missing {
		// No try brings a drive: the card asks for one, and "Try again"
		// looks once it is picked.
		log.Printf("extensions: %s: not set up: no drive picked for %s", id, st.Key)
		s.mu.Lock()
		s.cc.notes[id], s.cc.tries[id] = pickDriveNote(s.name(id)), maxHelperInstalls
		s.mu.Unlock()
		s.changed()
		return 0, false
	}
	makeDataAreasOf(id, d) // a purge before it was added back deleted them
	hctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	s.mu.Lock()
	s.cc.tries[id]++
	s.cc.installing[id] = cancel
	s.mu.Unlock()
	s.helperBusy(1)
	err = HelperFor(id).Install(hctx, x)
	s.mu.Lock()
	delete(s.cc.installing, id)
	removing := s.cc.removing[id] > 0
	s.mu.Unlock()

	wanted, werr := stillWanted(cat, id)
	switch {
	case !wanted && werr == nil:
		// Removed while it ran: no marker, so adding it again sets it up again.
		log.Printf("extensions: %s was removed while it was set up (%v)", id, err)
		if !removing {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), helperTimeout)
			if err := HelperFor(id).Remove(rctx, s.ext(id, d), false); err != nil {
				log.Printf("extensions: undoing the setup of %s: %v", id, err)
			}
			rcancel()
		}
		err = nil
	case err == nil && werr != nil:
		err = werr
	case err == nil:
		err = markInstalled(id)
	}
	s.mu.Lock()
	if err != nil && wanted {
		s.cc.notes[id] = refusalNote(id, err, installNote(s.name(id), d.Core))
	} else {
		delete(s.cc.notes, id)
	}
	s.mu.Unlock()
	s.helperBusy(-1)
	if err != nil {
		log.Printf("extensions: setting up %s: %v", id, err)
		return tries + 1, true
	}
	if wanted {
		log.Printf("extensions: set up %s", id)
	}
	return tries + 1, false
}
