//go:build !linux

package display

import "errors"

// Only Linux has netlink; elsewhere (go vet and tests on a Mac) no MAC is
// known.
func dumpNeighbours() ([]neighbour, error) {
	return nil, errors.New("no neighbour table on this system")
}
