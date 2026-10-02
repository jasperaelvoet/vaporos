package gamerfs

import "os"

// ReadDirNames returns up to max names (all of them when max <= 0) in the
// directory rel beneath root, in no particular order. An empty rel or "."
// lists root itself.
func ReadDirNames(root, rel string, max int) ([]string, error) {
	parts, err := split(root, rel, true)
	if err != nil {
		return nil, err
	}
	return readDirNames(root, parts, max)
}

// Remove removes the file rel beneath root. A symlink there is removed
// itself, never followed; a directory is an error.
func Remove(root, rel string) error {
	parts, err := split(root, rel, false)
	if err != nil {
		return err
	}
	return remove(root, parts)
}

// OpenOrCreate opens the regular file rel beneath root for reading, first
// creating it empty, owned by uid:gid (-1 leaves that part as the caller's)
// with mode perm, when it is missing. Its directory must exist. It is how
// a lock file in the user's tree is opened: the caller flocks the
// descriptor, which is the file that was checked.
func OpenOrCreate(root, rel string, perm os.FileMode, uid, gid int) (*os.File, error) {
	parts, err := split(root, rel, false)
	if err != nil {
		return nil, err
	}
	return openOrCreate(root, parts, perm.Perm(), uid, gid)
}
