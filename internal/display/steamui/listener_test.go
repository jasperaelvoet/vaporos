package steamui

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The kernel's own format, from a box with Steam's debugger, sshd and
// a client connected to the debugger (little-endian, as every box).
const realTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:7CA7 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 52114 1 0000000000000000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 18321 1 0000000000000000 100 0 0 10 0
   2: 0100007F:7CA7 0100007F:B2E4 01 00000000:00000000 00:00000000 00000000  1000        0 53001 1 0000000000000000 20 4 30 10 -1
   3: 2801A8C0:BB80 2701A8C0:D431 01 00000000:00000000 02:000AFC80 00000000     0        0 61234 2 0000000000000000 20 4 1 10 -1
`

const realTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 18323 1 0000000000000000 100 0 0 10 0
   1: 0000000000000000FFFF00002801A8C0:BB80 0000000000000000FFFF00002701A8C0:D432 01 00000000:00000000 00:00000000 00000000     0        0 61240 1 0000000000000000 20 4 1 10 -1
   2: 000080FE00000000FF2A1A02FE3B4C5D:BB80 000080FE00000000FF2A1A02010203A4:D433 01 00000000:00000000 00:00000000 00000000     0        0 61241 1 0000000000000000 20 4 1 10 -1
`

func littleEndian(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("the fixtures are a little-endian kernel's")
	}
}

func TestReadTCP(t *testing.T) {
	littleEndian(t)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "tcp"), realTCP)
	mustWrite(t, filepath.Join(dir, "tcp6"), realTCP6)
	v4, err := readTCP(filepath.Join(dir, "tcp"))
	if err != nil {
		t.Fatal(err)
	}
	want4 := []tcpSocket{
		{netip.MustParseAddr("127.0.0.1"), 31911, tcpListen, 52114},
		{netip.MustParseAddr("0.0.0.0"), 22, tcpListen, 18321},
		{netip.MustParseAddr("127.0.0.1"), 31911, 1, 53001},
		{netip.MustParseAddr("192.168.1.40"), 48000, 1, 61234},
	}
	if !slices.Equal(v4, want4) {
		t.Errorf("tcp:\n got %v\nwant %v", v4, want4)
	}
	v6, err := readTCP(filepath.Join(dir, "tcp6"))
	if err != nil {
		t.Fatal(err)
	}
	want6 := []tcpSocket{
		{netip.MustParseAddr("::"), 22, tcpListen, 18323},
		{netip.MustParseAddr("::ffff:192.168.1.40"), 48000, 1, 61240},
		{netip.MustParseAddr("fe80::21a:2aff:5d4c:3bfe"), 48000, 1, 61241},
	}
	if !slices.Equal(v6, want6) {
		t.Errorf("tcp6:\n got %v\nwant %v", v6, want6)
	}
}

func TestListenersOfThePort(t *testing.T) {
	littleEndian(t)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "net", "tcp"), realTCP)
	mustWrite(t, filepath.Join(dir, "net", "tcp6"), realTCP6)
	ls, err := listeners(dir, 31911)
	if err != nil {
		t.Fatal(err)
	}
	// The ESTABLISHED socket on the port is no listener.
	if want := []listener{{inode: 52114, loopback: true}}; !slices.Equal(ls, want) {
		t.Errorf("got %v, want %v", ls, want)
	}
	// sshd's 0.0.0.0:22 and [::]:22 would both take a connection to
	// 127.0.0.1:22.
	ls, _ = listeners(dir, 22)
	if want := []listener{{inode: 18321}, {inode: 18323}}; !slices.Equal(ls, want) {
		t.Errorf("port 22: got %v, want %v", ls, want)
	}
	// Without IPv6 there is no tcp6.
	os.Remove(filepath.Join(dir, "net", "tcp6"))
	if ls, err := listeners(dir, 31911); err != nil || len(ls) != 1 {
		t.Errorf("without tcp6: %v, %v", ls, err)
	}
}

