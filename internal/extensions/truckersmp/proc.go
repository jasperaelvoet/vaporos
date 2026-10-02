package truckersmp

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/gameproc"
)

// procFS reads the gaming user's own processes, as that user: which
// Steam reapers run (gameproc.ReaperApp) and which one a process runs
// under.
type procFS struct {
	dir string // /proc
	uid int
}

func ownProcs() procFS { return procFS{dir: "/proc", uid: os.Getuid()} }

// reapers are the AppIds of the user's Steam reapers.
func (p procFS) reapers() []uint64 {
	ents, err := os.ReadDir(p.dir)
	if err != nil {
		return nil
	}
	var out []uint64
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if uid, _ := p.status(pid); uid != p.uid {
			continue
		}
		if app, ok := gameproc.ReaperApp(p.cmdline(pid)); ok {
			out = append(out, app)
		}
	}
	return out
}

// reaperAbove is the AppId of the nearest Steam reaper above pid (the
// shortcut that runs it), 0 when none is.
func (p procFS) reaperAbove(pid int) uint64 {
	for range 64 {
		_, ppid := p.status(pid)
		if ppid <= 1 {
			return 0
		}
		if app, ok := gameproc.ReaperApp(p.cmdline(ppid)); ok {
			return app
		}
		pid = ppid
	}
	return 0
}

// status reads Uid (real) and PPid from /proc/<pid>/status; -1 when unknown.
func (p procFS) status(pid int) (uid, ppid int) {
	uid, ppid = -1, -1
	f, err := openNoFollow(filepath.Join(p.dir, strconv.Itoa(pid), "status"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		switch k {
		case "Uid":
			uid = n
		case "PPid":
			ppid = n
		}
	}
	return
}

func (p procFS) cmdline(pid int) []string {
	f, err := openNoFollow(filepath.Join(p.dir, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// sameApp tells whether two reaper AppIds name one app: a shortcut's may
// appear as its 32-bit app id or as its 64-bit game id.
func sameApp(a, b uint64) bool {
	norm := func(x uint64) uint64 {
		if x>>32 != 0 {
			return x >> 32
		}
		return x
	}
	return a != 0 && norm(a) == norm(b)
}
