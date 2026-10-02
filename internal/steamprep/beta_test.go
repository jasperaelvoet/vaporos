package steamprep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

func (b *box) betaKey() string {
	b.t.Helper()
	k, _, err := steam.BetaKey(b.steamFile("steamapps/appmanifest_227300.acf"))
	b.check(err)
	return k
}

func (b *box) setBetaKey(branch string) {
	b.edit("steamapps/appmanifest_227300.acf", func(d []byte) []byte {
		out, _, err := steam.SetBetaKey(d, branch)
		b.check(err)
		return out
	})
}

// withBeta asks for app's branch, as request id.
func withBeta(d Desired, app uint32, branch, id string) Desired {
	for i := range d.Apps {
		if d.Apps[i].App == app {
			d.Apps[i].Beta = &BetaWant{Branch: branch, Request: id}
		}
	}
	return d
}

func TestBranches(t *testing.T) {
	b := newBox(t)
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61", "r1"))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_61" {
		t.Fatalf("BetaKey %q", k)
	}
	if bs := b.state().peekApp(ets2).Beta; bs == nil || *bs != (BetaState{Wrote: "temporary_1_61", Before: "temporary_1_53", Request: "r1"}) {
		t.Fatalf("record %+v", bs)
	}
	// Not installed: left for later, without a record.
	if a := b.state().peekApp(ats); a != nil && a.Beta != nil {
		t.Errorf("ATS %+v", a.Beta)
	}

	// No longer asked for: the branch from before.
	b.desire(truckers(proton()))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_53" {
		t.Errorf("BetaKey %q", k)
	}
	if a := b.state().peekApp(ets2); a.Beta != nil {
		t.Errorf("record %+v", a.Beta)
	}

	// The public branch is a branch too. A request is applied once: the
	// branch the user picks in Steam afterwards stays theirs, while the
	// request is still there and after it goes.
	b.desire(withBeta(truckers(proton()), ets2, "", "r2"))
	b.run(false)
	if k := b.betaKey(); k != "" {
		t.Errorf("public: %q", k)
	}
	b.setBetaKey("temporary_1_58")
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_58" {
		t.Errorf("request applied again: %q", k)
	}
	b.desire(truckers(proton()))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_58" {
		t.Errorf("user's branch: %q", k)
	}

	// A new request is applied, over the user's branch.
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61", "r3"))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_61" {
		t.Errorf("new request: %q", k)
	}
	if bs := b.state().peekApp(ets2).Beta; bs.Before != "temporary_1_58" {
		t.Errorf("record %+v", bs)
	}
}

// An app that is uninstalled loses its branch record, so a reinstall
// gets the request that still stands; while a library's drive is away
// the app may still be there, and the record stays.
func TestBranchAfterAReinstall(t *testing.T) {
	b := newBox(t)
	manifest := filepath.Join(b.root, "steamapps", "appmanifest_227300.acf")
	installed := b.steamFile("steamapps/appmanifest_227300.acf")
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61", "r1"))
	b.run(false)
	b.setBetaKey("temporary_1_58") // the user's pick stays theirs
	b.run(false)

	lib := filepath.Join(b.dir, "var", "mnt", "games", "SteamLibrary")
	b.write(filepath.Join(b.root, "steamapps", "libraryfolders.vdf"),
		[]byte("\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\""+b.root+"\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\""+lib+"\"\n\t}\n}\n"))
	b.check(os.Remove(manifest))
	b.run(false)
	if bs := b.state().peekApp(ets2).Beta; bs == nil || bs.Request != "r1" {
		t.Fatalf("record dropped while a library is away: %+v", bs)
	}

	b.mkdir(filepath.Join(lib, "steamapps"))
	b.run(false)
	if a := b.state().peekApp(ets2); a.Beta != nil {
		t.Fatalf("uninstalled, record %+v", a.Beta)
	}
	b.write(manifest, installed)
	b.setBetaKey("")
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_61" {
		t.Errorf("reinstalled: BetaKey %q", k)
	}
	if bs := b.state().peekApp(ets2).Beta; bs == nil || *bs != (BetaState{Wrote: "temporary_1_61", Request: "r1"}) {
		t.Errorf("record %+v", bs)
	}
}

