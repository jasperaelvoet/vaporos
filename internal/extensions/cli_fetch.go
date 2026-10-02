package extensions

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

const fetchUsage = "usage: vos ext fetch [--from SRC] [--version V] [--state-dir DIR] [--seed [--repair]] [ids...]"

func init() {
	register("fetch", "[--from SRC] [--version V] [--state-dir DIR] [--seed [--repair]] [ids...]: fetch and seal images into the store",
		func(args []string) int {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return fetchCmd(ctx, args, os.Stdout, os.Stderr)
		})
}

// fetchCmd is `vos ext fetch`: the images of one image version, from its
// verified manifest, sealed into the store; with --seed also the store an
// install starts with (docs/CONTRACTS.md "Binary").
func fetchCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("vos ext fetch", flag.ContinueOnError)
	f.SetOutput(stderr)
	from := f.String("from", "", "the source (default: config.json's update.source)")
	version := f.String("version", "", "the image version (default: the booted one)")
	stateDir := f.String("state-dir", "", "the /var/lib/vos the store is in (default: this system's)")
	seed := f.Bool("seed", false, "also write slots/a.json, wanted and a pending set to try, as an install does (needs --from)")
	repair := f.Bool("repair", false, "with --seed: try core only, and remove enabled, slots/b.json, pending and failed, as a repair does")
	if err := f.Parse(args); err != nil {
		return 2
	}
	ids := f.Args()
	usage := (*repair && !*seed) || (*seed && *from == "")
	for _, id := range ids {
		usage = usage || !manifest.ValidExtensionID(id)
	}
	if usage {
		fmt.Fprintln(stderr, fetchUsage)
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "vos ext fetch: "+format+"\n", a...)
		return 1
	}

	// This system's own store cannot seal on a disk the initramfs found
	// without verity; another system's (--state-dir) is not that disk.
	stop := ""
	if *stateDir != "" {
		if fi, err := os.Stat(*stateDir); err != nil || !fi.IsDir() {
			return fail("%s is not a directory", *stateDir)
		}
		config.StateDir = *stateDir
	} else if rep, err := store.LoadBootReport(); err == nil && rep.HasReason(store.ReasonNoVerity) {
		stop = "the disk cannot seal extension images (no fs-verity)"
	}
	v := *version
	if v == "" {
		ii, err := config.LoadImageInfo()
		if err != nil {
			return fail("no --version, and no booted image version: %v", err)
		}
		v = ii.Version
	}
	spec := *from
	if spec == "" {
		c, err := config.Load()
		if err != nil {
			fmt.Fprintf(stderr, "vos ext fetch: %v (using the default source)\n", err)
		}
		spec = c.Update.Source
	}
	src, err := openSource(spec, v)
	if err != nil {
		return fail("%v", err)
	}
	m, err := src.Manifest(ctx)
	if err != nil {
		return fail("%v", err)
	}
	if m.Version != v {
		return fail("%s has version %s, not %s", src, m.Version, v)
	}
	cat := catalog.FromManifest(m)
	for _, id := range ids {
		if _, ok := cat.Get(id); !ok {
			return fail("version %s has no extension %s", v, id)
		}
	}
	base := slices.Clone(ids)
	if len(ids) == 0 {
		wanted, err := store.Wanted()
		if err != nil {
			return fail("%v", err)
		}
		base = append(base, wanted...)
	}
	if len(ids) == 0 || *seed {
		// A seeded store always wants core.
		base = append(base, cat.Core()...)
	}
	targets := cat.Closure(base)

	ok, failed := fetchEntries(ctx, src, cat, targets, stop, stdout, stderr)
	rc := 0
	if failed {
		rc = 1
	}
	if *seed {
		if err := seedStore(ctx, m, cat, ids, ok, *repair); err != nil {
			rc = fail("seeding the store: %v", err)
		}
	}
	return rc
}

