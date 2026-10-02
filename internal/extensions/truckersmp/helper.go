package truckersmp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

func init() {
	extensions.RegisterHelper(ID, newHelper())
	extensions.RegisterCommand(ID, "sync | mp ets2|ats | copy-profiles | ...: TruckersMP's own commands, as the gaming user", cli)
}

// Helper is the extension's helper. vosd calls it as root; whatever
// touches the gaming user's files runs as that user (`vos ext truckersmp
// ...` through runuser), and only the branch the person asked for is
// root's (the system data area).
type Helper struct {
	mu      sync.Mutex
	job     *syncJob     // the sync that runs, nil when none
	lastRun time.Time    // when the last sync started
	lastErr string       // why the last sync failed, "" when it did not
	api     *versionInfo // the version API's last answer
	apiAt   time.Time    // when it was last asked, answer or not
	asking  bool         // a question to the API is under way
	seen    []gameState  // the games, as last looked at
	seenAt  time.Time

	// Seams; newHelper wires the real ones.
	runSync  func(ctx context.Context, progress func(done, total int64)) error
	asGamer  func(ctx context.Context, name string, args ...string) (string, error)
	fetchAPI func(ctx context.Context) (*versionInfo, error)
	active   func() bool // this boot mounted the extension and its Install finished
}

func newHelper() *Helper {
	return &Helper{
		runSync: runSyncAsGamer,
		asGamer: sysd.AsGamer,
		fetchAPI: func(ctx context.Context) (*versionInfo, error) {
			return fetchVersion(ctx, newClient(apiTimeout))
		},
		active: installedAndMounted,
	}
}

// How often vosd looks again. Variables for tests.
var (
	apiEvery   = time.Hour // the version API, at most
	apiRetry   = 10 * time.Minute
	apiTimeout = 15 * time.Second
	syncGap    = time.Hour      // between two syncs vosd starts by itself
	syncEvery  = 24 * time.Hour // a sync even when nothing changed
	gamesEvery = 30 * time.Second
	busyStall  = 2 * time.Minute
)

// busyReason is what keeps the PC awake while a sync downloads.
const busyReason = "updating TruckersMP"

// ModuleOptions: TruckersMP sets no kernel module options.
func (h *Helper) ModuleOptions(*extensions.Ext) []string { return nil }

// Install copies the injector into the home data area and starts the
// first download of the mod in the background: it takes long (about 640
// MiB), and the card shows how far it is.
func (h *Helper) Install(ctx context.Context, x *extensions.Ext) error {
	if out, err := h.asGamer(ctx, vosBin, "ext", ID, "setup"); err != nil {
		log.Printf("truckersmp: setup: %v: %s", err, lastLine(out))
		return errors.New("its launcher could not be copied. Try again.")
	}
	h.mu.Lock()
	if h.job == nil {
		h.startLocked()
	}
	h.mu.Unlock()
	return nil
}

