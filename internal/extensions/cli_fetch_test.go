package extensions

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

var sum64 = strings.Repeat("0", 64)

// release writes a signed manifest of version listing imgs into the
// directory source (the images themselves are served separately) and
// trusts its key.
func (e *env) release(version string, imgs ...image) {
	e.t.Helper()
	m := &manifest.Manifest{
		Schema: 1, Product: "vaporos", Version: version, RollbackIndex: 1, Channel: "main", MinUpdater: 1,
		Artifacts: map[string]manifest.Artifact{
			"root": {Name: "root.erofs", Size: 1, SHA256: sum64}, "kernel": {Name: "vmlinuz", Size: 1, SHA256: sum64},
			"initrd": {Name: "initramfs.img", Size: 1, SHA256: sum64},
		},
		Extensions: map[string]manifest.Extension{},
	}
	for _, img := range imgs {
		x := img.entry
		m.Extensions[x.ID] = manifest.Extension{Name: manifest.ExtensionFile(x.ID), Size: x.Size, SHA256: x.SHA256,
			FSVerity: x.FSVerity, Core: x.Core, Requires: x.Requires}
	}
	raw, err := json.Marshal(m)
	must(e.t, err)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	writeFile(e.t, filepath.Join(config.KeysDir, "test.pub"), manifest.EncodePublicKey(pub))
	writeFile(e.t, filepath.Join(e.src, "manifest.json"), string(raw))
	writeFile(e.t, filepath.Join(e.src, "manifest.json.sig"), string(manifest.Sign(raw, priv)))
}

func runFetch(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rc := fetchCmd(t.Context(), args, &stdout, &stderr)
	return rc, stdout.String(), stderr.String()
}

func enabledIDs(t *testing.T) []string {
	t.Helper()
	s, err := store.Enabled()
	must(t, err)
	if s == nil {
		return nil
	}
	return s.IDs
}

type fetchFixture struct {
	*env
	proton, truckers, cc image
	target               string
}

func newFetchFixture(t *testing.T) *fetchFixture {
	f := &fetchFixture{env: newEnv(t), target: t.TempDir()}
	f.proton = newImage(t, "proton", "", 5000, true)
	f.truckers = newImage(t, "truckersmp", "", 3000, false, "proton")
	f.cc = newImage(t, "coolercontrol", "", 2000, false)
	f.release(bootedVersion, f.proton, f.truckers, f.cc)
	f.serve(f.proton)
	f.serve(f.truckers) // not coolercontrol
	return f
}