// A libraryfolders.vdf that does not parse may hide the library an app is
// in: the app is not taken for uninstalled, so its record stays and the
// branch the user picked is not taken away once the file reads again.
func TestBranchKeptWhileTheLibraryListIsDamaged(t *testing.T) {
	b := newBox(t)
	lib := filepath.Join(b.dir, "var", "mnt", "games", "SteamLibrary")
	manifest := filepath.Join(lib, "steamapps", "appmanifest_227300.acf")
	b.write(manifest, b.steamFile("steamapps/appmanifest_227300.acf"))
	b.check(os.Remove(filepath.Join(b.root, "steamapps", "appmanifest_227300.acf")))
	vdf := filepath.Join(b.root, "steamapps", "libraryfolders.vdf")
	folders := []byte("\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\"" + b.root + "\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\"" + lib + "\"\n\t}\n}\n")
	b.write(vdf, folders)
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61", "r1"))
	b.run(false)
	branch := func() string {
		data, err := readRegular(manifest, steam.VDFMax)
		b.check(err)
		k, _, err := steam.BetaKey(data)
		b.check(err)
		return k
	}
	if k := branch(); k != "temporary_1_61" {
		t.Fatalf("BetaKey %q", k)
	}
	data, err := readRegular(manifest, steam.VDFMax)
	b.check(err)
	picked, _, err := steam.SetBetaKey(data, "temporary_1_58")
	b.check(err)
	b.write(manifest, picked)

	b.write(vdf, folders[:len(folders)-12])
	b.run(false)
	st := b.state()
	if a := st.peekApp(ets2); a == nil || a.Beta == nil || a.Beta.Request != "r1" {
		t.Fatalf("record dropped while the library list is damaged: %+v\n%s", a, b.logs.String())
	}
	if !strings.Contains(st.Error, "libraryfolders.vdf") || st.Fingerprint != "" {
		t.Errorf("error %q, fingerprint %q", st.Error, st.Fingerprint)
	}

	b.write(vdf, folders)
	b.run(false)
	if k := branch(); k != "temporary_1_58" {
		t.Errorf("the user's branch was taken away: %q", k)
	}
	if st := b.state(); st.Error != "" || st.peekApp(ets2).Beta.Request != "r1" {
		t.Errorf("record %+v, error %q", st.peekApp(ets2).Beta, st.Error)
	}
}

func TestBranchAlreadySoIsAdopted(t *testing.T) {
	b := newBox(t)
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_53", "r1"))
	b.run(false)
	if bs := b.state().peekApp(ets2).Beta; bs == nil || *bs != (BetaState{Wrote: "temporary_1_53", Request: "r1"}) {
		t.Fatalf("record %+v", bs)
	}
	b.desire(truckers(proton()))
	b.run(false)
	if k := b.betaKey(); k != "" {
		t.Errorf("not back to the public branch: %q", k)
	}
}

func TestMalformedBranchLeftAlone(t *testing.T) {
	b := newBox(t)
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61", "r1"))
	b.run(false)
	// The branch as an old vosd wrote it: the rest of the app still
	// applies, and the branch and its record stay as they are.
	data, err := os.ReadFile(config.ExtSteamPath())
	b.check(err)
	old := strings.Replace(string(data), `{"branch":"temporary_1_61","request":"r1"}`, `"temporary_1_58"`, 1)
	if old == string(data) {
		t.Fatalf("steam.json: %s", data)
	}
	b.write(config.ExtSteamPath(), []byte(old))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_61" {
		t.Errorf("BetaKey %q", k)
	}
	if bs := b.state().peekApp(ets2).Beta; bs == nil || bs.Request != "r1" {
		t.Errorf("record %+v", bs)
	}
	if o, _ := b.launchOptions(acctA, ets2); !strings.HasPrefix(o, tokenETS2) {
		t.Errorf("launch options %q", o)
	}
}

func TestBranchInAnotherLibrary(t *testing.T) {
	b := newBox(t)
	lib := filepath.Join(b.dir, "var", "mnt", "games", "SteamLibrary")
	data := b.steamFile("steamapps/appmanifest_227300.acf")
	b.write(filepath.Join(lib, "steamapps", "appmanifest_227300.acf"), data)
	b.check(os.Remove(filepath.Join(b.root, "steamapps", "appmanifest_227300.acf")))
	b.write(filepath.Join(b.root, "steamapps", "libraryfolders.vdf"),
		[]byte("\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\""+b.root+"\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\""+lib+"\"\n\t}\n}\n"))
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61", "r1"))
	b.run(false)
	got, err := readRegular(filepath.Join(lib, "steamapps", "appmanifest_227300.acf"), steam.VDFMax)
	b.check(err)
	if k, _, _ := steam.BetaKey(got); k != "temporary_1_61" {
		t.Errorf("BetaKey %q", k)
	}
}

func TestDecideBeta(t *testing.T) {
	want := &BetaWant{Branch: "b2", Request: "r2"}
	for _, c := range []struct {
		name   string
		cur    string
		b      *BetaState
		want   *BetaWant
		branch string
		next   *BetaState
	}{
		{"apply", "b0", nil, want, "b2", &BetaState{Wrote: "b2", Before: "b0", Request: "r2"}},
		{"adopt", "b2", nil, want, "b2", &BetaState{Wrote: "b2", Request: "r2"}},
		{"applied once", "b9", &BetaState{Wrote: "b2", Before: "b0", Request: "r2"}, want, "b9",
			&BetaState{Wrote: "b2", Before: "b0", Request: "r2"}},
		{"new request, ours there", "b1", &BetaState{Wrote: "b1", Before: "b0", Request: "r1"}, want, "b2",
			&BetaState{Wrote: "b2", Before: "b0", Request: "r2"}},
		{"new request, user's there", "b9", &BetaState{Wrote: "b1", Before: "b0", Request: "r1"}, want, "b2",
			&BetaState{Wrote: "b2", Before: "b9", Request: "r2"}},
		{"withdrawn", "b2", &BetaState{Wrote: "b2", Before: "b0", Request: "r2"}, nil, "b0", nil},
		{"withdrawn, user's", "b9", &BetaState{Wrote: "b2", Before: "b0", Request: "r2"}, nil, "b9", nil},
	} {
		branch, next := decideBeta(c.cur, c.b, c.want)
		if branch != c.branch || (next == nil) != (c.next == nil) || next != nil && *next != *c.next {
			t.Errorf("%s: %q %+v", c.name, branch, next)
		}
	}
}
