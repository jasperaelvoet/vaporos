package starcitizen

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// feedURL is electron-builder's update feed for the RSI Launcher; the
// installer it names sits next to it.
var feedURL = "https://install.robertsspaceindustries.com/rel/2/latest.yml"

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("redirected away from https")
		}
		return nil
	},
}

const (
	maxFeed      = 64 << 10
	maxInstaller = 4 << 30
	fetchTries   = 3
)

var (
	installerRe = regexp.MustCompile(`^RSI Launcher-Setup-[0-9A-Za-z._+-]{1,40}\.exe$`)
	versionRe   = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,40}$`)
)

// release is the installer the feed names.
type release struct {
	Version string
	File    string // RSI Launcher-Setup-<version>.exe
	SHA512  []byte
	Size    int64
}

// parseFeed reads latest.yml, a small YAML file electron-builder writes:
//
//	version: 2.17.0
//	files:
//	  - url: RSI Launcher-Setup-2.17.0.exe
//	    sha512: <base64>
//	    size: 342574256
//	path: RSI Launcher-Setup-2.17.0.exe
//	sha512: <base64>
//	releaseDate: '2025-09-30T17:04:52.513Z'
//
// It reads only that shape: top-level keys, and the keys of the files list.
func parseFeed(b []byte) (release, error) {
	top := map[string]string{}
	var files []map[string]string
	inFiles := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '-' {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				return release{}, fmt.Errorf("line %q is not key: value", line)
			}
			k = strings.TrimSpace(k)
			inFiles = k == "files"
			if !inFiles {
				top[k] = unquote(strings.TrimSpace(v))
			}
			continue
		}
		if !inFiles {
			continue // a nested value of another key
		}
		if rest, ok := strings.CutPrefix(trimmed, "- "); ok {
			files = append(files, map[string]string{})
			trimmed = strings.TrimSpace(rest)
		}
		if len(files) == 0 {
			continue
		}
		if k, v, ok := strings.Cut(trimmed, ":"); ok {
			files[len(files)-1][strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
		}
	}
	if err := sc.Err(); err != nil {
		return release{}, err
	}

	r := release{Version: top["version"], File: top["path"]}
	var entry map[string]string
	for _, f := range files {
		if r.File == "" || f["url"] == r.File {
			entry = f
			break
		}
	}
	if entry == nil {
		return release{}, errors.New("the feed lists no installer")
	}
	if r.File == "" {
		r.File = entry["url"]
	}
	sum := entry["sha512"]
	if sum == "" {
		sum = top["sha512"]
	}
	switch {
	case !versionRe.MatchString(r.Version):
		return release{}, fmt.Errorf("version %q is not a version", r.Version)
	case !installerRe.MatchString(r.File):
		return release{}, fmt.Errorf("installer %q is not the RSI Launcher's setup", r.File)
	}
	var err error
	if r.SHA512, err = base64.StdEncoding.DecodeString(sum); err != nil || len(r.SHA512) != sha512.Size {
		return release{}, fmt.Errorf("sha512 %q is not a base64 SHA-512", sum)
	}
	if r.Size, err = strconv.ParseInt(entry["size"], 10, 64); err != nil || r.Size <= 0 || r.Size > maxInstaller {
		return release{}, fmt.Errorf("size %q is not a size", entry["size"])
	}
	return r, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		case v[0] == '"' && v[len(v)-1] == '"':
			if s, err := strconv.Unquote(v); err == nil {
				return s
			}
		}
	}
	return v
}

// installerURL is where the feed's installer is: next to the feed.
func installerURL(file string) string {
	return feedURL[:strings.LastIndex(feedURL, "/")+1] + url.PathEscape(file)
}

func readFeed(ctx context.Context) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return release{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("%s: %s", feedURL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFeed+1))
	if err != nil {
		return release{}, err
	}
	if len(b) > maxFeed {
		return release{}, fmt.Errorf("%s: larger than %d bytes", feedURL, maxFeed)
	}
	return parseFeed(b)
}

// errMismatch is a download whose bytes are not the ones the feed lists.
var errMismatch = errors.New("the installer is not the file its publisher lists")

// checkFile reports whether the file at p has r's size and SHA-512.
func checkFile(p string, r release) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != r.Size {
		return false, err
	}
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(h.Sum(nil), r.SHA512) == 1, nil
}

// fetchInstaller puts r's installer into dir, resuming a download that
// stopped (<file>.part) and checking size and SHA-512 before the file gets
// its name. Partial downloads go once it is there; other installers stay
// for the launch hook, since Steam's shortcut may name one until Steam
// picks up the new target.
func fetchInstaller(ctx context.Context, r release, dir string) error {
	final := filepath.Join(dir, r.File)
	if ok, _ := checkFile(final, r); ok {
		now := time.Now()
		os.Chtimes(final, now, now) // the newest, for the launch hook
	} else {
		part := final + ".part"
		var err error
		for try := 0; try < fetchTries; try++ {
			if err = download(ctx, installerURL(r.File), part, r.Size); err == nil || ctx.Err() != nil {
				break
			}
		}
		if err != nil {
			return err
		}
		ok, err := checkFile(part, r)
		if err != nil {
			return err
		}
		if !ok {
			os.Remove(part)
			return errMismatch
		}
		if err := os.Rename(part, final); err != nil {
			return err
		}
		if d, err := os.Open(dir); err == nil {
			d.Sync()
			d.Close()
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "RSI Launcher-Setup-") && strings.HasSuffix(n, ".exe.part") {
			os.Remove(filepath.Join(dir, n))
		}
	}
	return nil
}

// download brings part up to size bytes from u, asking only for the bytes
// it lacks.
func download(ctx context.Context, u, part string, size int64) error {
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	have := fi.Size()
	if have > size {
		have = 0
	}
	if have == size {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 && rangeStart(resp.Header.Get("Content-Range")) == have:
	case resp.StatusCode == http.StatusOK:
		have = 0
	default:
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			f.Truncate(0) // start over next time
		}
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	if err := f.Truncate(have); err != nil {
		return err
	}
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, size-have+1))
	if err != nil {
		f.Sync()
		return err
	}
	if have+n != size {
		return fmt.Errorf("%s: %d bytes, not %d", u, have+n, size)
	}
	return f.Sync()
}

// rangeStart reads the first byte of "bytes A-B/N", -1 when it is not one.
func rangeStart(cr string) int64 {
	rest, ok := strings.CutPrefix(cr, "bytes ")
	if !ok {
		return -1
	}
	a, _, ok := strings.Cut(rest, "-")
	if !ok {
		return -1
	}
	n, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