func TestFetchSeedsAnInstall(t *testing.T) {
	f := newFetchFixture(t)
	rc, out, errs := runFetch(t, "--from", f.src, "--version", bootedVersion, "--state-dir", f.target, "--seed")
	if rc != 0 {
		t.Fatalf("exit %d: %s", rc, errs)
	}
	if config.StateDir != f.target {
		t.Fatalf("StateDir = %s", config.StateDir)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != `{"bytes":0,"total":5000}` || lines[len(lines)-1] != `{"bytes":5000,"total":5000}` {
		t.Fatalf("progress %q", lines)
	}
	if got := f.sealer.putIDs(); !slices.Equal(got, []string{"proton"}) {
		t.Fatalf("fetched %v: core only", got)
	}
	sl, err := store.ReadSlot("a")
	must(t, err)
	if sl == nil || sl.Version != bootedVersion || len(sl.Extensions) != 3 {
		t.Fatalf("slot a = %+v", sl)
	}
	if w, err := store.Wanted(); err != nil || len(w) != 0 || !exists(config.ExtWantedPath()) {
		t.Fatalf("wanted %v %v", w, err)
	}
	if got := enabledIDs(t); !slices.Equal(got, []string{"proton"}) {
		t.Fatalf("enabled %v", got)
	}

	// With ids: what they need is fetched and they become wanted.
	rc, out, errs = runFetch(t, "--from", f.src, "--version", bootedVersion, "--state-dir", f.target, "--seed", "truckersmp")
	if rc != 0 {
		t.Fatalf("exit %d: %s", rc, errs)
	}
	if !strings.HasPrefix(out, `{"bytes":0,"total":3000}`) {
		t.Fatalf("progress %q (proton is sealed already)", out)
	}
	if w, _ := store.Wanted(); !slices.Equal(w, []string{"truckersmp"}) {
		t.Fatalf("wanted %v", w)
	}
	if got := enabledIDs(t); !slices.Equal(got, []string{"proton", "truckersmp"}) {
		t.Fatalf("enabled %v", got)
	}
}

func TestFetchSeedsWhatSealed(t *testing.T) {
	f := newFetchFixture(t)
	rc, _, errs := runFetch(t, "--from", f.src, "--version", bootedVersion, "--state-dir", f.target, "--seed", "coolercontrol")
	if rc != 1 || !strings.Contains(errs, "coolercontrol") {
		t.Fatalf("exit %d: %s", rc, errs)
	}
	if w, _ := store.Wanted(); !slices.Equal(w, []string{"coolercontrol"}) {
		t.Fatalf("wanted %v (core is implicit)", w)
	}
	if got := enabledIDs(t); !slices.Equal(got, []string{"proton"}) {
		t.Fatalf("enabled %v: core is always seeded", got)
	}
	if sl, _ := store.ReadSlot("a"); sl == nil {
		t.Fatal("no slot a")
	}
}

func TestFetchRepair(t *testing.T) {
	f := newFetchFixture(t)
	config.StateDir = f.target
	must(t, store.WriteSlot("b", otherVersion, nil))
	locked(t, func() error {
		if err := store.WriteWanted([]string{"truckersmp"}); err != nil {
			return err
		}
		if err := store.AddFailed(strings.Repeat("a", 64)); err != nil {
			return err
		}
		_, err := store.Propose([]string{"proton"}, nil)
		return err
	})
	rc, _, errs := runFetch(t, "--from", f.src, "--version", bootedVersion, "--state-dir", f.target, "--seed", "--repair")
	if rc != 0 {
		t.Fatalf("exit %d: %s", rc, errs)
	}
	if sl, _ := store.ReadSlot("b"); sl != nil {
		t.Fatalf("slot b kept: %+v", sl)
	}
	if exists(config.ExtPendingLink()) || exists(config.ExtFailedPath()) {
		t.Fatal("pending or failed kept")
	}
	if w, _ := store.Wanted(); !slices.Equal(w, []string{"truckersmp"}) {
		t.Fatalf("wanted %v: a repair keeps it", w)
	}
	if got := enabledIDs(t); !slices.Equal(got, []string{"proton", "truckersmp"}) {
		t.Fatalf("enabled %v", got)
	}
}

func TestFetchOnlyTheIDsGiven(t *testing.T) {
	f := newFetchFixture(t)
	rc, _, errs := runFetch(t, "--from", f.src, "--version", bootedVersion, "truckersmp")
	if rc != 0 {
		t.Fatalf("exit %d: %s", rc, errs)
	}
	if got := f.sealer.putIDs(); !slices.Equal(got, []string{"proton", "truckersmp"}) {
		t.Fatalf("fetched %v: the id and its requirements", got)
	}
}

func TestFetchDefaults(t *testing.T) {
	f := newFetchFixture(t)
	c := config.Defaults()
	c.Update.Source = f.src
	must(t, c.Save())
	rc, _, errs := runFetch(t)
	if rc != 0 {
		t.Fatalf("exit %d: %s", rc, errs)
	}
	if got := f.sealer.putIDs(); !slices.Equal(got, []string{"proton"}) {
		t.Fatalf("fetched %v", got)
	}
	if exists(config.ExtWantedPath()) || exists(config.ExtEnabledLink()) {
		t.Fatal("seeded without --seed")
	}
}

func TestFetchRefuses(t *testing.T) {
	f := newFetchFixture(t)
	for _, args := range [][]string{
		{"--repair"},
		{"--seed"},
		{"--from", f.src, "Bad_ID"},
		{"--nope"},
	} {
		if rc, _, _ := runFetch(t, args...); rc != 2 {
			t.Errorf("%q: exit %d, want 2", args, rc)
		}
	}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"other version", []string{"--from", f.src, "--version", otherVersion, "--seed"}, "has version " + bootedVersion},
		{"unknown id", []string{"--from", f.src, "nope"}, "no extension nope"},
		{"no state dir", []string{"--state-dir", filepath.Join(f.target, "missing")}, "not a directory"},
		{"bad source", []string{"--from", "ftp://x"}, "unsupported"},
		{"unsigned", []string{"--from", t.TempDir()}, "manifest.json"},
		{"bad --version", []string{"--from", f.src, "--version", "../x"}, "invalid version"},
		{"no image.json", []string{"--from", f.src}, "no booted image version"}, // last: it removes image.json
	} {
		if c.name == "no image.json" {
			must(t, os.Remove(config.ImageInfoPath))
		}
		rc, _, errs := runFetch(t, c.args...)
		if rc != 1 || !strings.Contains(errs, c.want) {
			t.Errorf("%s: exit %d: %s", c.name, rc, errs)
		}
	}
	if got := f.sealer.putIDs(); len(got) != 0 || exists(filepath.Join(f.target, "ext")) {
		t.Fatalf("fetched %v or wrote the store", got)
	}
}
