package truckersmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

func init() {
	extensions.RegisterHelper(ID, newHelper())
	extensions.RegisterCommand(ID, "sync | mp ets2|ats | copy-profiles | ...: TruckersMP's own commands, as the gaming user", cli)
}

// Helper is the extension's helper. vosd calls it as root; whatever
// touches the gaming user's files runs as that user (`vos ext truckersmp
// ...` through runuser, and copy-profiles, which vosd runs through `vos
// ext action` as that user), and only the branch the person asked for is
// root's (the system data area).
type Helper struct {
	mu      sync.Mutex
	job     *syncJob     // the sync that runs, nil when none
	lastRun time.Time    // when the last sync started
	lastFor []string     // the games installed when it started
	lastErr error        // why the last sync failed, nil when it did not
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
	root     func() bool // the helper runs as root (in vosd), not as the gaming user
}

func newHelper() *Helper {
	return &Helper{
		runSync: runSyncAsGamer,
		asGamer: sysd.AsGamer,
		fetchAPI: func(ctx context.Context) (*versionInfo, error) {
			return fetchVersion(ctx, newClient(apiTimeout))
		},
		active: installedAndMounted,
		root:   func() bool { return os.Geteuid() == 0 },
	}
}

// MessageText words the codes `vos ext truckersmp mp` and `handoff` leave
// vosd (extensions.MessageWords).
func (h *Helper) MessageText(code string) (string, bool) { return messageText(code) }

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
		return extensions.Refuse(msgSetup, fmt.Errorf("copying the injector: %v: %s", err, lastLine(out)))
	}
	gs := h.games()
	h.mu.Lock()
	if h.job == nil {
		h.startLocked(gs)
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
		if !h.root() {
			// vosd runs it as the gaming user (`vos ext action`).
			r := copyProfiles(homeDir(), libraries(), os.Stderr)
			for _, c := range r.copied {
				log.Printf("truckersmp: copy-profiles: copied %s", c)
			}
			return r.err()
		}
		out, err := h.asGamer(ctx, vosBin, "ext", ID, "copy-profiles")
		log.Printf("truckersmp: copy-profiles: %s", strings.ReplaceAll(strings.TrimSpace(out), "\n", "; "))
		if err != nil {
			log.Printf("truckersmp: copy-profiles: %v", err)
			return errors.New(lastLine(out))
		}
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
// same start as an entry in Sunshine, and the branches switch-branch asked
// for. Both shortcuts stay whether or not their game is installed: Steam
// keeps a shortcut's id, art and place only while it stays listed.
func (h *Helper) Steam(x *extensions.Ext) extensions.SteamParts {
	p := extensions.SteamParts{Shortcuts: map[string]extensions.ShortcutTarget{}, Beta: steamBeta(x.DataDir)}
	for _, g := range games {
		p.Shortcuts[g.key] = extensions.ShortcutTarget{Exe: vosBin, StartDir: x.HomeDir, Args: mpArgs(g)}
		p.SunshineApps = append(p.SunshineApps, extensions.SunshineApp{
			Name: shortcutName(x, g), Detached: []string{vosBin + " " + strings.Join(mpArgs(g), " ")},
		})
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
