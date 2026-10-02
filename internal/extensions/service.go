package extensions

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// busyReason is what keeps the PC awake while an image downloads, seals or
// is re-read (power's busy reasons).
const busyReason = "adding an extension"

// Retry schedule while an image is still missing: retryBase after the first
// pass that left one, doubling up to retryMax. Variables for tests.
var (
	retryBase = time.Minute
	retryMax  = 30 * time.Minute
)

// slotLockWait bounds the wait for the update lock to write the booted
// slot file; a stage holds it for minutes, and the next pass tries again.
const slotLockWait = 10 * time.Second

// Service is vosd's side of the extensions (docs/CONTRACTS.md
// "Extensions"): on an installed system it keeps the booted slot's catalog
// file, fetches and seals the images the wanted extensions need, reconciles
// the store's sets, collects its garbage, and once per boot reads the
// mounted images through to find damaged ones.
type Service struct {
	cfg  *config.Config // shared: read through Snapshot
	kick chan struct{}

	mu        sync.Mutex
	fetching  int  // downloads under way
	rehashing bool // the once-per-boot re-read is under way
	rehashed  bool
	progress  map[string]*Progress // by id, downloads for the booted version
	errs      map[string]string    // by id: why its image for the booted version is missing
	damaged   map[string]bool      // sha256 of images deleted as damaged this boot
	bad       map[string]bool      // sha256 the source serves other bytes for: not fetched again until Reconcile
	noVerity  bool                 // vos_data cannot seal: nothing is fetched this boot
	view      view                 // what the last pass saw, for Status

	// options renders the module options of a set's ids from their
	// settings; nil until an extension has any.
	options func(ids []string) []string
}

// NewService returns the extensions service; Run does the work.
func NewService(cfg *config.Config) *Service {
	if cfg == nil {
		cfg = config.Defaults()
	}
	return &Service{
		cfg:      cfg,
		kick:     make(chan struct{}, 1),
		progress: map[string]*Progress{},
		errs:     map[string]string{},
		damaged:  map[string]bool{},
		bad:      map[string]bool{},
	}
}

// booted is the running image: its catalog (in the read-only root, so it
// never changes during a boot), version and slot.
type booted struct {
	cat      *catalog.Catalog
	version  string
	slot     string
	slotDone bool // slots/<slot>.json lists cat
}

func loadBooted() (*booted, error) {
	cat, err := catalog.Load(config.ExtCatalogPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b := &booted{cat: cat}
	if slot := config.BootedSlot(); slot == "a" || slot == "b" {
		b.slot = slot
	}
	if ii, err := config.LoadImageInfo(); err == nil {
		b.version = ii.Version
	} else {
		log.Printf("extensions: image version: %v", err)
	}
	return b, nil
}

// Run reconciles at start, whenever Reconcile asks, and, while an image is
// still missing, again after retryBase, doubling up to retryMax. After the
// first pass it re-reads the mounted images once. The live system has no
// store: Run returns at once.
func (s *Service) Run(ctx context.Context) {
	if config.IsLive() {
		return
	}
	if err := store.CleanTemp(); err != nil {
		log.Printf("extensions: %v", err)
	}
	b, err := loadBooted()
	if err != nil {
		log.Printf("extensions: %v", err)
		s.mu.Lock()
		s.view.err = err.Error()
		s.mu.Unlock()
		return
	}
	failures := 0
	for {
		retry := s.pass(ctx, b)
		if ctx.Err() != nil {
			return
		}
		if s.firstRehash() && s.rehash(ctx, b.cat) {
			continue // fetch the deleted images again now
		}
		var t *time.Timer
		var wait <-chan time.Time
		if retry {
			failures++
			t = time.NewTimer(retryDelay(failures))
			wait = t.C
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
		case <-s.kick:
			failures = 0
			s.retryBad()
		case <-wait:
		}
		if t != nil {
			t.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func retryDelay(failures int) time.Duration {
	d := retryBase << min(max(failures-1, 0), 16)
	if d <= 0 || d > retryMax {
		return retryMax
	}
	return d
}

// Reconcile asks Run for another pass, after a change to wanted, a
// setting or the store. It never blocks.
func (s *Service) Reconcile() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// retryBad lets the next pass fetch again the images whose source served
// other bytes: someone asked for a pass.
func (s *Service) retryBad() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.bad)
}

// Busy keeps the PC awake while an image downloads, seals or is re-read.
func (s *Service) Busy() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetching > 0 || s.rehashing {
		return true, busyReason
	}
	return false, ""
}

// firstRehash reports whether the once-per-boot re-read is still to do,
// and marks it done.
func (s *Service) firstRehash() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := !s.rehashed
	s.rehashed = true
	return first
}
