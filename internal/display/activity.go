package display

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gameproc"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// Where the activity probes look (variables for tests).
var (
	ProcDir = "/proc"
	// SunshineServerInfo answers <state>SUNSHINE_SERVER_BUSY</state> while
	// Sunshine runs an app; it is local and unauthenticated.
	SunshineServerInfo = "http://127.0.0.1:47989/serverinfo"
)

// Everything below reads, as root, files that Steam and every game (the
// gaming user) control. Such reads never follow a symlink the user
// planted, never block on a FIFO and never read without a bound.
const (
	// steamRootRel is the Steam root, relative to the gaming user's home.
	steamRootRel = ".local/share/Steam"
	// maxVDF bounds libraryfolders.vdf (as internal/storage/steam does).
	maxVDF = 4 << 20
)

// gamerBusy reports why gamescope should keep running on a machine with a
// monitor even though nobody streams: a Steam game is running, Steam is
// downloading, or Sunshine still has a client. It is a private copy of the
// reference machine's idle checks (the power package has its own policy).
func gamerBusy(ctx context.Context) (bool, string) {
	if steamGameRunning() {
		return true, "a Steam game is running"
	}
	if steamDownloading(config.GamerHome, time.Now()) {
		return true, "Steam is downloading"
	}
	if sunshineStreaming(ctx) {
		return true, "Sunshine has an active client"
	}
	return false, ""
}

// gamerProbe looks at the gaming user's processes through the probe idle
// shutdown shares (internal/gameproc).
func gamerProbe() gameproc.Probe {
	return gameproc.Probe{ProcDir: ProcDir, UID: config.GamerUID, RuntimeDir: UserRuntimeDir}
}

// steamGameRunning reports whether a Steam game runs as the gaming user.
func steamGameRunning() bool { return gamerProbe().GameRunning() }

// steamDownloading reports whether any Steam library has a file in its
// downloading/ or temp/ directory modified in the last 30 seconds.
func steamDownloading(home string, now time.Time) bool {
	cutoff := now.Add(-30 * time.Second)
	for _, lib := range steamLibraries(home) {
		for _, sub := range []string{"downloading", "temp"} {
			if recentFile(filepath.Join(lib, "steamapps", sub), cutoff) {
				return true
			}
		}
	}
	return false
}

var vdfPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamLibraries lists the Steam root in home plus every library in its
// libraryfolders.vdf.
func steamLibraries(home string) []string {
	root := filepath.Join(home, steamRootRel)
	libs := []string{root}
	b, err := gamerfs.ReadFile(home, steamRootRel+"/steamapps/libraryfolders.vdf", maxVDF)
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

// recentFile walks dir (bounded) for a regular file newer than cutoff. It
// only lists directories and stats entries; it opens no file.
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

// sunshineStreaming asks Sunshine's local serverinfo whether it runs an
// app (a client streams, or one left it running).
func sunshineStreaming(ctx context.Context) bool {
	busy, _ := sunshineState(ctx)
	return busy
}

// sunshineState reads Sunshine's serverinfo state: busy when it runs an
// app. ok is false unless Sunshine answered with a state at all.
func sunshineState(ctx context.Context) (busy, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SunshineServerInfo, nil)
	if err != nil {
		return false, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case bytes.Contains(b, []byte("<state>SUNSHINE_SERVER_BUSY</state>")):
		return true, true
	case bytes.Contains(b, []byte("<state>SUNSHINE_SERVER_FREE</state>")):
		return false, true
	}
	return false, false
}
