package steamprep

import (
	"path/filepath"
	"slices"
	"strconv"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

func manifestPath(lib string, app uint32) string {
	return filepath.Join(lib, "steamapps", "appmanifest_"+strconv.FormatUint(uint64(app), 10)+".acf")
}

// findManifest returns app's manifest in the first library that has one,
// or nil when none does (it is not installed).
func findManifest(libs []string, app uint32) (*file, error) {
	for _, lib := range libs {
		f, err := readFile(manifestPath(lib, app), steam.VDFMax)
		if err != nil || !f.missing {
			return f, err
		}
	}
	return nil, nil
}

// betaApps lists the apps whose branch steam.json asks for or VaporOS
// asked for before.
func (p *prep) betaApps() []uint32 {
	var ids []uint32
	if p.want != nil && !p.o.Unwrap {
		for _, a := range p.want.Apps {
			if a.Beta != nil {
				ids = append(ids, a.App)
			}
		}
	}
	for k, a := range p.st.Apps {
		if a.Beta != nil {
			if id, err := parseAppID(k); err == nil {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// decideBeta returns the branch an app's manifest should ask for and
// VaporOS's record (nil: none). Each request is applied once: after that
// the manifest asks for whatever the user picks in Steam. A branch no
// longer asked for goes back to the one before, unless it was changed
// meanwhile.
func decideBeta(cur string, b *BetaState, want *BetaWant) (string, *BetaState) {
	if want != nil {
		switch {
		case b != nil && b.Request == want.Request:
			return cur, b
		case b == nil && cur == want.Branch:
			// Already so: VaporOS takes it over, with the public branch
			// to go back to.
			return cur, &BetaState{Wrote: cur, Request: want.Request}
		}
		next := &BetaState{Wrote: want.Branch, Before: cur, Request: want.Request}
		if b != nil && cur == b.Wrote {
			next.Before = b.Before
		}
		return want.Branch, next
	}
	if b != nil && cur == b.Wrote {
		return b.Before, nil
	}
	return cur, nil
}

// uninstalled drops the branch record of an app no library has the
// manifest of: a reinstall comes with a new manifest, which then gets
// the request that still stands. A library whose steamapps is
// missing (its drive is not mounted), or one a libraryfolders.vdf that
// does not parse may list, may still hold it, so the record waits for
// it: dropped, the next good run would apply the request again over the
// branch the user picked.
func (p *prep) uninstalled(app uint32) {
	a := p.st.peekApp(app)
	if a == nil || a.Beta == nil || p.libsPartial {
		return
	}
	for _, lib := range p.libraries() {
		if !dirExists(filepath.Join(lib, "steamapps")) {
			return
		}
	}
	p.o.Log.Printf("prepare: app %d is not installed; its branch is asked for again when it is", app)
	a.Beta = nil
	p.commit()
}

// branches is step 5: the BetaKey of each app's appmanifest, which Steam
// reads when it starts. An app that is not installed is left for later.
// --unwrap leaves branches alone, and so does an app whose request in
// steam.json was not well formed.
func (p *prep) branches() {
	if p.o.Unwrap {
		return
	}
	want := map[uint32]*BetaWant{}
	keep := map[uint32]bool{}
	for _, a := range p.want.Apps {
		switch {
		case a.keepBranch:
			keep[a.App] = true
		case a.Beta != nil && !p.isTool(a.App):
			want[a.App] = a.Beta
		}
	}
	libs := p.libraries()
	for _, app := range p.betaApps() {
		if keep[app] {
			continue
		}
		f, err := findManifest(libs, app)
		if err != nil {
			p.fail("appmanifest_"+acctKey(app)+".acf", err)
			continue
		}
		if f == nil {
			p.uninstalled(app)
			continue
		}
		name := relName(p.root, f.path)
		cur, _, err := steam.BetaKey(f.data)
		if err != nil {
			p.fail(name, err)
			continue
		}
		var b *BetaState
		if a := p.st.peekApp(app); a != nil {
			b = a.Beta
		}
		branch, next := decideBeta(cur, b, want[app])
		if branch != cur {
			data, _, err := steam.SetBetaKey(f.data, branch)
			if err != nil {
				p.fail(name, err)
				continue
			}
			if !p.step("writing " + name) {
				return
			}
			if err := f.write(data); err != nil {
				p.fail(name, err)
				continue
			}
			p.o.Log.Printf("prepare: %s: branch %q", name, branch)
		}
		if next != nil {
			p.st.app(app).Beta = next
		} else if a := p.st.peekApp(app); a != nil {
			a.Beta = nil
		}
		p.commit()
	}
}