func TestListenerOwner(t *testing.T) {
	const port = 31911
	cases := []struct {
		name  string
		setup func(p *fakeProc)
		owner *Owner // nil: accepted; else the *ListenerError's owner
		none  bool   // a bare ErrNoDebugger
	}{
		{name: "steam's helper", setup: func(p *fakeProc) {}},
		{name: "nothing listens", none: true, setup: func(p *fakeProc) {
			p.reset()
			p.socket("127.0.0.1", port, "127.0.0.1", 45000, 1, 52115) // a connection, no listener
		}},
		{name: "only on ::1", none: true, setup: func(p *fakeProc) {
			p.reset()
			p.listen("::1", port, 52114)
		}},
		{name: "another port", none: true, setup: func(p *fakeProc) {
			p.reset()
			p.listen("127.0.0.1", 8080, 52114)
		}},
		{name: "a game named itself something else", owner: &Owner{PID: 900, UID: 1000, Comm: "python3"}, setup: func(p *fakeProc) {
			p.reset()
			p.process(900, "1000\t1000\t1000\t1000", "python3", 60001)
			p.listen("127.0.0.1", port, 60001)
		}},
		{name: "root's", owner: &Owner{PID: 901, UID: 0, Comm: "steamwebhelper"}, setup: func(p *fakeProc) {
			p.reset()
			p.process(901, "0\t0\t0\t0", "steamwebhelper", 60002)
			p.listen("127.0.0.1", port, 60002)
		}},
		{name: "mixed uids", owner: &Owner{PID: 902, UID: -1, Comm: "steamwebhelper"}, setup: func(p *fakeProc) {
			p.reset()
			p.process(902, "1000\t0\t1000\t1000", "steamwebhelper", 60003)
			p.listen("127.0.0.1", port, 60003)
		}},
		{name: "held by nobody visible", owner: &Owner{}, setup: func(p *fakeProc) {
			p.reset()
			p.listen("127.0.0.1", port, 60004)
		}},
		{name: "a second listener on 0.0.0.0", owner: &Owner{PID: 903, UID: 1000, Comm: "nc"}, setup: func(p *fakeProc) {
			p.process(903, "1000\t1000\t1000\t1000", "nc", 60005)
			p.listen("0.0.0.0", port, 60005)
		}},
		{name: "a dual-stack listener on ::", owner: &Owner{PID: 904, UID: 1000, Comm: "nc"}, setup: func(p *fakeProc) {
			p.process(904, "1000\t1000\t1000\t1000", "nc", 60006)
			p.listen("::", port, 60006)
		}},
		{name: "v4-mapped loopback", owner: &Owner{PID: 905, UID: 1000, Comm: "nc"}, setup: func(p *fakeProc) {
			p.process(905, "1000\t1000\t1000\t1000", "nc", 60007)
			p.listen("::ffff:127.0.0.1", port, 60007)
		}},
		{name: "steam's helper twice (reuseport)", setup: func(p *fakeProc) {
			p.process(906, "1000\t1000\t1000\t1000", "steamwebhelper", 60008)
			p.listen("127.0.0.1", port, 60008)
		}},
		{name: "steam's helper and ::1 elsewhere", setup: func(p *fakeProc) {
			p.process(907, "1000\t1000\t1000\t1000", "nc", 60009)
			p.listen("::1", port, 60009) // takes no IPv4 connection
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProc(t, port)
			tc.setup(p)
			p.write()
			c := &Client{ProcDir: p.dir}
			err := c.checkListener()
			if !tc.none && tc.owner == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrNoDebugger) {
				t.Fatalf("got %v, want ErrNoDebugger", err)
			}
			var le *ListenerError
			if tc.none {
				if errors.As(err, &le) {
					t.Fatalf("got %v, want a bare ErrNoDebugger", err)
				}
				return
			}
			if !errors.As(err, &le) || le.Owner != *tc.owner || le.Port != port {
				t.Fatalf("got %#v, want owner %+v", err, *tc.owner)
			}
			t.Log(err)
		})
	}
}

func TestListenerNoProcNet(t *testing.T) {
	c := &Client{ProcDir: t.TempDir()}
	if err := c.checkListener(); !errors.Is(err, ErrNoDebugger) {
		t.Fatalf("got %v", err)
	}
}

// The owner is looked up once per socket: a check while the same socket
// listens does not search /proc again, a new socket is searched.
func TestListenerOwnerCachedByInode(t *testing.T) {
	p := newFakeProc(t, DevtoolsPort)
	c := &Client{ProcDir: p.dir}
	if err := c.checkListener(); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(p.dir, "700"))
	if err := c.checkListener(); err != nil {
		t.Fatalf("same socket, searched again: %v", err)
	}
	// Steam restarted as another process with another socket.
	p.reset()
	p.process(800, "1000\t1000\t1000\t1000", "steamwebhelper", 52200)
	p.listen("127.0.0.1", DevtoolsPort, 52200)
	p.write()
	if err := c.checkListener(); err != nil {
		t.Fatal(err)
	}
	if len(c.owners) != 1 || c.owners[52200].PID != 800 {
		t.Errorf("owners %v, want only the new socket's", c.owners)
	}
	// A socket nobody holds is not remembered, so the next check looks
	// again.
	p.reset()
	p.listen("127.0.0.1", DevtoolsPort, 52300)
	p.write()
	if err := c.checkListener(); err == nil {
		t.Fatal("a socket of nobody passed")
	}
	p.process(801, "1000\t1000\t1000\t1000", "steamwebhelper", 52300)
	if err := c.checkListener(); err != nil {
		t.Fatalf("after its process showed up: %v", err)
	}
}

func TestProcUID(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		status string
		want   int
	}{
		{"Name:\tx\nUid:\t1000\t1000\t1000\t1000\n", 1000},
		{"Uid:\t0\t0\t0\t0\n", 0},
		{"Uid:\t1000\t1000\t0\t1000\n", -1},
		{"Uid:\t1000\n", -1},
		{"Name:\tx\n", -1},
		{"Uid:\tx\tx\tx\tx\n", -1},
	} {
		mustWrite(t, filepath.Join(dir, "status"), tc.status)
		if got := procUID(dir); got != tc.want {
			t.Errorf("%q: %d, want %d", tc.status, got, tc.want)
		}
	}
	if got := procUID(filepath.Join(dir, "gone")); got != -1 {
		t.Errorf("missing: %d", got)
	}
}
