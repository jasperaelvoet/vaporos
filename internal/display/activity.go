package display

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Where the activity probes look (variables for tests).
var (
	ProcDir = "/proc"
	// SunshineServerInfo answers <state>SUNSHINE_SERVER_BUSY</state> while
	// a client streams; it is local and unauthenticated.
	SunshineServerInfo = "http://127.0.0.1:47989/serverinfo"
)

func steamRoot() string { return filepath.Join(config.GamerHome, ".local", "share", "Steam") }

// gamerBusy reports why gamescope should keep running on a machine with a
// monitor even though nobody streams: a Steam game is running, Steam is
// downloading, or Sunshine still has a client. It is a private copy of the
// reference machine's idle checks (the power package has its own policy).
func gamerBusy(ctx context.Context) (bool, string) {
	if steamGameRunning(ProcDir, config.GamerUID) {
		return true, "a Steam game is running"
	}
	if steamDownloading(steamRoot(), time.Now()) {
		return true, "Steam is downloading"
	}
	if sunshineStreaming(ctx) {
		return true, "Sunshine has an active client"
	}
	return false, ""
}

var steamAppID = regexp.MustCompile(`^SteamAppId=[1-9][0-9]*$`)

// steamGameRunning looks for a process of uid whose environment carries a
// non-zero SteamAppId, which Steam sets for every game it launches.
func steamGameRunning(procDir string, uid int) bool {
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		if procUID(filepath.Join(dir, "status")) != uid {
			continue
		}
		env, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if steamAppID.Match(kv) {
				return true
			}
		}
	}
	return false
}

// procUID returns the real uid from /proc/<pid>/status, or -1.
func procUID(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				n, err := strconv.Atoi(fields[0])
				if err == nil {
					return n
				}
			}
		}
	}
	return -1
}

// steamDownloading reports whether any Steam library has a file in its
// downloading/ or temp/ directory modified in the last 30 seconds.
func steamDownloading(root string, now time.Time) bool {
	cutoff := now.Add(-30 * time.Second)
	for _, lib := range steamLibraries(root) {
		for _, sub := range []string{"downloading", "temp"} {
			if recentFile(filepath.Join(lib, "steamapps", sub), cutoff) {
				return true
			}
		}
	}
	return false
}

var vdfPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamLibraries lists the Steam root plus every library in
// libraryfolders.vdf.
func steamLibraries(root string) []string {
	libs := []string{root}
	b, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	for _, m := range vdfPath.FindAllStringSubmatch(string(b), -1) {
		p := strings.ReplaceAll(m[1], `\\`, `\`)
		if p != root {
			libs = append(libs, p)
		}
	}
	return libs
}

// recentFile walks dir (bounded) for a regular file newer than cutoff.
func recentFile(dir string, cutoff time.Time) bool {
	found := false
	visited := 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if visited++; visited > 20000 {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// sunshineStreaming asks Sunshine's local serverinfo whether a client is
// connected.
func sunshineStreaming(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SunshineServerInfo, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return bytes.Contains(b, []byte("<state>SUNSHINE_SERVER_BUSY</state>"))
}
