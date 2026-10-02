package truckersmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// The sync, `vos ext truckersmp sync`, run as the gaming user: it brings
// the mod's files in the home data area in line with TruckersMP's
// files.json for the games installed, checking each by the MD5 TruckersMP
// publishes, and prints its progress as {"bytes","total"} lines.

// Bounds on the download. Variables for tests.
var (
	maxFileSize  int64 = 2 << 30
	maxTotalSize int64 = 8 << 30
	stallTimeout       = 60 * time.Second
)

// errSyncRunning means another sync holds the lock.
var errSyncRunning = errors.New("another sync of the TruckersMP files is running")

// errUnchecked means the version API does not vouch for a wanted game's
// core library: it gives no checksum for it, or another than files.json.
var errUnchecked = errors.New("TruckersMP's checksums don't vouch for its files yet")

// spaceError means the files do not fit in the home data area with
// store.ExtReserve to spare.
type spaceError struct{ short int64 } // the bytes missing

func (e *spaceError) Error() string {
	return "not enough free space for the TruckersMP files: " + sizeText(e.short) + " short"
}

// freeSpace is the bytes free to the gaming user on path's filesystem; a
// variable for tests.
var freeSpace = func(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return -1, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

type syncer struct {
	home     string          // the home data area
	client   *http.Client    // TruckersMP's servers
	progress io.Writer       // {"bytes","total"} lines
	games    func() []string // the games installed now
	now      func() time.Time
	free     func(path string) (int64, error)

	done  int64     // bytes downloaded, or found in partial/
	sized int64     // the sizes known so far, held to maxTotalSize
	total int64     // the whole download's size for progress, 0 while not all of it is known
	last  time.Time // when progress was last printed
}

func newSyncer(progress io.Writer) *syncer {
	return &syncer{
		home: homeDir(), client: newClient(0), progress: progress,
		games: func() []string { return installedGames(libraries()) },
		now:   time.Now, free: freeSpace,
	}
}

// run is one sync. It holds the lock in the home data area throughout,
// so two never write the files at once.
func (s *syncer) run(ctx context.Context) error {
	for _, d := range []string{s.home, filepath.Join(s.home, filesRel), filepath.Join(s.home, partialRel)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	unlock, err := lockSync(filepath.Join(s.home, lockRel))
	if err != nil {
		return err
	}
	defer unlock()

	actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	info, err := fetchVersion(actx, s.client)
	if err != nil {
		cancel()
		return fmt.Errorf("asking TruckersMP for its version: %w", err)
	}
	b, err := getSmall(actx, s.client, filesURL, maxFilesBody)
	cancel()
	if err != nil {
		return fmt.Errorf("asking TruckersMP for its files: %w", err)
	}
	list, err := parseFiles(b)
	if err != nil {
		return err
	}
	old, err := readManifest(s.home)
	if err != nil {
		old = nil // a damaged manifest only costs a full check
	}

	var keys []string
	for _, g := range games {
		if slices.Contains(s.games(), g.key) || old.has(g) {
			keys = append(keys, g.key)
		}
	}
	var want []modFile
	for _, f := range list {
		if (f.Type == "system" && len(keys) > 0) || slices.Contains(keys, f.Type) {
			want = append(want, f)
		}
	}
	for _, k := range keys {
		g, _ := gameByKey(k)
		i := slices.IndexFunc(want, func(f modFile) bool { return f.Path == g.coreDLL && f.Type == g.key })
		if i < 0 {
			return fmt.Errorf("files.json has no %s", g.coreDLL)
		}
		switch sum := info.coreMD5(g); {
		case sum == "":
			return fmt.Errorf("%w: the version API gives no checksum for %s; trying again later", errUnchecked, g.coreDLL)
		case want[i].MD5 != sum:
			return fmt.Errorf("%w: files.json and the version API give %s different checksums; trying again later", errUnchecked, g.coreDLL)
		}
	}

	next := &manifest{Version: info.Name, Games: keys, Files: []manifestFile{}}
	var need []modFile
	for _, f := range want {
		if e, ok := s.current(old, f); ok {
			next.Files = append(next.Files, e)
		} else {
			need = append(need, f)
		}
	}
	if len(need) > 0 {
		if err := s.fetchAll(ctx, need); err != nil {
			return err
		}
		// Only now do the files change: no manifest until they are whole.
		if err := os.Remove(filepath.Join(s.home, manifestRel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		placed, err := s.place(need)
		if err != nil {
			return err
		}
		next.Files = append(next.Files, placed...)
	}
	s.cleanUp(old, want)
	next.Checked = s.now().UTC()
	slices.SortFunc(next.Files, func(a, b manifestFile) int { return strings.Compare(a.Path, b.Path) })
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(filepath.Join(s.home, manifestRel), append(data, '\n'), 0o644); err != nil {
		return err
	}
	s.report(true)
	return nil
}

// current returns f's manifest entry when the file on disk already is f:
// unchanged since the last sync recorded it with this MD5, or, without
// such a record, of this MD5 now.
func (s *syncer) current(old *manifest, f modFile) (manifestFile, bool) {
	p := filepath.Join(s.home, filesRel, filepath.FromSlash(f.Path))
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return manifestFile{}, false
	}
	e := manifestFile{Path: f.Path, Type: f.Type, MD5: f.MD5, Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
	if o, ok := old.entry(f.Path); ok && o.MD5 == f.MD5 && o.Size == e.Size && o.MTime == e.MTime {
		return e, true
	}
	if sum, err := fileMD5(p); err == nil && sum == f.MD5 {
		return e, true
	}
	return manifestFile{}, false
}

// fetchAll downloads need into partial/ and checks each download's MD5,
// changing nothing in MODDIR: whatever stops it (a size over the bounds,
// too little free space, a download that fails or does not match) leaves
// the files and the manifest as they were, so multiplayer keeps working.
// It sizes every file first (HEAD) for the free space and the progress
// total; files of one MD5 download once.
func (s *syncer) fetchAll(ctx context.Context, need []modFile) error {
	parts := uniqueMD5(need)
	sizes := make([]int64, len(parts))
	known := true
	var room int64
	for i, f := range parts {
		n, err := s.size(ctx, f)
		if err != nil {
			return err
		}
		sizes[i] = n
		if n < 0 {
			known = false
			continue
		}
		if err := s.count(n); err != nil {
			return err
		}
		room += max(n-s.partSize(f), 0)
	}
	if err := s.roomFor(room); err != nil {
		return err
	}
	if known {
		s.total = s.sized
	}
	s.report(true)
	for i, f := range parts {
		if err := s.fetch(ctx, f, sizes[i]); err != nil {
			return fmt.Errorf("downloading %s: %w", f.Path, err)
		}
	}
	s.total = max(s.total, s.done)
	if len(need) == len(parts) {
		return nil
	}
	// The other files of a download's MD5 get copies of it.
	seen := map[string]bool{}
	var copies int64
	for _, f := range need {
		if seen[f.MD5] {
			copies += s.partSize(f)
		}
		seen[f.MD5] = true
	}
	return s.roomFor(copies)
}

// uniqueMD5 is need's first file of each MD5, in order.
func uniqueMD5(need []modFile) []modFile {
	seen := map[string]bool{}
	var out []modFile
	for _, f := range need {
		if !seen[f.MD5] {
			seen[f.MD5] = true
			out = append(out, f)
		}
	}
	return out
}

// place moves the checked downloads into MODDIR and returns their
// manifest entries; a file whose download another file shares gets a copy
// of it.
func (s *syncer) place(need []modFile) ([]manifestFile, error) {
	uses := map[string]int{}
	for _, f := range need {
		uses[f.MD5]++
	}
	var out []manifestFile
	for _, f := range need {
		dst := filepath.Join(s.home, filesRel, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		uses[f.MD5]--
		var err error
		if uses[f.MD5] > 0 {
			err = s.copyPart(f, dst)
		} else {
			err = os.Rename(s.partPath(f), dst)
		}
		if err != nil {
			return nil, err
		}
		fi, err := os.Lstat(dst)
		if err != nil {
			return nil, err
		}
		out = append(out, manifestFile{Path: f.Path, Type: f.Type, MD5: f.MD5, Size: fi.Size(), MTime: fi.ModTime().UnixNano()})
	}
	return out, nil
}

// copyPart copies f's download to dst through a temporary file in
// partial/, which the next sync clears when one is left over.
func (s *syncer) copyPart(f modFile, dst string) error {
	in, err := os.Open(s.partPath(f))
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Join(s.home, partialRel), "copy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, in)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// count adds a file of n bytes to the sizes known, within the bounds.
func (s *syncer) count(n int64) error {
	s.sized += n
	if n > maxFileSize || s.sized > maxTotalSize {
		return s.tooLarge()
	}
	return nil
}

func (s *syncer) tooLarge() error {
	return fmt.Errorf("the TruckersMP files are larger than VaporOS allows (%d MiB)", max(s.sized, s.done)>>20)
}

// roomFor fails with a spaceError when n more bytes and store.ExtReserve
// do not fit in the home data area. Unknown free space passes.
func (s *syncer) roomFor(n int64) error {
	free, err := s.free(s.home)
	if err != nil || free < 0 {
		return nil
	}
	if short := max(n, 0) + store.ExtReserve - free; short > 0 {
		return &spaceError{short: short}
	}
	return nil
}

// partPath is where f downloads to.
func (s *syncer) partPath(f modFile) string {
	return filepath.Join(s.home, partialRel, f.MD5+".part")
}

// partSize is how much of f an earlier sync left in partPath.
func (s *syncer) partSize(f modFile) int64 {
	if fi, err := os.Stat(s.partPath(f)); err == nil && fi.Mode().IsRegular() {
		return fi.Size()
	}
	return 0
}

// downloadURL is where f is served.
func downloadURL(f modFile) string {
	parts := strings.Split(f.Path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return downloadBase + "/" + strings.Join(parts, "/")
}

// size asks for f's size (HEAD); -1 when the server does not say.
func (s *syncer) size(ctx context.Context, f modFile) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL(f), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("TruckersMP lists %s but does not serve it", f.Path)
	}
	if resp.StatusCode != http.StatusOK {
		return -1, nil
	}
	return resp.ContentLength, nil
}

// fetch downloads f into partial/<md5>.part, resuming what an earlier
// sync left there, and checks its MD5.
func (s *syncer) fetch(ctx context.Context, f modFile, size int64) error {
	part := s.partPath(f)
	err := s.download(ctx, f, part, &size)
	if errors.Is(err, errRestart) {
		err = s.download(ctx, f, part, &size)
	}
	if err != nil {
		return err
	}
	sum, err := fileMD5(part)
	if err != nil {
		return err
	}
	if sum != f.MD5 {
		os.Remove(part)
		return fmt.Errorf("its MD5 is %s, TruckersMP says %s", sum, f.MD5)
	}
	return nil
}

// errRestart asks fetch to download the file again from its start.
var errRestart = errors.New("restart the download")

// download appends what the server has beyond part's size (a Range
// request), or starts over when the server sends the whole file. A size
// HEAD did not give (-1) is taken from the GET's answer.
func (s *syncer) download(ctx context.Context, f modFile, part string, size *int64) error {
	var have int64
	exists := false
	if fi, err := os.Stat(part); err == nil && fi.Mode().IsRegular() {
		have, exists = fi.Size(), true
	}
	if *size >= 0 && have > *size {
		os.Remove(part)
		have, exists = 0, false
	}
	s.add(have)
	if exists && *size >= 0 && have == *size {
		return nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(stallTimeout, func() { cancel(fmt.Errorf("no data for %s", stallTimeout)) })
	defer stall.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL(f), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return orCause(ctx, err)
	}
	defer resp.Body.Close()
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	start, total := contentRange(resp)
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 && start == have:
	case resp.StatusCode == http.StatusOK:
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		s.add(-have)
		have, total = 0, resp.ContentLength
	case resp.StatusCode == http.StatusPartialContent, resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// Not the bytes after ours: start over.
		os.Remove(part)
		s.add(-have)
		return errRestart
	default:
		return fmt.Errorf("%s", resp.Status)
	}
	if *size < 0 && total >= 0 {
		*size = total
		if err := s.count(total); err != nil {
			return err
		}
		if err := s.roomFor(total - have); err != nil {
			return err
		}
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	written := have
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(stallTimeout)
			written += int64(n)
			if written > maxFileSize || (*size >= 0 && written > *size) {
				out.Close()
				os.Remove(part)
				return errors.New("the server sent more than the file's size")
			}
			if s.done+int64(n) > maxTotalSize {
				out.Close()
				return s.tooLarge()
			}
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			s.add(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return orCause(ctx, rerr)
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if *size >= 0 && written != *size {
		return fmt.Errorf("got %d of %d bytes", written, *size)
	}
	return nil
}

// contentRange reads a 206 answer's Content-Range ("bytes 100-199/200"):
// its first byte and the whole file's size, -1 for what it does not say.
func contentRange(resp *http.Response) (start, total int64) {
	start, total = -1, -1
	cr, ok := strings.CutPrefix(resp.Header.Get("Content-Range"), "bytes ")
	if !ok {
		return
	}
	span, size, _ := strings.Cut(cr, "/")
	first, _, _ := strings.Cut(span, "-")
	if n, err := strconv.ParseInt(first, 10, 64); err == nil && n >= 0 {
		start = n
	}
	if n, err := strconv.ParseInt(size, 10, 64); err == nil && n >= 0 {
		total = n
	}
	return
}

// orCause prefers why ctx ended (a stalled download) over err.
func orCause(ctx context.Context, err error) error {
	if c := context.Cause(ctx); c != nil && !errors.Is(c, context.Canceled) {
		return c
	}
	return err
}

// cleanUp removes the files an earlier sync wrote that the mod no longer
// has, and downloads left over for files no longer wanted.
func (s *syncer) cleanUp(old *manifest, want []modFile) {
	keep := map[string]bool{}
	sums := map[string]bool{}
	for _, f := range want {
		keep[f.Path] = true
		sums[f.MD5+".part"] = true
	}
	if old != nil {
		for _, f := range old.Files {
			if keep[f.Path] {
				continue
			}
			if _, ok := cleanModPath("/" + f.Path); !ok {
				continue
			}
			p := filepath.Join(s.home, filesRel, filepath.FromSlash(f.Path))
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
				os.Remove(p)
			}
		}
	}
	ents, _ := os.ReadDir(filepath.Join(s.home, partialRel))
	for _, e := range ents {
		if !sums[e.Name()] {
			os.Remove(filepath.Join(s.home, partialRel, e.Name()))
		}
	}
}

func (s *syncer) add(n int64) {
	s.done += n
	s.report(false)
}

// report prints the progress, at most twice a second unless final. A
// total of 0 is one not known yet: a total that grew file by file would
// show the card a percentage that is not real.
func (s *syncer) report(final bool) {
	if s.progress == nil {
		return
	}
	now := s.now()
	if !final && now.Sub(s.last) < 500*time.Millisecond {
		return
	}
	s.last = now
	total := s.total
	if total > 0 {
		total = max(total, s.done)
	}
	fmt.Fprintf(s.progress, "{\"bytes\":%d,\"total\":%d}\n", s.done, total)
}

// sizeText is n bytes for the card, rounded up: GiB with one decimal, or
// whole MiB.
func sizeText(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", math.Ceil(float64(n)*10/(1<<30))/10)
	}
	return fmt.Sprintf("%d MiB", (n+1<<20-1)>>20)
}

// lockSync takes the sync's lock (flock, without waiting).
func lockSync(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errSyncRunning
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
