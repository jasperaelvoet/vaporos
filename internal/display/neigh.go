package display

// The client's MAC address, from the kernel's neighbour tables (ARP and
// IPv6 neighbour discovery) through netlink RTM_GETNEIGH: a MAC tells
// devices apart that all pair as "roth", and it is the same over IPv4 and
// IPv6 (docs/CONTRACTS.md, Display policy, Scaling, Screens).

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"syscall"
)

// neighbour is one entry of a neighbour table.
type neighbour struct {
	addr  netip.Addr
	mac   net.HardwareAddr
	state uint16
}

// readNeighbours dumps both neighbour tables; tests replace it.
var readNeighbours = dumpNeighbours

// Netlink's numbers (linux/netlink.h, linux/rtnetlink.h,
// linux/neighbour.h), spelled out so the parser builds everywhere.
const (
	nlmsgHdrLen    = 16
	nlmsgError     = 2
	nlmsgDone      = 3
	rtmNewNeigh    = 28
	ndMsgLen       = 12
	ndaDst         = 1
	ndaLLAddr      = 2
	nlaTypeMask    = 0x3fff
	linuxAFInet    = 2
	linuxAFInet6   = 10
	nudReachable   = 0x02
	nudStale       = 0x04
	nudDelay       = 0x08
	nudPermanent   = 0x80
	nudUsable      = nudReachable | nudStale | nudDelay | nudPermanent
	neighDumpLimit = 1 << 24
)

// parseNeighbours reads an RTM_GETNEIGH dump: netlink messages in the
// host's byte order, each an ndmsg and its attributes.
func parseNeighbours(b []byte) ([]neighbour, error) {
	if len(b) > neighDumpLimit {
		return nil, errors.New("neighbour dump too large")
	}
	var out []neighbour
	for len(b) >= nlmsgHdrLen {
		n := int(binary.NativeEndian.Uint32(b[0:4]))
		typ := binary.NativeEndian.Uint16(b[4:6])
		if n < nlmsgHdrLen || n > len(b) {
			return out, errors.New("neighbour dump: bad message length")
		}
		msg := b[nlmsgHdrLen:n]
		switch typ {
		case nlmsgDone:
			return out, nil
		case nlmsgError:
			if len(msg) >= 4 {
				if errno := int32(binary.NativeEndian.Uint32(msg[0:4])); errno < 0 {
					return out, syscall.Errno(-errno)
				}
			}
			return out, errors.New("neighbour dump: netlink error")
		case rtmNewNeigh:
			if nb, ok := parseNdMsg(msg); ok {
				out = append(out, nb)
			}
		}
		b = b[min(align4(n), len(b)):]
	}
	return out, nil
}

func align4(n int) int { return (n + 3) &^ 3 }

func parseNdMsg(m []byte) (neighbour, bool) {
	if len(m) < ndMsgLen {
		return neighbour{}, false
	}
	family := m[0]
	nb := neighbour{state: binary.NativeEndian.Uint16(m[8:10])}
	for a := m[ndMsgLen:]; len(a) >= 4; {
		l := int(binary.NativeEndian.Uint16(a[0:2]))
		typ := binary.NativeEndian.Uint16(a[2:4]) & nlaTypeMask
		if l < 4 || l > len(a) {
			break
		}
		data := a[4:l]
		switch typ {
		case ndaDst:
			if (family == linuxAFInet && len(data) == 4) || (family == linuxAFInet6 && len(data) == 16) {
				nb.addr, _ = netip.AddrFromSlice(data)
			}
		case ndaLLAddr:
			nb.mac = net.HardwareAddr(append([]byte(nil), data...))
		}
		a = a[min(align4(l), len(a)):]
	}
	return nb, nb.addr.IsValid()
}

// NeighbourMAC is the MAC address the kernel's neighbour tables have for
// addr ("" when none): only reachable, stale, delay or permanent entries
// with a non-zero Ethernet address count.
func NeighbourMAC(addr netip.Addr) string {
	addr = addr.WithZone("").Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified() {
		return ""
	}
	nbs, err := readNeighbours()
	if err != nil {
		return ""
	}
	for _, nb := range nbs {
		if nb.addr.Unmap() != addr || nb.state&nudUsable == 0 {
			continue
		}
		if m := normMAC(nb.mac.String()); m != "" {
			return m
		}
	}
	return ""
}
