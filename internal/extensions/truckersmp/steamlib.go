package truckersmp

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// libraries are the gaming user's Steam libraries. A variable for tests.
var libraries = func() []string { return steam.Libraries(steam.Root(config.GamerHome)) }

// gameLibrary is the library that has g installed (its appmanifest), ""
// when none does.
func gameLibrary(libs []string, g game) string {
	name := "appmanifest_" + strconv.FormatUint(uint64(g.app), 10) + ".acf"
	for _, lib := range libs {
		if fi, err := os.Lstat(filepath.Join(lib, "steamapps", name)); err == nil && fi.Mode().IsRegular() {
			return lib
		}
	}
	return ""
}

// installedGames are the keys of the games some library has.
func installedGames(libs []string) []string {
	var out []string
	for _, g := range games {
		if gameLibrary(libs, g) != "" {
			out = append(out, g.key)
		}
	}
	return out
}

// prefixDocsRel is g's Documents folder in its Proton prefix, relative to
// the library that has it.
func prefixDocsRel(g game) string {
	return filepath.Join("steamapps", "compatdata", strconv.FormatUint(uint64(g.app), 10),
		"pfx", "drive_c", "users", "steamuser", "Documents", g.docs)
}

// prefixUserRel is the prefix's user folder: it exists once Proton made
// the prefix, which the first start of the game does.
func prefixUserRel(g game) string {
	return filepath.Join("steamapps", "compatdata", strconv.FormatUint(uint64(g.app), 10),
		"pfx", "drive_c", "users", "steamuser")
}

// nativeDocsRel is the Linux build's folder, relative to the home.
func nativeDocsRel(g game) string { return filepath.Join(".local", "share", g.docs) }

// maxLogRead is how much of game.log.txt is read: the version is near its
// top, and a game writes the log anew at every start.
const maxLogRead = 1 << 20

// initVerRe finds the version in game.log.txt's start line:
// "00:00:00.000 : Euro Truck Simulator 2 init ver.1.61.1.1s (rev. …) win_x64 …".
var initVerRe = regexp.MustCompile(`init ver\.\s*([0-9]+(?:\.[0-9]+){1,3}[a-z]{0,3})\b`)

// logVersion is the version of the last "init ver." line in r.
func logVersion(r io.Reader) string {
	v := ""
	sc := bufio.NewScanner(io.LimitReader(r, maxLogRead))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		if m := initVerRe.FindStringSubmatch(sc.Text()); m != nil {
			v = m[1]
		}
	}
	return v
}

// installedVersion is the version g last started with, from its log in
// the Proton prefix, else the Linux build's; "" when neither says. vosd
// reads these files, which the gaming user owns, through gamerfs.
func installedVersion(lib string, g game) string {
	var tries [][2]string
	if lib != "" {
		root, rel := libRoot(lib)
		tries = append(tries, [2]string{root, filepath.Join(rel, prefixDocsRel(g), "game.log.txt")})
	}
	tries = append(tries, [2]string{config.GamerHome, filepath.Join(nativeDocsRel(g), "game.log.txt")})
	for _, t := range tries {
		f, err := gamerfs.Open(t[0], t[1])
		if err != nil {
			continue
		}
		v := logVersion(f)
		f.Close()
		if v != "" {
			return v
		}
	}
	return ""
}

// libRoot is the gamerfs root for files in library lib, and lib relative
// to it: the home for a library inside it (the user owns every directory
// on the way), the library itself otherwise (on a disk, whose mount the
// user cannot move).
func libRoot(lib string) (root, rel string) {
	if r, err := filepath.Rel(config.GamerHome, lib); err == nil && filepath.IsLocal(r) {
		return config.GamerHome, r
	}
	return lib, ""
}
