package sunshine

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// Sunshine keeps everything in its appdata directory, ~/.config/sunshine
// of the user it runs as (vapor).
func sunshineDir() string    { return filepath.Join(config.GamerHome, ".config", "sunshine") }
func confPath() string       { return filepath.Join(sunshineDir(), "sunshine.conf") }
func appsPath() string       { return filepath.Join(sunshineDir(), "apps.json") }
func statePath() string      { return filepath.Join(sunshineDir(), "sunshine_state.json") }
func certPath() string       { return filepath.Join(sunshineDir(), "credentials", "cacert.pem") }
func logPath() string        { return filepath.Join(sunshineDir(), "sunshine.log") }
func templatePath() string   { return filepath.Join(config.ShareDir, "sunshine.conf.tmpl") }
func runningAsRoot() bool    { return os.Geteuid() == 0 }
func gamerOwner() (int, int) { return config.GamerUID, config.GamerUID }

// maxFileSize caps what vosd reads from Sunshine's files, which the
// gaming user can grow at will.
const maxFileSize = 16 << 20

// vosd runs as root but reads and writes in the gaming user's home, where
// Steam and every game run as that user and can plant symlinks or swap a
// directory for one at any moment. So every path there goes through
// gamerfs, which resolves it beneath the home without following a symlink
// and never looks a checked directory up by name again.

// gamerRel returns path relative to the gaming user's home.
func gamerRel(path string) (string, error) {
	home := filepath.Clean(config.GamerHome)
	rel, err := filepath.Rel(home, filepath.Clean(path))
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not below %s", path, home)
	}
	return rel, nil
}

// readRegular reads a regular file of at most maxFileSize bytes without
// following a symlink. A path in the gaming user's home is resolved
// beneath the home, so no component of it may be a symlink; any other path
// (the image's pacman database) is trusted up to its directory.
func readRegular(path string) ([]byte, error) {
	if rel, err := gamerRel(path); err == nil {
		return gamerfs.ReadFile(config.GamerHome, rel, maxFileSize)
	}
	return gamerfs.ReadFile(filepath.Dir(path), filepath.Base(path), maxFileSize)
}

// writeGamerFile replaces path, below the gaming user's home, with data,
// owned by that user with mode perm, and reports whether the content
// changed. An identical file is not rewritten (Sunshine is only restarted
// for real changes), but its mode and owner are still corrected. Missing
// directories are created 0700 and owned by the user; existing ones keep
// their owner and mode. The new file gets its owner and mode before it is
// renamed into place, so Sunshine never sees a half-written or root-owned
// file.
func writeGamerFile(path string, data []byte, perm os.FileMode) (bool, error) {
	rel, err := gamerRel(path)
	if err != nil {
		return false, err
	}
	uid, gid := -1, -1
	if runningAsRoot() {
		uid, gid = gamerOwner()
	}
	if same, err := keepIfSame(rel, data, perm, uid, gid); same || err != nil {
		return false, err
	}
	if err := gamerfs.WriteFile(config.GamerHome, rel, data, perm, uid, gid); err != nil {
		return false, err
	}
	return true, nil
}

// keepIfSame reports whether rel already is a regular file holding exactly
// data, and then gives it mode perm and owner uid:gid (-1: unchanged)
// through the descriptor that was compared. A file with more than one link
// counts as different: renaming a new file over it leaves the other name
// alone, where chown or chmod would change a file outside this tree.
// Anything that cannot be opened safely also counts as different, and the
// caller's rewrite reports the error.
func keepIfSame(rel string, data []byte, perm os.FileMode, uid, gid int) (bool, error) {
	f, err := gamerfs.Open(config.GamerHome, rel)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() != int64(len(data)) {
		return false, nil
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return false, nil
	}
	old, err := io.ReadAll(io.LimitReader(f, int64(len(data))+1))
	if err != nil || !bytes.Equal(old, data) {
		return false, nil
	}
	// chown before chmod: chown clears set-id bits.
	if uid >= 0 && (int(st.Uid) != uid || int(st.Gid) != gid) {
		if err := f.Chown(uid, gid); err != nil {
			return true, err
		}
	}
	if fi.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != perm {
		if err := f.Chmod(perm); err != nil {
			return true, err
		}
	}
	return true, nil
}
