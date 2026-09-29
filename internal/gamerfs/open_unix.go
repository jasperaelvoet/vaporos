//go:build unix && !linux

package gamerfs

// openBeneath opens the file parts beneath rootfd; without openat2 that is
// the openat walk. It takes over rootfd.
func openBeneath(rootfd int, root string, parts []string) (int, error) {
	return openWalk(rootfd, root, parts)
}