// fetchEntries seals each of ids' images that the store lacks, printing
// {"bytes","total"} lines on stdout as they come (for the whole run, never
// going down). It stops at a disk that cannot seal or a source that cannot
// be reached, and with stop set does not start: the images left are
// reported not sealed. It returns the ids whose image is sealed, and
// whether any is not.
func fetchEntries(ctx context.Context, src source, cat *catalog.Catalog, ids []string, stop string, stdout, stderr io.Writer) (map[string]bool, bool) {
	ok := map[string]bool{}
	var todo []catalog.Entry
	var total int64
	for _, id := range ids {
		e, _ := cat.Get(id)
		if sealed(e) {
			ok[id] = true
			continue
		}
		todo = append(todo, e)
		total += e.Size
	}
	p := &progressLines{w: stdout, total: total, last: -1}
	p.show(0)
	failed := false
	var base int64
	for _, e := range todo {
		switch {
		case stop != "":
			fmt.Fprintf(stderr, "vos ext fetch: %s: not sealed: %s\n", e.ID, stop)
		default:
			err := fetchImage(ctx, src, e, func(done int64) { p.show(base + done) })
			if err == nil {
				ok[e.ID] = true
				break
			}
			fmt.Fprintf(stderr, "vos ext fetch: %s: %v\n", e.ID, err)
			switch {
			case errors.Is(err, store.ErrUnsupported):
				stop = "the disk cannot seal extension images (no fs-verity)"
			case ctx.Err() != nil:
				stop = "stopped"
			case unreachable(src, err):
				stop = fmt.Sprintf("%s cannot be reached", src)
			}
		}
		failed = failed || !ok[e.ID]
		base += e.Size
		p.show(base)
	}
	return ok, failed
}

// progressLines prints the bytes done so far, never fewer than before (a
// download that starts again by digest restarts its count).
type progressLines struct {
	w           io.Writer
	total, last int64
}

func (p *progressLines) show(done int64) {
	if done <= p.last {
		return
	}
	p.last = done
	fmt.Fprintf(p.w, "{\"bytes\":%d,\"total\":%d}\n", done, p.total)
}

// seedStore writes what an install starts the store with, under the update
// lock and then the store lock: slots/a.json from the manifest; with
// repair, the removal of slots/b.json, enabled, pending and failed; wanted
// (the ids given less core, which is always wanted; without ids an
// existing wanted stays, as a repair keeps the user's); and a pending set
// to try at the first boot, of core and the ids given (with repair, of
// core only: vosd proposes what else is wanted through a trial of its
// own), with their requirements, as far as their images sealed. enabled is
// never written: the first boot's trial makes it.
func seedStore(ctx context.Context, m *manifest.Manifest, cat *catalog.Catalog, ids []string, ok map[string]bool, repair bool) error {
	return update.WithLock(ctx, func() error {
		unlock, err := store.Lock(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		if repair {
			if err := store.RemoveSlot("b"); err != nil {
				return err
			}
		}
		if err := store.WriteSlot("a", m.Version, m.Extensions); err != nil {
			return err
		}
		if repair {
			if err := store.ClearEnabled(); err != nil {
				return err
			}
			if err := store.ClearPending(); err != nil {
				return err
			}
			if err := os.Remove(config.ExtFailedPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if _, err := os.Lstat(config.ExtWantedPath()); len(ids) > 0 || errors.Is(err, fs.ErrNotExist) {
			var user []string
			for _, id := range ids {
				if e, _ := cat.Get(id); !e.Core {
					user = append(user, id)
				}
			}
			if err := store.WriteWanted(user); err != nil {
				return err
			}
		}
		try := cat.Core()
		if !repair {
			try = append(try, ids...)
		}
		var set []string
		in := map[string]bool{}
		for _, id := range cat.Closure(try) {
			e, _ := cat.Get(id)
			met := ok[id]
			for _, r := range e.Requires {
				met = met && in[r]
			}
			if met {
				in[id] = true
				set = append(set, id)
			}
		}
		if len(set) == 0 {
			return nil
		}
		_, err = store.Propose(set, nil)
		return err
	})
}
