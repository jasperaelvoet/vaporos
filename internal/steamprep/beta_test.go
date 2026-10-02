package steamprep

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

func (b *box) betaKey() string {
	b.t.Helper()
	k, _, err := steam.BetaKey(b.steamFile("steamapps/appmanifest_227300.acf"))
	b.check(err)
	return k
}

func withBeta(d Desired, app uint32, beta string) Desired {
	for i := range d.Apps {
		if d.Apps[i].App == app {
			d.Apps[i].Beta = &beta
		}
	}
	return d
}

func TestBranches(t *testing.T) {
	b := newBox(t)
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61"))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_61" {
		t.Fatalf("BetaKey %q", k)
	}
	if bs := b.state().peekApp(ets2).Beta; bs == nil || *bs != (BetaState{Wrote: "temporary_1_61", Before: "temporary_1_53"}) {
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

	// The public branch is a branch too; one the user changed meanwhile
	// stays theirs.
	b.desire(withBeta(truckers(proton()), ets2, ""))
	b.run(false)
	if k := b.betaKey(); k != "" {
		t.Errorf("public: %q", k)
	}
	b.edit("steamapps/appmanifest_227300.acf", func(d []byte) []byte {
		out, _, err := steam.SetBetaKey(d, "temporary_1_58")
		b.check(err)
		return out
	})
	b.desire(truckers(proton()))
	b.run(false)
	if k := b.betaKey(); k != "temporary_1_58" {
		t.Errorf("user's branch: %q", k)
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
	b.desire(withBeta(truckers(proton()), ets2, "temporary_1_61"))
	b.run(false)
	got, err := readRegular(filepath.Join(lib, "steamapps", "appmanifest_227300.acf"), steam.VDFMax)
	b.check(err)
	if k, _, _ := steam.BetaKey(got); k != "temporary_1_61" {
		t.Errorf("BetaKey %q", k)
	}
}
