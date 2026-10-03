package display

// Telling which device runs a session (docs/CONTRACTS.md, Display policy,
// Scaling, Screens): Sunshine runs `vos session begin` inside Moonlight's
// /launch request, so while begin runs the client holds a connection to
// Sunshine's HTTPS port, which the kernel's socket tables show.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sunshine's ports (its defaults, which nftables.nft opens).
const (
	SunshineHTTPSPort = 47984 // pairing, serverinfo, /launch
	SunshineRTSPPort  = 48010 // the stream's setup, right after /launch
)

// tcpEstablished is ESTABLISHED as /proc/net/tcp prints the state.
const tcpEstablished = 0x01

// procTCPConn is one line of /proc/net/tcp or /proc/net/tcp6.
type procTCPConn struct {
	local, remote netip.AddrPort
	state         uint8
}

// parseProcNetTCP reads /proc/net/tcp or /proc/net/tcp6. Addresses are
// printed as 32-bit words in the kernel's (on x86, little-endian) byte
// order, four of them for IPv6; ports in plain hex. Lines it cannot read
// are skipped.
func parseProcNetTCP(data []byte) []procTCPConn {
	var out []procTCPConn
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), 1<<16)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasSuffix(f[0], ":") {
			continue // the header, or a line cut short
		}
		local, ok1 := parseProcAddrPort(f[1])
		remote, ok2 := parseProcAddrPort(f[2])
		state, err := strconv.ParseUint(f[3], 16, 8)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		out = append(out, procTCPConn{local: local, remote: remote, state: uint8(state)})
	}
	return out
}

func parseProcAddrPort(s string) (netip.AddrPort, bool) {
	hexAddr, hexPort, ok := strings.Cut(s, ":")
	if !ok || (len(hexAddr) != 8 && len(hexAddr) != 32) {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	b := make([]byte, len(hexAddr)/2)
	for i := 0; i < len(hexAddr); i += 8 {
		w, err := strconv.ParseUint(hexAddr[i:i+8], 16, 32)
		if err != nil {
			return netip.AddrPort{}, false
		}
		binary.NativeEndian.PutUint32(b[i/2:], uint32(w))
	}
	a, _ := netip.AddrFromSlice(b)
	return netip.AddrPortFrom(a, uint16(port)), true
}

// readProcTCP reads both socket tables under ProcDir; a missing tcp6 (no
// IPv6) is no error.
func readProcTCP(procDir string) ([]procTCPConn, error) {
	var out []procTCPConn
	for _, name := range []string{"tcp", "tcp6"} {
		b, err := os.ReadFile(filepath.Join(procDir, "net", name))
		if err != nil {
			if name == "tcp6" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, parseProcNetTCP(b)...)
	}
	return out, nil
}

// peerSet is a set of client addresses (unmapped, without port).
type peerSet map[netip.Addr]struct{}

// connSet is a set of the remote ends (address unmapped, and port) of
// connections.
type connSet map[netip.AddrPort]struct{}

// tcpRemotes is the remote ends of the TCP connections on local port port
// (only established ones unless anyState), listeners and loopback left out
// and IPv4-mapped IPv6 addresses unmapped.
func tcpRemotes(procDir string, port uint16, anyState bool) (connSet, error) {
	conns, err := readProcTCP(procDir)
	if err != nil {
		return nil, err
	}
	out := connSet{}
	for _, c := range conns {
		if c.local.Port() != port || (!anyState && c.state != tcpEstablished) {
			continue
		}
		// A listener's remote is *:0.
		a := c.remote.Addr().Unmap()
		if c.remote.Port() == 0 || a.IsLoopback() || a.IsUnspecified() {
			continue
		}
		out[netip.AddrPortFrom(a, c.remote.Port())] = struct{}{}
	}
	return out, nil
}

// addrs is the distinct addresses of the connections, but those in
// except, which a connection keeps for as long as it exists (TIME_WAIT
// included), while a new one gets a new port.
func (c connSet) addrs(except connSet) peerSet {
	out := peerSet{}
	for ap := range c {
		if _, old := except[ap]; !old {
			out[ap.Addr()] = struct{}{}
		}
	}
	return out
}

// sunshineClients samples who is in Moonlight's /launch right now: the
// established connections on Sunshine's HTTPS port. Begin samples it first
// thing and again before it answers.
func sunshineClients() (peerSet, error) {
	c, err := tcpRemotes(ProcDir, SunshineHTTPSPort, false)
	if err != nil {
		return nil, err
	}
	return c.addrs(nil), nil
}

// rtspConns is the connections on Sunshine's RTSP port, TIME_WAIT
// included: a device whose stream starts makes some right after /launch.
// It settles an ambiguous begin.
func rtspConns() (connSet, error) { return tcpRemotes(ProcDir, SunshineRTSPPort, true) }

// and is the addresses in both sets.
func (p peerSet) and(q peerSet) peerSet {
	out := peerSet{}
	for a := range p {
		if _, ok := q[a]; ok {
			out[a] = struct{}{}
		}
	}
	return out
}

// one is the only address, if there is exactly one.
func (p peerSet) one() (netip.Addr, bool) {
	if len(p) != 1 {
		return netip.Addr{}, false
	}
	for a := range p {
		return a, true
	}
	return netip.Addr{}, false
}
