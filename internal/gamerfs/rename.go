package gamerfs

import (
	"errors"
	"io/fs"
	"strings"
)

// Rename gives the entry rel beneath root the name name in the same
// directory, as rename(2) does: a symlink there is renamed itself, never
// followed, and a directory moves with all it holds. name is one
// component; an entry already called name is replaced only when rename(2)
// replaces it (a file, or an empty directory for a directory).
func Rename(root, rel, name string) error {
	parts, err := split(root, rel, false)
	if err != nil {
		return err
	}
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return &fs.PathError{Op: "rename", Path: join(root, parts), Err: errors.New("the new name is not one component")}
	}
	return rename(root, parts, name)
}
