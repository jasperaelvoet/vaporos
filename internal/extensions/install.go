package extensions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
// extension that runs as root or sets kernel module options needs.
func (s *Service) Install(ctx context.Context, id string, options map[string]any, authorize func() error) error {
	s.cc.change.Lock()
	defer s.cc.change.Unlock()
	e, inCat, wanted, err := s.known(id)
	if err != nil {
		return err
	}
	name := s.name(id)
	if !inCat {
		return refuse(http.StatusConflict, fmt.Sprintf("This version of VaporOS does not have %s.", name))
	}
	if e.Core {
		return refuse(http.StatusConflict, name+" is part of VaporOS and always on.")
	}
	d := s.desc(id)
	opts, err := checkSettings(d, options)
	if err != nil {
		return refuse(http.StatusBadRequest, err.Error())
	}
	cat := s.catalog()
	want := wantSet(cat, wanted)
	var adding []string
	for _, a := range cat.Closure([]string{id}) {
		if !want[a] {
			adding = append(adding, a)
		}
	}
	if err := s.checkConflicts(cat, want, adding); err != nil {
		return err
	}
	if err := s.checkSpace(cat, adding); err != nil {
		return err
	}
	needsPassword := false
	modules := moduleSettings(d)
	for k := range opts {
		needsPassword = needsPassword || modules[k]
	}
	for _, a := range adding {
		if ad := s.desc(a); ad != nil && (ad.RunsAsRoot() || ad.HasModuleOptions()) {
			needsPassword = true
		}
	}
	if needsPassword {
		if err := authorize(); err != nil {
			return err
		}
	}

	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	unlock, err := store.Lock(lctx)
	if err != nil {
		return err
	}
	defer unlock()
	if wanted, err = store.Wanted(); err != nil {
		return err
	}
	ids := adding
	if !slices.Contains(ids, id) {
		ids = append(slices.Clone(adding), id)
	}
	for _, a := range ids {
		if ae, _ := cat.Get(a); !ae.Core && !slices.Contains(wanted, a) {
			wanted = append(wanted, a)
		}
	}
	if err := store.WriteWanted(wanted); err != nil {
		return err
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
			return err
		}
	}
	s.forget(ids...)
	log.Printf("extensions: adding %v", ids)
	s.Reconcile()
	return nil
}

// checkConflicts refuses adding what cannot run beside an extension in
// want or beside another of adding: one names the other, or a capability
// it provides, in conflicts, or both provide the same capability.
func (s *Service) checkConflicts(cat *catalog.Catalog, want map[string]bool, adding []string) error {
	others := slices.Clone(adding)
	for _, id := range cat.IDs() {
		if want[id] {
			others = append(others, id)
		}
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

// runInstalls runs the helper's Install, once, for every extension this
// boot mounted that is wanted and has not been set up (no installed
// marker), as root, one at a time and each within helperTimeout. A
// failure is the card's reason until a later pass's try works (at most
// maxHelperInstalls per boot, then only "Try again"). It reports whether
// one failed with tries left.
func (s *Service) runInstalls(ctx context.Context, cat *catalog.Catalog, rep *store.BootReport) (retry bool) {
	wanted, err := store.Wanted()
	if err != nil {
		log.Printf("extensions: %v", err)
		return false
	}
	want := wantSet(cat, wanted)
	for _, m := range rep.Mounted {
		if ctx.Err() != nil {
			return false
		}
		d := s.desc(m.ID)
		if !want[m.ID] || d == nil || isInstalled(m.ID) {
			continue
		}
		s.mu.Lock()
		tries := s.cc.tries[m.ID]
		if tries < maxHelperInstalls {
			s.cc.tries[m.ID]++
			s.cc.helpers++
		}
		s.mu.Unlock()
		if tries >= maxHelperInstalls {
			continue
		}
		s.changed()
		hctx, cancel := context.WithTimeout(ctx, helperTimeout)
		err := HelperFor(m.ID).Install(hctx, s.ext(m.ID, d))
		cancel()
		s.cc.change.Lock()
		// A removal while it ran wins: no marker, so a later add sets it up again.
		if w, werr := store.Wanted(); err == nil && werr == nil && wantSet(cat, w)[m.ID] {
			err = markInstalled(m.ID)
		}
		s.cc.change.Unlock()
		s.mu.Lock()
		s.cc.helpers--
		if err != nil {
			s.cc.notes[m.ID] = installNoteText + err.Error()
		} else {
			delete(s.cc.notes, m.ID)
		}
		s.mu.Unlock()
		if err != nil {
			log.Printf("extensions: setting up %s: %v", m.ID, err)
			retry = retry || tries+1 < maxHelperInstalls
		} else {
			log.Printf("extensions: set up %s", m.ID)
		}
		s.changed()
	}
	return retry
}
