package display

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func usePeerProc(t *testing.T, dir string) {
	t.Helper()
	save := ProcDir
	t.Cleanup(func() { ProcDir = save })
	ProcDir = dir
}

func peers(addrs ...string) peerSet {
	out := peerSet{}
	for _, a := range addrs {
		out[netip.MustParseAddr(a)] = struct{}{}
	}
	return out
}

// Addresses as an x86 kernel prints them: 32-bit words, little-endian.
func TestPeerParsesProcAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:BB70":                         "127.0.0.1:47984",
		"2801A8C0:D2F4":                         "192.168.1.40:54004",
		"00000000:0000":                         "0.0.0.0:0",
		"00000000000000000000000001000000:0050": "[::1]:80",
		"0000000000000000FFFF00003C01A8C0:D400": "[::ffff:192.168.1.60]:54272",
		"1018022A0100CDAB0000000040000000:D500": "[2a02:1810:abcd:1::40]:54528",
		"000080FE00000000FF3C2B1C6F5E4DFE:0050": "[fe80::1c2b:3cff:fe4d:5e6f]:80",
	} {
		got, ok := parseProcAddrPort(in)
		if !ok || got.String() != want {
			t.Errorf("parseProcAddrPort(%q) = %v, %v; want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0100007F", "0100007F:", "0100007F:XYZ", "100007F:BB70", "0100007G:BB70", "0100007F:1BB70"} {
		if got, ok := parseProcAddrPort(in); ok {
			t.Errorf("parseProcAddrPort(%q) = %v", in, got)
		}
	}
}

func TestPeerParsesProcNetTCP(t *testing.T) {
	conns := parseProcNetTCP([]byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0201A8C0:BB70 2801A8C0:D2F4 01 00000000:00000000 02:000AFC80 00000000  1000        0 50001 2 0000000000000000 21 4 30 10 -1\n" +
		"   1: garbage\n" +
		"   2: 0201A8C0:BB70 3201A8C0:E004 06 00000000:00000000 03:00001523 00000000     0        0 0 3 0000000000000000\n" +
		"   3: 0201A8C0:BB70 3201A8C0:E004 ZZ 00000000:00000000 03:00001523 00000000     0        0 0 3 0000000000000000\n"))
	want := []procTCPConn{
		{netip.MustParseAddrPort("192.168.1.2:47984"), netip.MustParseAddrPort("192.168.1.40:54004"), tcpEstablished},
		{netip.MustParseAddrPort("192.168.1.2:47984"), netip.MustParseAddrPort("192.168.1.50:57348"), 0x06},
	}
	if !reflect.DeepEqual(conns, want) {
		t.Errorf("parseProcNetTCP = %+v", conns)
	}
}

func TestPeerSunshineClients(t *testing.T) {
	usePeerProc(t, filepath.Join("testdata", "proc"))
	got, err := sunshineClients()
	if err != nil {
		t.Fatal(err)
	}
	// Two connections from .40 are one client; .50 is in TIME_WAIT and on
	// port 80, loopback and listeners are nobody, ::ffff:.60 is .60.
	if want := peers("192.168.1.40", "192.168.1.60", "2a02:1810:abcd:1::40"); !reflect.DeepEqual(got, want) {
		t.Errorf("sunshineClients = %v, want %v", got, want)
	}
	// RTSP: .40's connection in TIME_WAIT, by its port; the listener is
	// nobody.
	conns, err := rtspConns()
	if err != nil {
		t.Fatal(err)
	}
	if want := (connSet{netip.MustParseAddrPort("192.168.1.40:54016"): {}}); !reflect.DeepEqual(conns, want) {
		t.Errorf("rtspConns = %v, want %v", conns, want)
	}
}

// What was there before only counts while its connection lasts: a new
// connection from the same address has a new port.
func TestPeerConnsExcept(t *testing.T) {
	old := netip.MustParseAddrPort("192.168.1.40:54016")
	now := connSet{old: {}, netip.MustParseAddrPort("192.168.1.50:40000"): {}}
	if got, want := now.addrs(connSet{old: {}}), peers("192.168.1.50"); !reflect.DeepEqual(got, want) {
		t.Errorf("addrs = %v, want %v", got, want)
	}
	now[netip.MustParseAddrPort("192.168.1.40:54018")] = struct{}{}
	if got, want := now.addrs(connSet{old: {}}), peers("192.168.1.40", "192.168.1.50"); !reflect.DeepEqual(got, want) {
		t.Errorf("addrs with a new connection = %v, want %v", got, want)
	}
	if got, want := now.addrs(nil), peers("192.168.1.40", "192.168.1.50"); !reflect.DeepEqual(got, want) {
		t.Errorf("addrs = %v, want %v", got, want)
	}
}

func TestPeerWithoutIPv6(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	usePeerProc(t, dir)
	if _, err := sunshineClients(); err == nil {
		t.Error("no /proc/net/tcp, no error")
	}
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
			"   0: 0201A8C0:BB70 2801A8C0:D2F4 01 00000000:00000000 02:000AFC80 00000000  1000        0 50001 2 0000000000000000 21 4 30 10 -1\n"), 0o644)
	got, err := sunshineClients()
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := got.one(); !ok || a != netip.MustParseAddr("192.168.1.40") {
		t.Errorf("one = %v, %v", a, ok)
	}
}

// Begin samples at its start and before it answers; the client is the one
// address in both.
func TestPeerSamples(t *testing.T) {
	for _, c := range []struct {
		what          string
		first, second peerSet
		want          string // "" for ambiguous
	}{
		{"one client", peers("192.168.1.40"), peers("192.168.1.40"), "192.168.1.40"},
		{"another device's poll came and went", peers("192.168.1.40", "192.168.1.50"), peers("192.168.1.40"), "192.168.1.40"},
		{"a poll came in between", peers("192.168.1.40"), peers("192.168.1.40", "192.168.1.50"), "192.168.1.40"},
		{"two all along", peers("192.168.1.40", "192.168.1.50"), peers("192.168.1.40", "192.168.1.50"), ""},
		{"gone by the reply", peers("192.168.1.40"), peers(), ""},
		{"nothing", peers(), peers(), ""},
		{"a failed sample", nil, peers("192.168.1.40"), ""},
	} {
		a, ok := c.first.and(c.second).one()
		if got := map[bool]string{true: a.String(), false: ""}[ok]; got != c.want {
			t.Errorf("%s: client = %q, want %q", c.what, got, c.want)
		}
	}
}