// Remove stops a sync that runs (purging deletes the files it writes)
// and forgets the branch: Steam gets its own back from prepare.
func (h *Helper) Remove(ctx context.Context, x *extensions.Ext, purge bool) error {
	h.stopSync(10 * time.Second)
	if err := os.Remove(branchPath(x.DataDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Action runs the card's actions.
func (h *Helper) Action(ctx context.Context, x *extensions.Ext, name string, _ json.RawMessage) error {
	switch name {
	case "copy-profiles":
		out, err := h.asGamer(ctx, vosBin, "ext", ID, "copy-profiles")
		if err != nil {
			log.Printf("truckersmp: copy-profiles: %v", err)
			return errors.New(lastLine(out))
		}
		log.Printf("truckersmp: copy-profiles: %s", strings.ReplaceAll(out, "\n", "; "))
		return nil
	case "switch-branch":
		return h.switchBranch(ctx, x)
	case "latest-branch":
		if err := os.Remove(branchPath(x.DataDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return extensions.ErrNoAction
}

// lastLine is a command's last line of output: `vos ext truckersmp`
// prints its error there.
func lastLine(out string) string {
	out = strings.TrimSpace(out)
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	if out == "" {
		return "it failed"
	}
	return out
}

// Steam: a shortcut per game, which runs vos (no compatibility tool), the
// same start as an entry in Sunshine, and the branch the person asked for.
func (h *Helper) Steam(x *extensions.Ext) extensions.SteamParts {
	p := extensions.SteamParts{Shortcuts: map[string]extensions.ShortcutTarget{}}
	for _, g := range games {
		p.Shortcuts[g.key] = extensions.ShortcutTarget{Exe: vosBin, StartDir: x.HomeDir, Args: mpArgs(g)}
		p.SunshineApps = append(p.SunshineApps, extensions.SunshineApp{
			Name: shortcutName(x, g), Detached: []string{vosBin + " " + strings.Join(mpArgs(g), " ")},
		})
	}
	if b := readBranch(x.DataDir); len(b) > 0 {
		p.Beta = b
	}
	return p
}

// shortcutName is the descriptor's name for g's shortcut: Sunshine's
// entry takes it, so it stands for the shortcut there.
func shortcutName(x *extensions.Ext, g game) string {
	if x.Desc != nil && x.Desc.Steam != nil {
		for _, sc := range x.Desc.Steam.Shortcuts {
			if sc.Key == g.key {
				return sc.Name
			}
		}
	}
	return "TruckersMP (" + g.short + ")"
}

// branchRequest is branch.json: the Steam branch each game stays on
// because the person chose the version TruckersMP supports.
type branchRequest struct {
	At   time.Time         `json:"at"`
	Apps map[string]string `json:"apps"` // app id → branch
}

var branchRe = regexp.MustCompile(`^temporary_[0-9]{1,4}_[0-9]{1,4}$`)

// readBranch returns the branches asked for, by app id.
func readBranch(dataDir string) map[uint32]string {
	var b branchRequest
	if err := config.ReadJSON(branchPath(dataDir), &b); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("truckersmp: %v", err)
		}
		return nil
	}
	out := map[uint32]string{}
	for k, v := range b.Apps {
		n, err := strconv.ParseUint(k, 10, 32)
		if _, ok := gameByApp(uint32(n)); err == nil && ok && branchRe.MatchString(v) {
			out[uint32(n)] = v
		}
	}
	return out
}

// switchBranch asks Steam, through prepare at its next start, for the
// branch of the version TruckersMP supports, for each installed game
// that runs a newer one. A game already held there stays.
func (h *Helper) switchBranch(ctx context.Context, x *extensions.Ext) error {
	info := h.versionNow(ctx)
	if info == nil {
		return errors.New("Couldn't reach TruckersMP to see which versions it supports. Try again later.")
	}
	held := readBranch(x.DataDir)
	req := branchRequest{At: time.Now().UTC(), Apps: map[string]string{}}
	libs := libraries()
	var installed []string
	unknown := ""
	for _, g := range games {
		lib := gameLibrary(libs, g)
		if lib == "" {
			continue
		}
		installed = append(installed, g.short)
		sup := info.supported(g)
		want, ok := branchFor(sup)
		if !ok {
			continue
		}
		key := strconv.FormatUint(uint64(g.app), 10)
		if held[g.app] == want {
			req.Apps[key] = want
			continue
		}
		// Only a game known to run a newer version: an older one needs
		// Steam's update, and a branch for the current version does not
		// exist.
		v := installedVersion(lib, g)
		if v == "" {
			unknown = cmp.Or(unknown, g.short)
			continue
		}
		if c, ok := compareMinor(v, sup); ok && c > 0 {
			req.Apps[key] = want
		}
	}
	switch {
	case len(installed) == 0:
		return errors.New("Neither ETS2 nor ATS is installed in Steam. Install one, then try again.")
	case len(req.Apps) == 0 && unknown != "":
		return fmt.Errorf("Start %s once so VaporOS can see its version, then try again.", unknown)
	case len(req.Apps) == 0 && len(installed) == 1:
		return fmt.Errorf("%s already runs a version TruckersMP supports.", installed[0])
	case len(req.Apps) == 0:
		return errors.New("ETS2 and ATS already run a version TruckersMP supports.")
	}
	return config.WriteJSONAtomic(branchPath(x.DataDir), req, 0o644)
}

// versionNow is the version API's answer, asked now unless the last one
// is fresh.
func (h *Helper) versionNow(ctx context.Context) *versionInfo {
	h.mu.Lock()
	info, fresh := h.api, time.Since(h.apiAt) < apiEvery && h.api != nil
	h.mu.Unlock()
	if fresh {
		return info
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	v, err := h.fetchAPI(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiAt = time.Now()
	if err != nil {
		log.Printf("truckersmp: version API: %v", err)
		return h.api
	}
	h.api = v
	return v
}
