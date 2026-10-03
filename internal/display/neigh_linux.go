//go:build linux

package display

import "syscall"

func dumpNeighbours() ([]neighbour, error) {
	b, err := syscall.NetlinkRIB(syscall.RTM_GETNEIGH, syscall.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	return parseNeighbours(b)
}
