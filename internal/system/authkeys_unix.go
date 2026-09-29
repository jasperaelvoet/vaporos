//go:build unix

package system

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ~vapor is writable by the gaming user, and so by Steam and every game.
// vosd runs as root, so it must never follow a path there by name: a
// symlinked ~/.ssh would otherwise get root to chown or write wherever it
// points. Everything below works on directory file descriptors opened
// with O_NOFOLLOW, and the key file is replaced with renameat.

const (
	sshDirName  = ".ssh"
	keysName    = "authorized_keys"
	tmpKeysName = ".authorized_keys.vos-tmp"
)

// writeAuthorizedKeys replaces home/.ssh/authorized_keys with keys, owned by
// uid:gid, the directory 0700 and the file 0600 as sshd's StrictModes wants.
func writeAuthorizedKeys(home string, uid, gid int, keys []string) error {
	dfd, err := openSSHDir(home, true)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Fchown(dfd, uid, gid); err != nil {
		return fmt.Errorf("chown ~/.ssh: %w", err)
	}
	if err := unix.Fchmod(dfd, 0o700); err != nil {
		return fmt.Errorf("chmod ~/.ssh: %w", err)
	}

	content := ""
	if len(keys) > 0 {
		content = strings.Join(keys, "\n") + "\n"
	}
	if err := unix.Unlinkat(dfd, tmpKeysName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("removing stale %s: %w", tmpKeysName, err)
	}
	fd, err := unix.Openat(dfd, tmpKeysName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmpKeysName, err)
	}
	f := os.NewFile(uintptr(fd), tmpKeysName)
	werr := func() error {
		if _, err := f.WriteString(content); err != nil {
			return err
		}
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
		if err := f.Chmod(0o600); err != nil {
			return err
		}
		return f.Sync()
	}()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = unix.Renameat(dfd, tmpKeysName, dfd, keysName)
	}
	if werr != nil {
		unix.Unlinkat(dfd, tmpKeysName, 0)
		return fmt.Errorf("writing authorized_keys: %w", werr)
	}
	_ = unix.Fsync(dfd)
	return nil
}

// removeAuthorizedKeys deletes home/.ssh/authorized_keys if it exists.
func removeAuthorizedKeys(home string) error {
	dfd, err := openSSHDir(home, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Unlinkat(dfd, keysName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("removing authorized_keys: %w", err)
	}
	return nil
}

// openSSHDir opens home/.ssh without following symlinks. With create, a
// missing directory is made, and a symlink in its place is removed and
// replaced by a real directory.
func openSSHDir(home string, create bool) (int, error) {
	hfd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("opening %s: %w", home, err)
	}
	defer unix.Close(hfd)
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	dir := sshDirName
	for attempt := 0; ; attempt++ {
		dfd, err := unix.Openat(hfd, dir, flags, 0)
		if err == nil {
			return dfd, nil
		}
		if !create || attempt > 0 {
			return -1, wrapSSHDirErr(err)
		}
		switch {
		case errors.Is(err, unix.ENOENT):
		case errors.Is(err, unix.ELOOP) || isSymlinkAt(hfd, dir):
			if err := unix.Unlinkat(hfd, dir, 0); err != nil {
				return -1, fmt.Errorf("removing symlinked ~/.ssh: %w", err)
			}
		default:
			return -1, wrapSSHDirErr(err)
		}
		if err := unix.Mkdirat(hfd, dir, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("creating ~/.ssh: %w", err)
		}
	}
}

func isSymlinkAt(dirfd int, name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK
}

func wrapSSHDirErr(err error) error {
	if errors.Is(err, unix.ENOENT) {
		return err // callers test for it
	}
	if errors.Is(err, unix.ENOTDIR) {
		return errors.New("~/.ssh exists but is not a directory")
	}
	return fmt.Errorf("opening ~/.ssh: %w", err)
}
