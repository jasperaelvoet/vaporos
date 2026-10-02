package extensions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"slices"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// maxSettleSteps bounds one settle: a plan needs at most a fail, a clear
// and a proposal before it asks for nothing.
const maxSettleSteps = 8

// noVerityText says why nothing is fetched on a disk that cannot seal.
const noVerityText = "The disk cannot seal extension images (no fs-verity), so VaporOS cannot add extensions."

// view is what the last pass saw, for Status.
type view struct {
	cat     *catalog.Catalog
	rep     *store.BootReport
	plan    store.Plan
	wanted  []string
	pending *store.Set
	restart bool
	version string
	err     string
}

// pass is one reconcile: the booted slot file, then, under the store lock,
// whatever the plan says to fail or clear; then the missing images for the
// booted version, fetched without the lock; then the plan acted on in full;
// then, best effort, the images the other slot's version needs; then GC.
// It reports whether an image is still missing that a later pass could
// fetch.
func (s *Service) pass(ctx context.Context, b *booted) (retry bool) {
	rep, err := store.LoadBootReport()
	if err != nil {
		s.failed(b, nil, fmt.Errorf("boot report: %w", err))
		return false
	}
	if rep.HasReason(store.ReasonNoVerity) {
		s.mu.Lock()
		s.noVerity = true
		s.mu.Unlock()
	}
	s.writeBootedSlot(ctx, b)

	p, err := s.settle(ctx, b.cat, rep, true)
	if err != nil {
		s.failed(b, rep, err)
		return true
	}
	s.record(b, rep, p) // Status shows the downloads
	if !s.fetchAll(ctx, b.version, p.Missing, true) {
		retry = true
	}
	if ctx.Err() != nil {
		return false
	}
	if p, err = s.settle(ctx, b.cat, rep, false); err != nil {
		s.failed(b, rep, err)
		return true
	}
	s.record(b, rep, p)

	if !s.fetchOther(ctx, b) {
		retry = true
	}
	if err := s.collect(ctx, b.cat, rep); err != nil {
		log.Printf("extensions: garbage collection: %v", err)
	}
	return retry
}

