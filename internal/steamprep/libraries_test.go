package steamprep

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

const libList = "\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\"/var/home/vapor/.local/share/Steam\"\n\t\t\"label\"\t\t\"\"\n\t\t\"contentid\"\t\t\"1\"\n\t}\n}\n"

func (b *box) listedLibraries(rel string) []string {
	b.t.Helper()
	paths, err := steam.ParseLibraryFolders(b.steamFile(rel))
	b.check(err)
	return paths
}

// A library vosd queued while Steam ran is added by the next prepare,
// before Steam starts, with steam.json or without; one whose disk is away
// and one Steam lists already are left as they are.
func TestAddsQueuedLibraries(t *testing.T) {
	for name, desired := range map[string]bool{"with steam.json": true, "without": false} {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			if desired {
				b.desire(proton())
			}
			b.write(filepath.Join(b.root, "steamapps", "libraryfolders.vdf"), []byte(libList))
			b.write(filepath.Join(b.root, "config", "libraryfolders.vdf"), []byte(libList))
			big := filepath.Join(b.dir, "var", "mnt", "SATA1TB")
			b.write(filepath.Join(big, "libraryfolder.vdf"), []byte("\"libraryfolder\"\n{\n\t\"contentid\"\t\t\"77\"\n\t\"label\"\t\t\"Big\"\n}\n"))
			b.mkdir(filepath.Join(big, "steamapps"))
			away := filepath.Join(b.dir, "var", "mnt", "USB")
			b.mkdir(away) // its mountpoint, nothing mounted
			b.write(filepath.Join(config.StateDir, "steam-libraries.json"),
				[]byte(`{"pending":["`+big+`","`+away+`"],"seeded":["x"]}`))
			b.run(false)

			for _, rel := range []string{"steamapps/libraryfolders.vdf", "config/libraryfolders.vdf"} {
				got := b.listedLibraries(rel)
				if !slices.Contains(got, big) || slices.Contains(got, away) || len(got) != 2 {
					t.Errorf("%s lists %q\n%s", rel, got, b.logs.String())
				}
			}
			before := b.steamFile("steamapps/libraryfolders.vdf")
			b.run(false)
			if got := b.steamFile("steamapps/libraryfolders.vdf"); string(got) != string(before) {
				t.Errorf("second run changed the list:\n%s", got)
			}
		})
	}
}

// Without Steam's list (it never ran), nothing is made.
func TestQueuedLibraryWithoutSteamList(t *testing.T) {
	b := newBox(t)
	b.desire(proton())
	dir := filepath.Join(b.dir, "var", "mnt", "games")
	b.mkdir(filepath.Join(dir, "steamapps"))
	b.write(filepath.Join(config.StateDir, "steam-libraries.json"), []byte(`{"pending":["`+dir+`"]}`))
	b.run(false)
	if _, err := os.Stat(filepath.Join(b.root, "steamapps", "libraryfolders.vdf")); !os.IsNotExist(err) {
		t.Errorf("libraryfolders.vdf made: %v", err)
	}
}
