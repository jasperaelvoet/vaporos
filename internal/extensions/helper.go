package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// Helper is an extension's own logic, built into vos (internal/extensions/
// <id>). The descriptor says what an extension is; a helper does what a
// descriptor cannot say: download TruckersMP's files, place Star Citizen's
// prefix, copy CoolerControl's password. Embed NopHelper and override only
// what the extension needs. vosd calls every method but LaunchHook as root;
// work in vapor's trees goes through sysd.AsGamer (see CONTRACTS Users).
type Helper interface {
	// Status returns the lines its card shows under the summary. It must be
	// cheap: the control center asks often.
	Status(ctx context.Context, x *Ext) []StatusLine
	// Install runs once the extension is mounted for the first time (and
	// again after Remove), with its data areas made.
	Install(ctx context.Context, x *Ext) error
	// Remove undoes what Install did, while the extension is still mounted;
	// purge also deletes its data areas.
	Remove(ctx context.Context, x *Ext, purge bool) error
	// Action runs one of the descriptor's actions.
	Action(ctx context.Context, x *Ext, name string, args json.RawMessage) error
	// ModuleOptions renders "options <module> <param>=<value>" lines from
	// its settings, for the set's modprobe.conf.
	ModuleOptions(x *Ext) []string
	// Steam returns what the extension adds to Steam beyond its descriptor,
	// such as a shortcut's executable chosen at install.
	Steam(x *Ext) SteamParts
	// LaunchHook may rewrite a Steam launch of one of its hooked apps or
	// shortcuts. It runs as vapor inside `vos ext launch`, never in vosd.
	LaunchHook(ctx context.Context, l *Launch) error
}

// Ext is what a helper gets to work with.
type Ext struct {
	ID       string
	Desc     *descriptor.Descriptor // the shipped descriptor, with its build section
	Settings map[string]any         // current values, by setting key
	DataDir  string                 // its system data area
	HomeDir  string                 // its home data area
}

// StatusLine is one line on the extension's card. Tone is "", "warning" or
// "error".
type StatusLine struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

// SteamParts adds to the descriptor's steam section.
type SteamParts struct {
	// Shortcuts by descriptor key: where the executable is and what to
	// start in, which only the install knows.
	Shortcuts map[string]ShortcutTarget
	// Beta asks Steam to switch an app to a branch ("" for the public one).
	Beta map[uint32]string
	// SunshineApps are entries Sunshine lists after the games, started
	// without a Steam shortcut (TruckersMP's multiplayer start: `vos ext
	// truckersmp mp ets2`). Optional; the shortcuts are listed anyway.
	SunshineApps []SunshineApp
}

// ShortcutTarget is a shortcut's executable and start directory, canonical
// /var/... paths, and the arguments it runs with (TruckersMP's shortcuts
// run `/usr/bin/vos ext truckersmp mp ets2`): plain words, which prepare
// writes after %command% in the shortcut's launch options.
type ShortcutTarget struct {
	Exe      string   `json:"exe"`
	StartDir string   `json:"start_dir"`
	Args     []string `json:"args,omitempty"`
}

// Launch is one Steam launch passing through `vos ext launch`: the app or
// shortcut it is for and the command line Steam built (%command%). Env is
// Steam's, for the game alone: programs a hook starts itself inherit vos's
// own environment, which has no LD_PRELOAD (Steam's overlay). A hook's
// error is shown to the person at the control center, after "<name> did
// not start: ".
type Launch struct {
	App      uint32   // a Steam app id, or 0
	Shortcut string   // "<id>/<key>", or ""
	Argv     []string // what will be exec'd; a hook may rewrite it
	Env      []string // its environment; a hook may add to it
}

// ErrNoAction is what Action returns for a name the extension does not have.
var ErrNoAction = errors.New("this extension has no such action")

// NopHelper does nothing; helpers embed it.
type NopHelper struct{}

func (NopHelper) Status(context.Context, *Ext) []StatusLine                   { return nil }
func (NopHelper) Install(context.Context, *Ext) error                         { return nil }
func (NopHelper) Remove(context.Context, *Ext, bool) error                    { return nil }
func (NopHelper) Action(context.Context, *Ext, string, json.RawMessage) error { return ErrNoAction }
func (NopHelper) ModuleOptions(*Ext) []string                                 { return nil }
func (NopHelper) Steam(*Ext) SteamParts                                       { return SteamParts{} }
func (NopHelper) LaunchHook(context.Context, *Launch) error                   { return nil }

var helpers = map[string]Helper{}

// RegisterHelper adds the helper for extension id; each helper package calls
// it from an init function. An extension without one gets NopHelper.
func RegisterHelper(id string, h Helper) {
	if _, dup := helpers[id]; dup {
		panic("extension helper " + id + " registered twice")
	}
	helpers[id] = h
}

// HelperFor returns id's helper.
func HelperFor(id string) Helper {
	if h, ok := helpers[id]; ok {
		return h
	}
	return NopHelper{}
}

// Busier is a helper with work of its own that keeps the PC awake while
// it runs, such as TruckersMP's download of its mod: Service.Busy asks
// every registered helper that has the method.
type Busier interface {
	Busy() (bool, string)
}

// helpersBusy is the first busy reason of a Busier helper, in id order.
func helpersBusy() (bool, string) {
	ids := make([]string, 0, len(helpers))
	for id := range helpers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if b, ok := helpers[id].(Busier); ok {
			if busy, why := b.Busy(); busy {
				return true, why
			}
		}
	}
	return false, ""
}
