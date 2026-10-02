package update

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// lanState is what the LAN check of `vos health` sees.
type lanState int

const (
	lanNoCarrier lanState = iota // no network hardware has a link
	lanNoAddress                 // a link, but no usable address on it
	lanUp                        // a link with a usable address
)

var (
	// NetClassDir lists the network interfaces.
	NetClassDir = "/sys/class/net"
	// interfaceAddrs returns an interface's addresses; tests fake it.
	interfaceAddrs = func(name string) ([]net.Addr, error) {
		ifc, err := net.InterfaceByName(name)
		if err != nil {
			return nil, err
		}
		return ifc.Addrs()
	}
)

// probeLAN looks at the network hardware (interfaces with a device link,
// so not lo, bridges, veth or tunnels): up when one with carrier has an
// IPv4 address that is neither loopback nor link-local, or a global or
// unique local IPv6 one.
func probeLAN() lanState {
	ents, _ := os.ReadDir(NetClassDir)
	st := lanNoCarrier
	for _, e := range ents {
		name := e.Name()
		dir := filepath.Join(NetClassDir, name)
		if name == "lo" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "device")); err != nil {
			continue
		}
		// Reading carrier fails (EINVAL) on an interface that is down.
		if b, err := os.ReadFile(filepath.Join(dir, "carrier")); err != nil || strings.TrimSpace(string(b)) != "1" {
			continue
		}
		st = lanNoAddress
		addrs, _ := interfaceAddrs(name)
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
				return lanUp
			}
		}
	}
	return st
}
