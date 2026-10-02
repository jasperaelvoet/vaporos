package steam

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Root returns the Steam installation directory in home. Steam keeps a
// ~/.steam/root symlink to wherever it actually lives; without one (Steam
// never started) the default ~/.local/share/Steam is assumed.
func Root(home string) string {
	if p, err := filepath.EvalSymlinks(filepath.Join(home, ".steam", "root")); err == nil {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return filepath.Join(home, ".local", "share", "Steam")
}

// Libraries returns every Steam library root known to the Steam install at
// root: root itself first, then each "path" in libraryfolders.vdf. Steam
// keeps that file in steamapps/ (and a copy in config/); both are read, as
// either can be the newer one right after a library is added. Duplicates,
// including the same directory reached through a symlink such as
// /mnt -> /var/mnt, are removed. A file that cannot be read or parsed
// adds nothing; ReadLibraries says when that happened.
func Libraries(root string) []string {
	libs, _ := ReadLibraries(root)
	return libs
}

// ReadLibraries is Libraries that also says when the list may lack a
// library: a libraryfolders.vdf that is there but cannot be read or parsed
// (damaged, or caught mid-write) may list more than the libraries
// returned. A missing one is no error.
func ReadLibraries(root string) ([]string, error) {
	libs := []string{root}
	var errs []error
	for _, f := range []string{
		filepath.Join(root, "steamapps", "libraryfolders.vdf"),
		filepath.Join(root, "config", "libraryfolders.vdf"),
	} {
		data, err := ReadFileLimited(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err) // it names the file
			continue
		}
		paths, err := ParseLibraryFolders(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		libs = append(libs, paths...)
	}
	return dedupePaths(libs), errors.Join(errs...)
}

// ParseLibraryFolders returns the library paths in a libraryfolders.vdf.
// It understands the current format ("libraryfolders" { "0" { "path" … } })
// and the pre-2021 one ("LibraryFolders" { "1" "/path" }).
func ParseLibraryFolders(data []byte) ([]string, error) {
	root, err := ParseVDF(data)
	if err != nil {
		return nil, err
	}
	top := root.Child("libraryfolders")
	if top == nil || !top.Block {
		return nil, errors.New("libraryfolders.vdf: no libraryfolders block")
	}
	var out []string
	for _, c := range top.Children {
		if _, err := strconv.Atoi(c.Key); err != nil {
			continue // TimeNextStatsReport, ContentStatsID, …
		}
		p := c.Value
		if c.Block {
			p = c.Str("path")
		}
		if p = strings.TrimSpace(p); filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
		}
	}
	return out, nil
}

// App is one installed (or installing) game or tool from an appmanifest.
type App struct {
	ID         int
	Name       string
	InstallDir string
	StateFlags int
	Library    string // library root the manifest was found in
}

// stateFullyInstalled is StateFlags bit 2 (value 4): the app's files are
// complete. Updating apps keep it set; a first download does not.
const stateFullyInstalled = 4

// Installed reports whether the app can be launched.
func (a App) Installed() bool { return a.StateFlags&stateFullyInstalled != 0 }

// IsTool reports whether the app is Steam plumbing rather than a game:
// Proton, the Steam Linux Runtime containers and the common redistributables.
// They have appmanifests like games, but launching them does nothing useful.
// The ids catch them before they are installed; the names, versions
// this list does not know yet.
func (a App) IsTool() bool {
	switch a.ID {
	case 228980, // Steamworks Common Redistributables
		1070560, 1391110, 1628350, 4183110, // Steam Linux Runtime 1.0, 2.0, 3.0, 4.0
		1493710, 2180100, 3658110, 4628710, // Proton Experimental, Hotfix, 10.0, 11.0
		2805730, 2348590, 1887720, 1580130, 1420170, // Proton 9.0, 8.0, 7.0, 6.3, 5.13
		1245040, 1113280, 1054830, 961940, 858280, // Proton 5.0, 4.11, 4.2, 3.16, 3.7
		1826330, 1161040: // Proton EasyAntiCheat and BattlEye runtimes
		return true
	}
	n := strings.ToLower(a.Name)
	return strings.HasPrefix(n, "proton ") || n == "proton" ||
		strings.HasPrefix(n, "steam linux runtime") ||
		strings.HasPrefix(n, "steamworks common redistributables")
}

// ParseManifest parses one appmanifest_<id>.acf.
func ParseManifest(data []byte) (App, error) {
	root, err := ParseVDF(data)
	if err != nil {
		return App{}, err
	}
	st := root.Child("AppState")
	if st == nil || !st.Block {
		return App{}, errors.New("appmanifest: no AppState block")
	}
	id, err := strconv.Atoi(st.Str("appid"))
	if err != nil || id <= 0 {
		return App{}, fmt.Errorf("appmanifest: bad appid %q", st.Str("appid"))
	}
	flags, _ := strconv.Atoi(st.Str("StateFlags"))
	return App{
		ID:         id,
		Name:       strings.TrimSpace(st.Str("name")),
		InstallDir: st.Str("installdir"),
		StateFlags: flags,
	}, nil
}

// InstalledGames returns the launchable games across libs, one entry per
// app id, sorted by name. Unreadable libraries and manifests are skipped:
// an unplugged disk must not hide the games on the others.
func InstalledGames(libs []string) []App {
	seen := map[int]bool{}
	var out []App
	for _, lib := range libs {
		matches, _ := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
		sort.Strings(matches)
		for _, m := range matches {
			data, err := ReadFileLimited(m)
			if err != nil {
				continue
			}
			app, err := ParseManifest(data)
			if err != nil || app.Name == "" || !app.Installed() || app.IsTool() || seen[app.ID] {
				continue
			}
			seen[app.ID] = true
			app.Library = lib
			out = append(out, app)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// LibraryIn reports whether the filesystem mounted at dir holds a Steam
// library and returns its directory relative to dir: "." when the
// filesystem root is the library (Steam's own layout for a whole disk:
// steamapps/ and libraryfolder.vdf at the top) or "SteamLibrary" (the name
// Steam proposes when adding a folder, common on disks shared with Windows).
func LibraryIn(dir string) (string, bool) {
	if isLibraryDir(dir) {
		return ".", true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), "SteamLibrary") && isLibraryDir(filepath.Join(dir, e.Name())) {
			return e.Name(), true
		}
	}
	return "", false
}

// isLibraryDir reports whether dir itself is a library root: it has a
// steamapps directory (any case; old Windows installs wrote SteamApps) or
// the libraryfolder.vdf marker Steam writes into every library.
func isLibraryDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		switch {
		case strings.EqualFold(e.Name(), "steamapps") && e.IsDir():
			return true
		case e.Name() == "libraryfolder.vdf" && e.Type().IsRegular():
			return true
		}
	}
	return false
}

// ReadFileLimited reads a small Steam metadata file, refusing anything
// larger than a real one could be.
func ReadFileLimited(path string) ([]byte, error) {
	return ReadFileMax(path, maxVDFSize)
}

// ReadFileMax is ReadFileLimited with its own cap, for the files that
// grow larger (LocalConfigMax).
func ReadFileMax(path string, limit int) ([]byte, error) {
	// Steam's files belong to the gaming user and vosd reads them as root:
	// never follow a final symlink, never block on a FIFO, and read only
	// regular files.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s: too large", path)
	}
	return data, nil
}

// dedupePaths keeps the first occurrence of each directory, comparing
// resolved paths when the directory exists.
func dedupePaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		key := filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(key); err == nil {
			key = r
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, filepath.Clean(p))
	}
	return out
}
