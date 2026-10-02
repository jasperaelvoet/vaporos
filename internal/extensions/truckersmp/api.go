package truckersmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TruckersMP's own endpoints. Variables for tests.
var (
	versionURL   = "https://api.truckersmp.com/v2/version"
	filesURL     = "https://update.ets2mp.com/files.json"
	downloadBase = "https://download.ets2mp.com/files" // + a file's FilePath
)

// Bounds on what TruckersMP's servers send.
const (
	maxVersionBody = 64 << 10
	maxFilesBody   = 4 << 20
	maxFiles       = 10000
)

// versionInfo is what https://api.truckersmp.com/v2/version says about
// the current mod: its version, its core libraries' MD5 and the game
// versions it supports.
type versionInfo struct {
	Name          string   `json:"name"`
	ETS2          checksum `json:"ets2mp_checksum"`
	ATS           checksum `json:"atsmp_checksum"`
	SupportedETS2 string   `json:"supported_game_version"`
	SupportedATS  string   `json:"supported_ats_game_version"`
}

type checksum struct {
	DLL string `json:"dll"`
}

// supported is the game version TruckersMP supports for g, "" when unknown.
func (v *versionInfo) supported(g game) string {
	if v == nil {
		return ""
	}
	if g.key == "ats" {
		return v.SupportedATS
	}
	return v.SupportedETS2
}

// coreMD5 is the API's MD5 of g's core library, "" when unknown.
func (v *versionInfo) coreMD5(g game) string {
	if g.key == "ats" {
		return v.ATS.DLL
	}
	return v.ETS2.DLL
}

var (
	modVersionRe  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$`)
	gameVersionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}[a-z]{0,3}$`)
	md5Re         = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// parseVersion checks the API's answer. Its values reach the card and the
// branch Steam is asked for, so anything not shaped like a version is
// dropped.
func parseVersion(b []byte) (*versionInfo, error) {
	var v versionInfo
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("version API: %w", err)
	}
	if !modVersionRe.MatchString(v.Name) {
		return nil, fmt.Errorf("version API: %q is not a version", v.Name)
	}
	for _, s := range []*string{&v.SupportedETS2, &v.SupportedATS} {
		if !gameVersionRe.MatchString(*s) {
			*s = ""
		}
	}
	for _, s := range []*string{&v.ETS2.DLL, &v.ATS.DLL} {
		if *s = strings.ToLower(*s); !md5Re.MatchString(*s) {
			*s = ""
		}
	}
	return &v, nil
}

// modFile is one file of the mod as files.json lists it.
type modFile struct {
	Path string // relative to MODDIR, slash-separated
	Type string // "system", "ets2" or "ats"
	MD5  string // lower-case hex
}

// parseFiles reads https://update.ets2mp.com/files.json:
// {"Files":[{"Md5","Type","FilePath":"/core_ets2mp.dll"},…]}. Files of
// other types are skipped; a path that would leave MODDIR fails it whole.
func parseFiles(b []byte) ([]modFile, error) {
	var raw struct {
		Files []struct {
			MD5      string `json:"Md5"`
			Type     string `json:"Type"`
			FilePath string `json:"FilePath"`
		} `json:"Files"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("files.json: %w", err)
	}
	if len(raw.Files) == 0 || len(raw.Files) > maxFiles {
		return nil, fmt.Errorf("files.json lists %d files", len(raw.Files))
	}
	seen := map[string]bool{}
	var out []modFile
	for _, f := range raw.Files {
		if f.Type != "system" && f.Type != "ets2" && f.Type != "ats" {
			continue
		}
		rel, ok := cleanModPath(f.FilePath)
		if !ok {
			return nil, fmt.Errorf("files.json: %q is not a path inside the mod's folder", f.FilePath)
		}
		sum := strings.ToLower(f.MD5)
		if !md5Re.MatchString(sum) {
			return nil, fmt.Errorf("files.json: %s has no MD5", rel)
		}
		if seen[strings.ToLower(rel)] {
			return nil, fmt.Errorf("files.json lists %s twice", rel)
		}
		seen[strings.ToLower(rel)] = true
		out = append(out, modFile{Path: rel, Type: f.Type, MD5: sum})
	}
	return out, nil
}

// cleanModPath turns a FilePath ("/data/x.zip") into a clean relative path
// that stays inside MODDIR and is not one of the sync's own names.
func cleanModPath(p string) (string, bool) {
	rel, ok := strings.CutPrefix(p, "/")
	if !ok || rel == "" || len(rel) > 512 || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") || rel == ".." {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || strings.HasPrefix(part, ".") {
			return "", false
		}
	}
	return rel, !strings.ContainsFunc(rel, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' || r == ':' })
}

// newClient is an HTTP client that gives up on a server that does not
// answer: headers within 30 s, and never from https down to http.
func newClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != via[0].URL.Scheme {
			return fmt.Errorf("redirected from %s to %s", via[0].URL.Scheme, req.URL.Scheme)
		}
		return nil
	}}
}

// getSmall fetches url's body, at most max bytes.
func getSmall(ctx context.Context, c *http.Client, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, max)
	}
	return b, nil
}

// userAgent names VaporOS to TruckersMP's servers.
const userAgent = "VaporOS-truckersmp/1"

// fetchVersion asks the version API.
func fetchVersion(ctx context.Context, c *http.Client) (*versionInfo, error) {
	b, err := getSmall(ctx, c, versionURL, maxVersionBody)
	if err != nil {
		return nil, err
	}
	return parseVersion(b)
}

// gameVersion splits "1.61.1.1s" into its numbers; ok is false for
// anything else.
func gameVersion(s string) (nums []int, ok bool) {
	s = strings.TrimRight(s, "abcdefghijklmnopqrstuvwxyz")
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 9999 {
			return nil, false
		}
		nums = append(nums, n)
	}
	return nums, len(nums) >= 2
}

// compareMinor compares two game versions by major and minor only, which
// is what TruckersMP checks and what Steam's branches name: -1, 0 or 1,
// and false when either is not a version.
func compareMinor(a, b string) (int, bool) {
	x, ok1 := gameVersion(a)
	y, ok2 := gameVersion(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := 0; i < 2; i++ {
		switch {
		case x[i] < y[i]:
			return -1, true
		case x[i] > y[i]:
			return 1, true
		}
	}
	return 0, true
}

// minorOf is "1.61" of "1.61.1.1s".
func minorOf(v string) string {
	n, ok := gameVersion(v)
	if !ok {
		return v
	}
	return strconv.Itoa(n[0]) + "." + strconv.Itoa(n[1])
}

// branchFor is the Steam branch that holds game version v:
// temporary_<major>_<minor>.
func branchFor(v string) (string, bool) {
	n, ok := gameVersion(v)
	if !ok {
		return "", false
	}
	return "temporary_" + strconv.Itoa(n[0]) + "_" + strconv.Itoa(n[1]), true
}
