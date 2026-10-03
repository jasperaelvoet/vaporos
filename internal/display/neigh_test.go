package display

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"syscall"
	"testing"
)

// A netlink dump as the kernel sends one, in the host's byte order.
func neighNlMsg(typ uint16, payload []byte) []byte {
	b := make([]byte, nlmsgHdrLen)
	binary.NativeEndian.PutUint32(b[0:], uint32(nlmsgHdrLen+len(payload)))
	binary.NativeEndian.PutUint16(b[4:], typ)
	b = append(b, payload...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func neighAttr(typ uint16, data []byte) []byte {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint16(b[0:], uint16(4+len(data)))
	binary.NativeEndian.PutUint16(b[2:], typ)
	b = append(b, data...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func neighEntry(addr string, state uint16, mac string) []byte {
	a := netip.MustParseAddr(addr)
	m := make([]byte, ndMsgLen)
	m[0] = linuxAFInet
	if a.Is6() {
		m[0] = linuxAFInet6
	}
	binary.NativeEndian.PutUint32(m[4:], 2) // ifindex
	binary.NativeEndian.PutUint16(m[8:], state)
	m = append(m, neighAttr(ndaDst, a.AsSlice())...)
	if mac != "" {
		hw, _ := net.ParseMAC(mac)
		m = append(m, neighAttr(ndaLLAddr, hw)...)
	}
	m = append(m, neighAttr(8, []byte{1, 0, 0, 0})...) // NDA_PROBES, ignored
	return neighNlMsg(rtmNewNeigh, m)
}

func neighDump(entries ...[]byte) []byte {
	var b []byte
	for _, e := range entries {
		b = append(b, e...)
	}
	return append(b, neighNlMsg(nlmsgDone, []byte{0, 0, 0, 0})...)
}

func useNeighbours(t *testing.T, dump []byte, err error) {
	t.Helper()
	save := readNeighbours
	t.Cleanup(func() { readNeighbours = save })
	readNeighbours = func() ([]neighbour, error) {
		if err != nil {
			return nil, err
		}
		return parseNeighbours(dump)
	}
}

func TestNeighbourMAC(t *testing.T) {
	useNeighbours(t, neighDump(
		neighEntry("192.168.1.40", nudReachable, "AA:BB:CC:DD:EE:FF"),
		neighEntry("192.168.1.41", 0x20, "aa:bb:cc:dd:ee:01"), // failed
		neighEntry("192.168.1.42", nudStale, "00:00:00:00:00:00"),
		neighEntry("192.168.1.43", 0x01, ""),                  // incomplete
		neighEntry("192.168.1.44", 0x10, "aa:bb:cc:dd:ee:04"), // probe
		neighEntry("192.168.1.45", 0x40, "aa:bb:cc:dd:ee:05"), // noarp
		neighEntry("192.168.1.46", nudStale, "aa:bb:cc:dd:ee:06"),
		neighEntry("fe80::1c2b:3cff:fe4d:5e6f", nudDelay, "11:22:33:44:55:66"),
		neighEntry("fd00::40", nudPermanent, "11:22:33:44:55:77"),
	), nil)
	for addr, want := range map[string]string{
		"192.168.1.40":                    "aa:bb:cc:dd:ee:ff",
		"::ffff:192.168.1.40":             "aa:bb:cc:dd:ee:ff",
		"192.168.1.41":                    "",
		"192.168.1.42":                    "",
		"192.168.1.43":                    "",
		"192.168.1.44":                    "",
		"192.168.1.45":                    "",
		"192.168.1.46":                    "aa:bb:cc:dd:ee:06",
		"fe80::1c2b:3cff:fe4d:5e6f%wlan0": "11:22:33:44:55:66",
		"fd00::40":                        "11:22:33:44:55:77",
		"192.168.1.99":                    "",
		"127.0.0.1":                       "",
	} {
		if got := NeighbourMAC(netip.MustParseAddr(addr)); got != want {
			t.Errorf("NeighbourMAC(%s) = %q, want %q", addr, got, want)
		}
	}
	if got := NeighbourMAC(netip.Addr{}); got != "" {
		t.Errorf("NeighbourMAC(invalid) = %q", got)
	}
}

func TestNeighbourDumpErrors(t *testing.T) {
	useNeighbours(t, nil, errors.New("no netlink"))
	if got := NeighbourMAC(netip.MustParseAddr("192.168.1.40")); got != "" {
		t.Errorf("without a table: %q", got)
	}

	errMsg := make([]byte, 4)
	binary.NativeEndian.PutUint32(errMsg, uint32(0xffffffff)) // -EPERM
	if _, err := parseNeighbours(neighNlMsg(nlmsgError, errMsg)); !errors.Is(err, syscall.EPERM) {
		t.Errorf("netlink error: %v", err)
	}
	good := neighEntry("192.168.1.40", nudReachable, "aa:bb:cc:dd:ee:ff")
	cut := append(append([]byte{}, good...), good[:20]...)
	binary.NativeEndian.PutUint32(cut[len(good):], 200)
	if nbs, err := parseNeighbours(cut); err == nil || len(nbs) != 1 {
		t.Errorf("a cut dump: %v, %v", nbs, err)
	}
	// Whatever follows NLMSG_DONE is not read.
	after := append(neighDump(), neighEntry("192.168.1.40", nudReachable, "aa:bb:cc:dd:ee:ff")...)
	if nbs, err := parseNeighbours(after); err != nil || len(nbs) != 0 {
		t.Errorf("after done: %v, %v", nbs, err)
	}
	// A message too short for its ndmsg, and an attribute cut short.
	short := neighNlMsg(rtmNewNeigh, []byte{2, 0, 0, 0})
	badAttr := neighEntry("192.168.1.40", nudReachable, "aa:bb:cc:dd:ee:ff")
	binary.NativeEndian.PutUint16(badAttr[nlmsgHdrLen+ndMsgLen:], 200)
	if nbs, err := parseNeighbours(neighDump(short, badAttr)); err != nil || len(nbs) != 0 {
		t.Errorf("broken messages: %v, %v", nbs, err)
	}
}

// The real table, where there is one: the dump must parse.
func TestNeighbourDumpLive(t *testing.T) {
	nbs, err := dumpNeighbours()
	if runtime.GOOS != "linux" {
		if err == nil {
			t.Error("a neighbour table off Linux")
		}
		return
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPROTONOSUPPORT) {
		t.Skipf("netlink unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, nb := range nbs {
		if !nb.addr.IsValid() {
			t.Errorf("entry without an address: %+v", nb)
		}
	}
}