// failed records a pass that stopped early.
func (s *Service) failed(b *booted, rep *store.BootReport, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	log.Printf("extensions: %v", err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view.cat, s.view.version, s.view.err = b.cat, b.version, err.Error()
	if rep != nil {
		s.view.rep = rep
	}
}

// record keeps what a finished pass saw for Status.
func (s *Service) record(b *booted, rep *store.BootReport, p store.Plan) {
	wanted, _ := store.Wanted()
	pending, _ := store.Pending()
	restart := store.RestartNeeded(rep, pending, func(id string) bool {
		e, ok := b.cat.Get(id)
		return ok && sealed(e)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view = view{cat: b.cat, rep: rep, plan: p, wanted: wanted, pending: pending, restart: restart, version: b.version}
	if s.noVerity {
		s.view.err = noVerityText
	}
}

// settle applies the plan's store writes under the lock until it asks for
// none, and returns the last plan. early (before downloads) stops short of
// a proposal: the set proposed after them is the one worth trying.
func (s *Service) settle(ctx context.Context, cat *catalog.Catalog, rep *store.BootReport, early bool) (store.Plan, error) {
	unlock, err := store.Lock(ctx)
	if err != nil {
		return store.Plan{}, err
	}
	defer unlock()
	var p store.Plan
	for range maxSettleSteps {
		in, err := s.inputs(cat, rep)
		if err != nil {
			return p, err
		}
		p = store.PlanReconcile(in)
		switch p.Action {
		case store.ActionFailPending:
			log.Printf("extensions: pending set %s ran out of tries; marking it failed", setName(in.Pending))
			err = store.FailPending(cat)
		case store.ActionClearPending:
			log.Printf("extensions: removing pending set %s", setName(in.Pending))
			err = store.ClearPending()
		case store.ActionPropose:
			if early {
				return p, nil
			}
			var set *store.Set
			if set, err = store.Propose(p.IDs, p.Options); err == nil {
				log.Printf("extensions: proposed set %s %v; it is tried at the next restart", set.Name, set.IDs)
			}
		default:
			return p, nil
		}
		if err != nil {
			return p, err
		}
	}
	return p, errors.New("reconcile did not settle")
}

func setName(s *store.Set) string {
	if s == nil {
		return "(none)"
	}
	return s.Name
}

func (s *Service) inputs(cat *catalog.Catalog, rep *store.BootReport) (store.ReconcileInput, error) {
	in := store.ReconcileInput{Catalog: cat, Report: rep, Have: sealed, Options: s.options}
	var err error
	if in.Wanted, err = store.Wanted(); err != nil {
		return in, err
	}
	if in.Enabled, err = store.Enabled(); err != nil {
		return in, err
	}
	if in.Pending, err = store.Pending(); err != nil {
		return in, err
	}
	if in.Failed, err = store.Failed(); err != nil {
		return in, err
	}
	if rep.Set != "" {
		// Without the booted set, what booted is the mounted images alone.
		set, err := store.ReadSet(rep.Set)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: booted set: %v", err)
		}
		in.BootedSet = set
	}
	return in, nil
}

// writeBootedSlot records the booted catalog as slots/<booted>.json (under
// the update lock), unless the file already lists it.
func (s *Service) writeBootedSlot(ctx context.Context, b *booted) {
	if b.slotDone || b.slot == "" || !manifest.ValidVersion(b.version) {
		return
	}
	want := slotExtensions(b.cat)
	if cur, err := store.ReadSlot(b.slot); err == nil && cur != nil && cur.Version == b.version &&
		sameExtensions(cur.Extensions, want) {
		b.slotDone = true
		return
	}
	lctx, cancel := context.WithTimeout(ctx, slotLockWait)
	defer cancel()
	err := update.WithLock(lctx, func() error { return store.WriteSlot(b.slot, b.version, want) })
	if err != nil {
		log.Printf("extensions: slot %s catalog: %v", b.slot, err)
		return
	}
	b.slotDone = true
}

// slotExtensions is a catalog as a manifest's extensions map.
func slotExtensions(c *catalog.Catalog) map[string]manifest.Extension {
	out := map[string]manifest.Extension{}
	for _, e := range c.Entries {
		x := manifest.Extension{Name: manifest.ExtensionFile(e.ID), Size: e.Size, SHA256: e.SHA256, FSVerity: e.FSVerity, Core: e.Core}
		if len(e.Requires) > 0 {
			x.Requires = slices.Clone(e.Requires)
		}
		out[e.ID] = x
	}
	return out
}

// sameExtensions compares two extensions maps by what a catalog holds (a
// stage writes the manifest's, with build keys the catalog lacks).
func sameExtensions(a, b map[string]manifest.Extension) bool {
	if len(a) != len(b) {
		return false
	}
	for id, x := range a {
		y, ok := b[id]
		if !ok || x.Size != y.Size || x.SHA256 != y.SHA256 || x.FSVerity != y.FSVerity || x.Core != y.Core ||
			!slices.Equal(sortedCopy(x.Requires), sortedCopy(y.Requires)) {
			return false
		}
	}
	return true
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

// fetchOther fetches, best effort, the images the other slot's version
// needs for wanted ∪ its core. It reports whether none is missing that a
// later pass could fetch.
func (s *Service) fetchOther(ctx context.Context, b *booted) bool {
	if b.slot == "" {
		return true
	}
	sl, err := store.ReadSlot(config.OtherSlot(b.slot))
	if err != nil {
		log.Printf("extensions: %v", err)
		return true
	}
	if sl == nil || sl.Version == b.version || !manifest.ValidVersion(sl.Version) {
		return true
	}
	wanted, err := store.Wanted()
	if err != nil {
		log.Printf("extensions: %v", err)
		return true
	}
	cat := sl.Catalog()
	var missing []catalog.Entry
	for _, id := range cat.Closure(append(wanted, cat.Core()...)) {
		if e, _ := cat.Get(id); !sealed(e) {
			missing = append(missing, e)
		}
	}
	return s.fetchAll(ctx, sl.Version, missing, false)
}

// fetchAll seals entries from config.update.source at version (forBooted:
// the booted version, whose progress and errors Status shows). It reports
// whether every one is sealed now, or cannot be fetched this boot.
func (s *Service) fetchAll(ctx context.Context, version string, entries []catalog.Entry, forBooted bool) bool {
	var todo []catalog.Entry
	for _, e := range entries {
		if s.fetchable(e) {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return true
	}
	spec := s.cfg.Snapshot().Update.Source
	src, err := update.OpenSourceAt(spec, version)
	if err != nil {
		for _, e := range todo {
			s.note(e, forBooted, fmt.Errorf("%s %s from %q: %w", e.ID, version, spec, err))
		}
		return false
	}
	ok := true
	for _, e := range todo {
		if ctx.Err() != nil || !s.fetchable(e) {
			return false
		}
		s.fetchStart(e, forBooted)
		err := fetchImage(ctx, src, e, func(done int64) { s.fetchProgress(e, forBooted, done) })
		if err != nil {
			err = fmt.Errorf("%s %s: %w", e.ID, version, err)
		}
		s.fetchEnd(e, forBooted)
		s.note(e, forBooted, err)
		ok = ok && err == nil
	}
	return ok
}

// fetchable reports whether e's image may be fetched this boot.
func (s *Service) fetchable(e catalog.Entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.noVerity && !s.bad[e.SHA256]
}

func (s *Service) fetchStart(e catalog.Entry, forBooted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetching++
	if forBooted {
		s.progress[e.ID] = &Progress{Total: e.Size}
	}
}

func (s *Service) fetchProgress(e catalog.Entry, forBooted bool, done int64) {
	if !forBooted {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.progress[e.ID]; p != nil {
		p.Bytes = done
	}
}

func (s *Service) fetchEnd(e catalog.Entry, forBooted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetching--
	if forBooted {
		delete(s.progress, e.ID)
	}
}

// note records how fetching e's image went. A source that serves other
// bytes, or a disk that cannot seal them, would only download the image
// again for nothing, so neither is retried this boot (Reconcile retries
// the first).
func (s *Service) note(e catalog.Entry, forBooted bool, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("extensions: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		if forBooted {
			delete(s.errs, e.ID)
		}
		return
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, store.ErrUnsupported):
		s.noVerity = true
	case errors.Is(err, store.ErrMismatch), errors.Is(err, update.ErrChecksum):
		s.bad[e.SHA256] = true
	}
	if forBooted {
		s.errs[e.ID] = err.Error()
		if s.noVerity {
			s.errs[e.ID] = noVerityText
		}
	}
}

// collect is the store's GC, keeping what wanted ∪ core needs in the
// booted catalog and both slot files, and what this boot mounted. It skips
// a run when a slot file cannot be read: it might list images to keep.
func (s *Service) collect(ctx context.Context, cat *catalog.Catalog, rep *store.BootReport) error {
	cats := []*catalog.Catalog{cat}
	for _, slot := range []string{"a", "b"} {
		sl, err := store.ReadSlot(slot)
		if err != nil {
			return err
		}
		cats = append(cats, sl.Catalog())
	}
	unlock, err := store.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	wanted, err := store.Wanted()
	if err != nil {
		return err
	}
	removed, err := store.Collect(wanted, cats, rep)
	if len(removed) > 0 {
		log.Printf("extensions: removed %v", removed)
	}
	return err
}
