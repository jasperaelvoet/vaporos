package sunshine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
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

// vosd runs as root but writes into the gaming user's home, where Steam
// and every game run as that user. So nothing here follows a symlink the
// user could have planted: a directory component that is a symlink is an
// error, and files are replaced by rename (which replaces a link, never
// its target) or adjusted through an O_NOFOLLOW descriptor.

// ensureGamerDir creates dir and any missing parents below the gaming
// user's home, owned by that user. Directories that already exist keep
// their owner and mode.
func ensureGamerDir(dir string) error {
	home := filepath.Clean(config.GamerHome)
	rel, err := filepath.Rel(home, filepath.Clean(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", dir, home)
	}
	cur := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.IsDir():
			continue
		case err == nil:
			return fmt.Errorf("%s is not a directory", cur)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if err := os.Mkdir(cur, 0o700); err != nil {
			return err
		}
		if runningAsRoot() {
			uid, gid := gamerOwner()
			if err := os.Lchown(cur, uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}

// readRegular reads path if it is a regular file (not through a symlink).
func readRegular(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return io.ReadAll(io.LimitReader(f, 16<<20))
}

// writeGamerFile replaces path with data, owned by the gaming user with
// mode perm, and reports whether the content changed. An identical file is
// not rewritten (Sunshine is only restarted for real changes), but its
// mode and owner are still corrected.
//
// The temporary file gets its final owner and mode before the rename, so
// Sunshine never sees a half-written or root-owned file.
func writeGamerFile(path string, data []byte, perm os.FileMode) (bool, error) {
	dir := filepath.Dir(path)
	if err := ensureGamerDir(dir); err != nil {
		return false, err
	}
	if old, err := readRegular(path); err == nil && bytes.Equal(old, data) {
		return false, fixOwnership(path, perm)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".vos-*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil && runningAsRoot() {
		uid, gid := gamerOwner()
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return true, nil
}

func fixOwnership(path string, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Mode().Perm() != perm {
		if err := f.Chmod(perm); err != nil {
			return err
		}
	}
	if runningAsRoot() {
		uid, gid := gamerOwner()
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid || int(st.Gid) != gid {
			return f.Chown(uid, gid)
		}
	}
	return nil
}
