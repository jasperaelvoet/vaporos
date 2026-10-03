package steamui

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// GamerUID is the gaming user Steam runs as (config.GamerUID; this
// package imports nothing of VaporOS's).
const GamerUID = 1000

// helperComm is the comm of the Steam process that holds the debugger's
// socket: CEF's browser process.
const helperComm = "steamwebhelper"

const tcpListen = 0x0A // the kernel's TCP_LISTEN, as /proc/net/tcp prints it

// Owner is the process that holds a listening socket.
type Owner struct {
	PID  int    // 0 when no process holds it that vosd can see
	UID  int    // the uid all of its ids (real, effective, saved, fs) are, -1 when they differ or are unknown
	Comm string // its comm
}

func (o Owner) steam() bool { return o.PID > 0 && o.UID == GamerUID && o.Comm == helperComm }

// ListenerError: something other than Steam's steamwebhelper listens on
// the debugger's port. It is an ErrNoDebugger that names the owner, for
// the log.
type ListenerError struct {
	Port  int
	Owner Owner
}

func (e *ListenerError) Error() string {
	if e.Owner.PID == 0 {
		return fmt.Sprintf("steamui: no process holds the listener on 127.0.0.1:%d", e.Port)
	}
	return fmt.Sprintf("steamui: 127.0.0.1:%d is held by pid %d (uid %d, %q), not Steam's %s",
		e.Port, e.Owner.PID, e.Owner.UID, clampString(e.Owner.Comm, 16), helperComm)
}

func (e *ListenerError) Unwrap() error { return ErrNoDebugger }

// listener is a LISTEN socket that takes connections to 127.0.0.1:port.
type listener struct {
	inode    uint64
	loopback bool // bound to 127.0.0.1 itself (not 0.0.0.0 or ::)
}

// checkListener makes sure that every socket that could take vosd's
// connection to 127.0.0.1:port is Steam's and that there is one bound to
// 127.0.0.1. Owners are remembered by socket inode, so /proc is only
// searched when the listener changes.
func (c *Client) checkListener() error {
	ls, err := listeners(c.procDir(), c.port())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoDebugger, err)
	}
	loopback := false
	for _, l := range ls {
		loopback = loopback || l.loopback
	}
	if !loopback {
		return ErrNoDebugger
	}
	c.ownMu.Lock()
	defer c.ownMu.Unlock()
	want := map[uint64]bool{}
	for _, l := range ls {
		if _, ok := c.owners[l.inode]; !ok {
			want[l.inode] = true
		}
	}
	found := map[uint64]Owner{}
	if len(want) > 0 {
		found = findOwners(c.procDir(), want)
	}
	owners := map[uint64]Owner{}
	for _, l := range ls {
		if o, ok := c.owners[l.inode]; ok {
			owners[l.inode] = o
		} else if o, ok := found[l.inode]; ok {
			owners[l.inode] = o
		}
	}
	c.owners = owners
	for _, l := range ls {
		o, ok := owners[l.inode]
		if !ok || !o.steam() {
			return &ListenerError{Port: c.port(), Owner: o}
		}
	}
	return nil
}

// listeners reads the LISTEN sockets on port that take connections to
// 127.0.0.1: bound to it or to 0.0.0.0 (net/tcp), or to :: or a
// v4-mapped one of those (net/tcp6, a dual-stack socket). tcp6 is missing
// when IPv6 is off.
func listeners(procDir string, port int) ([]listener, error) {
	var out []listener
	for _, name := range []string{"tcp", "tcp6"} {
		socks, err := readTCP(filepath.Join(procDir, "net", name))
		if err != nil {
			if name == "tcp6" && os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, s := range socks {
			if s.state != tcpListen || s.port != port {
				continue
			}
			a := s.addr.Unmap()
			if a.Is4() && (a == netip.AddrFrom4([4]byte{127, 0, 0, 1}) || a.IsUnspecified()) ||
				s.addr.Is6() && s.addr.IsUnspecified() {
				out = append(out, listener{inode: s.inode, loopback: a == netip.AddrFrom4([4]byte{127, 0, 0, 1})})
			}
		}
	}
	return out, nil
}

// tcpSocket is one line of /proc/net/tcp or tcp6.
type tcpSocket struct {
	addr  netip.Addr // local
	port  int        // local
	state int
	inode uint64
}

// readTCP parses /proc/net/tcp or tcp6:
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:7CA7 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 52114 …
//
// An address is the kernel's in-memory words printed as hex in host
// order (four of them for IPv6), the port hex.
func readTCP(path string) ([]tcpSocket, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []tcpSocket
	sc := bufio.NewScanner(f)
	sc.Scan() // the header
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 10 {
			continue
		}
		ip, port, ok := strings.Cut(fs[1], ":")
		if !ok {
			continue
		}
		addr, ok := parseHexAddr(ip)
		p, err1 := strconv.ParseUint(port, 16, 16)
		st, err2 := strconv.ParseUint(fs[3], 16, 8)
		ino, err3 := strconv.ParseUint(fs[9], 10, 64)
		if !ok || err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, tcpSocket{addr: addr, port: int(p), state: int(st), inode: ino})
	}
	return out, sc.Err()
}

func parseHexAddr(s string) (netip.Addr, bool) {
	raw, err := hex.DecodeString(s)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.Addr{}, false
	}
	b := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(b[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	a, _ := netip.AddrFromSlice(b)
	return a, true
}

// findOwners looks through every process's descriptors for the sockets
// in want and returns the processes holding them.
func findOwners(procDir string, want map[uint64]bool) map[uint64]Owner {
	found := map[uint64]Owner{}
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return found
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil {
				continue
			}
			n, ok := strings.CutPrefix(link, "socket:[")
			if !ok {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(n, "]"), 10, 64)
			if err != nil || !want[ino] {
				continue
			}
			if _, done := found[ino]; !done {
				found[ino] = Owner{PID: pid, UID: procUID(dir), Comm: procComm(dir)}
			}
			if len(found) == len(want) {
				return found
			}
		}
	}
	return found
}

// The process files are the gaming user's to shape (a FIFO cannot appear
// in /proc, but a fake tree is a plain directory): reads never follow a
// symlink at the end, never block and are bounded.
func readProcFile(path string, limit int64) []byte {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	return b
}

func procComm(dir string) string {
	return strings.TrimSuffix(string(readProcFile(filepath.Join(dir, "comm"), 64)), "\n")
}

// procUID is the uid of <dir>/status's Uid line when its four ids agree,
// else -1.
func procUID(dir string) int {
	for _, line := range strings.Split(string(readProcFile(filepath.Join(dir, "status"), 64<<10)), "\n") {
		rest, ok := strings.CutPrefix(line, "Uid:")
		if !ok {
			continue
		}
		ids := strings.Fields(rest)
		if len(ids) != 4 {
			return -1
		}
		uid, err := strconv.Atoi(ids[0])
		if err != nil {
			return -1
		}
		for _, id := range ids[1:] {
			if id != ids[0] {
				return -1
			}
		}
		return uid
	}
	return -1
}
