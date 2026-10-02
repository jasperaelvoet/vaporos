package truckersmp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

// branchFile is branch.json: per game, the Steam branch VaporOS asked for
// so the game runs the version TruckersMP supports.
type branchFile struct {
	Apps map[string]branchHold `json:"apps"` // by app id
}

// branchHold is one game's request. Its id stays until a switch changes
// the game's branch: prepare applies each request once, so a branch the
// person picks in Steam afterwards stays theirs. Latest is the newest
// version the game ran before VaporOS held it back: once TruckersMP
// supports that one, no older branch is needed, and none exists for it.
type branchHold struct {
	Branch  string `json:"branch"` // temporary_<major>_<minor>, or "" for the latest version again
	Request string `json:"request"`
	Latest  string `json:"latest"`
}

var (
	branchRe  = regexp.MustCompile(`^temporary_[0-9]{1,4}_[0-9]{1,4}$`)
	requestRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
)

// requestLayout makes a request's id from the switch's time: digits, T
// and a dot, as steam.json's request ids are.
const requestLayout = "20060102T150405.000000000"

// readBranch returns the requests in branch.json, by app id; entries for
// other apps or not so shaped are dropped.
func readBranch(dataDir string) map[uint32]branchHold {
	var b branchFile
	if err := config.ReadJSON(branchPath(dataDir), &b); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("truckersmp: %v", err)
		}
		return nil
	}
	out := map[uint32]branchHold{}
	for k, v := range b.Apps {
		n, err := strconv.ParseUint(k, 10, 32)
		if _, ok := gameByApp(uint32(n)); err != nil || !ok {
			continue
		}
		if (v.Branch == "" || branchRe.MatchString(v.Branch)) && requestRe.MatchString(v.Request) &&
			(v.Latest == "" || gameVersionRe.MatchString(v.Latest)) {
			out[uint32(n)] = v
		}
	}
	return out
}

// steamBeta is the helper's SteamParts.Beta: each request in branch.json.
func steamBeta(dataDir string) map[uint32]extensions.BetaRequest {
	held := readBranch(dataDir)
	if len(held) == 0 {
		return nil
	}
	out := map[uint32]extensions.BetaRequest{}
	for app, b := range held {
		out[app] = extensions.BetaRequest{Branch: b.Branch, Request: b.Request}
	}
	return out
}

// heldVersion is the game version a branch holds ("1.61" of
// temporary_1_61).
func heldVersion(branch string) string {
	return strings.ReplaceAll(strings.TrimPrefix(branch, "temporary_"), "_", ".")
}

// newer is the newer of two game versions by major and minor; either when
// the other is not a version.
func newer(a, b string) string {
	if c, ok := compareMinor(a, b); ok && c < 0 {
		return b
	}
	if _, ok := gameVersion(a); !ok {
		return b
	}
	return a
}

// switchBranch moves each installed game to the version TruckersMP
// supports, through prepare at Steam's next start: a game that runs a
// newer version is held on its branch; a game held on an older branch
// than TruckersMP now supports moves to the new one, or back to its
// latest version when TruckersMP supports that. A game already where it
// should be keeps its request, so its id does not change.
func (h *Helper) switchBranch(ctx context.Context, x *extensions.Ext) error {
	info := h.versionNow(ctx)
	if info == nil {
		return errors.New("Couldn't reach TruckersMP to see which versions it supports. Try again later.")
	}
	held := readBranch(x.DataDir)
	next := branchFile{Apps: map[string]branchHold{}}
	for app, b := range held {
		next.Apps[strconv.FormatUint(uint64(app), 10)] = b
	}
	id := time.Now().UTC().Format(requestLayout)
	libs := libraries()
	var installed []string
	unknown := ""
	changed := false
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
		v := installedVersion(lib, g)
		b, isHeld := held[g.app]
		var hold branchHold
		switch {
		case isHeld && b.Branch == want:
			continue
		case isHeld && b.Branch != "":
			latest := newer(b.Latest, v)
			hold = branchHold{Branch: want, Request: id, Latest: latest}
			if c, ok := compareMinor(sup, latest); !ok || c >= 0 {
				hold.Branch = ""
			}
		case v == "":
			unknown = cmp.Or(unknown, g.short)
			continue
		default:
			// Only a game known to run a newer version: an older one
			// needs Steam's update.
			if c, ok := compareMinor(v, sup); !ok || c <= 0 {
				continue
			}
			hold = branchHold{Branch: want, Request: id, Latest: v}
		}
		next.Apps[strconv.FormatUint(uint64(g.app), 10)] = hold
		changed = true
	}
	switch {
	case len(installed) == 0:
		return errors.New("Neither ETS2 nor ATS is installed in Steam. Install one, then try again.")
	case !changed && unknown != "":
		return fmt.Errorf("Start %s once so VaporOS can see its version, then try again.", unknown)
	case !changed && len(installed) == 1:
		return fmt.Errorf("%s already runs a version TruckersMP supports.", installed[0])
	case !changed:
		return errors.New("ETS2 and ATS already run a version TruckersMP supports.")
	}
	return config.WriteJSONAtomic(branchPath(x.DataDir), next, 0o644)
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
