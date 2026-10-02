package truckersmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
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

type syncer struct {
	home     string          // the home data area
	client   *http.Client    // TruckersMP's servers
	progress io.Writer       // {"bytes","total"} lines
	games    func() []string // the games installed now
	now      func() time.Time

	done, total int64
	last        time.Time // when progress was last printed
}

func newSyncer(progress io.Writer) *syncer {
	return &syncer{
		home: homeDir(), client: newClient(0), progress: progress,
		games: func() []string { return installedGames(libraries()) },
		now:   time.Now,
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
		if sum := info.coreMD5(g); sum != "" && want[i].MD5 != sum {
			return fmt.Errorf("files.json and the version API give %s different checksums; trying again later", g.coreDLL)
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
		// From here on the files change: no manifest until they are whole.
		if err := os.Remove(filepath.Join(s.home, manifestRel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := s.fetchAll(ctx, need, next); err != nil {
			return err
		}
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

// fetchAll downloads need, sizing the whole first so progress has a total.
func (s *syncer) fetchAll(ctx context.Context, need []modFile, next *manifest) error {
	sizes := make([]int64, len(need))
	for i, f := range need {
		n, err := s.size(ctx, f)
		if err != nil {
			return err
		}
		sizes[i] = n
		if n > 0 {
			s.total += n
		}
		if n > maxFileSize || s.total > maxTotalSize {
			return fmt.Errorf("the TruckersMP files are larger than VaporOS allows (%d MiB)", s.total>>20)
		}
	}
	s.report(true)
	for i, f := range need {
		e, err := s.fetch(ctx, f, sizes[i])
		if err != nil {
			return fmt.Errorf("downloading %s: %w", f.Path, err)
		}
		next.Files = append(next.Files, e)
	}
	return nil
}

// downloadURL is where f is served.
func downloadURL(f modFile) string {
	parts := strings.Split(f.Path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return downloadBase + "/" + strings.Join(parts, "/")
}

// size asks for f's size; -1 when the server does not say.
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
// sync left there, checks its MD5 and moves it into MODDIR.
func (s *syncer) fetch(ctx context.Context, f modFile, size int64) (manifestFile, error) {
	part := filepath.Join(s.home, partialRel, f.MD5+".part")
	err := s.download(ctx, f, part, size)
	if errors.Is(err, errRestart) {
		err = s.download(ctx, f, part, size)
	}
	if err != nil {
		return manifestFile{}, err
	}
	sum, err := fileMD5(part)
	if err != nil {
		return manifestFile{}, err
	}
	if sum != f.MD5 {
		os.Remove(part)
		return manifestFile{}, fmt.Errorf("its MD5 is %s, TruckersMP says %s", sum, f.MD5)
	}
	dst := filepath.Join(s.home, filesRel, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return manifestFile{}, err
	}
	if err := os.Rename(part, dst); err != nil {
		return manifestFile{}, err
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		return manifestFile{}, err
	}
	return manifestFile{Path: f.Path, Type: f.Type, MD5: f.MD5, Size: fi.Size(), MTime: fi.ModTime().UnixNano()}, nil
}

// errRestart asks fetch to download the file again from its start.
var errRestart = errors.New("restart the download")

// download appends what the server has beyond part's size (a Range
// request), or starts over when the server sends the whole file.
func (s *syncer) download(ctx context.Context, f modFile, part string, size int64) error {
	var have int64
	if fi, err := os.Stat(part); err == nil && fi.Mode().IsRegular() {
		have = fi.Size()
	}
	if size >= 0 && have > size {
		os.Remove(part)
		have = 0
	}
	s.add(have)
	if size >= 0 && have == size {
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
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 && rangeStart(resp) == have:
	case resp.StatusCode == http.StatusOK:
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		s.add(-have)
		have = 0
	case resp.StatusCode == http.StatusPartialContent, resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// Not the bytes after ours: start over.
		os.Remove(part)
		s.add(-have)
		return errRestart
	default:
		return fmt.Errorf("%s", resp.Status)
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
			if written > maxFileSize || (size >= 0 && written > size) {
				out.Close()
				os.Remove(part)
				return errors.New("the server sent more than the file's size")
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
	if size >= 0 && written != size {
		return fmt.Errorf("got %d of %d bytes", written, size)
	}
	return nil
}

// rangeStart is the first byte a 206 answer holds ("bytes 100-199/200").
func rangeStart(resp *http.Response) int64 {
	cr, ok := strings.CutPrefix(resp.Header.Get("Content-Range"), "bytes ")
	if !ok {
		return -1
	}
	first, _, _ := strings.Cut(cr, "-")
	n, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return -1
	}
	return n
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

// report prints the progress, at most twice a second unless final.
func (s *syncer) report(final bool) {
	if s.progress == nil {
		return
	}
	now := s.now()
	if !final && now.Sub(s.last) < 500*time.Millisecond {
		return
	}
	s.last = now
	total := max(s.total, s.done)
	fmt.Fprintf(s.progress, "{\"bytes\":%d,\"total\":%d}\n", s.done, total)
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
