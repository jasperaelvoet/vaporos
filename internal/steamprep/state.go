package steamprep

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// State is prepare's own record (~/.local/state/vaporos/steam.json): what
// VaporOS wrote into Steam's files and what was there before, so it
// changes only what it owns and can give the rest back. Only `vos steam
// prepare` writes it; vosd reads it through gamerfs, for display only.
type State struct {
	Fingerprint string                               `json:"fingerprint"`
	Vos         string                               `json:"vos"`
	Accounts    []string                             `json:"accounts"`
	Default     Mapping                              `json:"default"`
	Apps        map[string]*AppState                 `json:"apps"`
	Shortcuts   map[string]map[string]*ShortcutState `json:"shortcuts"`
	// Skipped says why the last run changed nothing (skip* below), ""
	// when it went ahead.
	Skipped string `json:"skipped"`
	Error   string `json:"error"`
}

// Why a run changed nothing, in the record's skipped.
const (
	skipSteamRunning = "steam-running"
	skipNoDesired    = "no-steam-json"
	skipBadDesired   = "bad-steam-json"
	skipOtherSet     = "other-set"
)

// Mapping is one CompatToolMapping entry VaporOS owns: the tool it wrote
// (empty: it owns none), the entry before it (nil: there was none), and
// whether it put that back for now (Suspended).
type Mapping struct {
	Wrote     string            `json:"wrote"`
	Before    *steam.CompatTool `json:"before"`
	Suspended bool              `json:"suspended"`
}

func (m *Mapping) owned() bool { return m != nil && m.Wrote != "" }

// AppState is what VaporOS did to one app (or shortcut app id).
type AppState struct {
	Mapping *Mapping                `json:"mapping,omitempty"`
	Launch  map[string]*LaunchState `json:"launch,omitempty"` // by account id
	Beta    *BetaState              `json:"beta,omitempty"`
}

func (a *AppState) empty() bool {
	return a == nil || (a.Mapping == nil && len(a.Launch) == 0 && a.Beta == nil)
}

// LaunchState is an account's launch options for an app: what VaporOS
// wrote and the user's own options. Conflict: the user's options hold
// several %command%, so they were left alone.
type LaunchState struct {
	Wrote    string `json:"wrote"`
	Before   string `json:"before"`
	Conflict bool   `json:"conflict,omitempty"`
}

// BetaState is the branch VaporOS asked Steam for, the one before (""
// is the public branch) and the id of the request it applied.
type BetaState struct {
	Wrote   string `json:"wrote"`
	Before  string `json:"before"`
	Request string `json:"request,omitempty"`
}

// ShortcutState is one of an account's VaporOS shortcuts: the app id
// Steam keeps for it and the game id steam://rungameid/ takes. Deleted:
// the user removed it in Steam, and it is not added again. Art is the
// grid files VaporOS wrote for it, by name.
type ShortcutState struct {
	AppID   uint32             `json:"appid"`
	GameID  string             `json:"gameid"`
	Deleted bool               `json:"deleted"`
	Art     map[string]ArtFile `json:"art,omitempty"`
}

// ArtFile is a grid file as VaporOS wrote it: its size and mtime (Unix
// nanoseconds). It goes with its shortcut only while it is still so.
type ArtFile struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"`
}

func newShortcutState(appid uint32) *ShortcutState {
	return &ShortcutState{AppID: appid, GameID: strconv.FormatUint(steam.GameID(appid), 10)}
}

// carried is the record of a shortcut found again with app id appid: the
// art VaporOS wrote for it stays recorded, whatever app id it has now.
func carried(prev *ShortcutState, appid uint32) *ShortcutState {
	ss := newShortcutState(appid)
	if prev != nil {
		ss.Art = maps.Clone(prev.Art)
	}
	return ss
}

func (ss *ShortcutState) clone() *ShortcutState {
	c := *ss
	c.Art = maps.Clone(ss.Art)
	return &c
}

// StatePath is the record's place in the gaming user's home.
func StatePath(home string) string { return filepath.Join(home, config.ExtGamerStateFile) }

const maxState = 4 << 20

// loadState reads the record. A missing or unreadable one is an empty
// record: what VaporOS wrote is then recognised by its values.
func loadState(path string) (*State, error) {
	st := &State{}
	data, err := readRegular(path, maxState)
	if err == nil {
		err = json.Unmarshal(data, st)
	}
	if err != nil {
		st = &State{}
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	}
	if st.Apps == nil {
		st.Apps = map[string]*AppState{}
	}
	if st.Shortcuts == nil {
		st.Shortcuts = map[string]map[string]*ShortcutState{}
	}
	return st, err
}

func (st *State) save(path string) error {
	for k, a := range st.Apps {
		if a.empty() {
			delete(st.Apps, k)
		}
	}
	for k, m := range st.Shortcuts {
		if len(m) == 0 {
			delete(st.Shortcuts, k)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.WriteJSONAtomic(path, st, 0o644)
}

// app returns the record of app id, made when missing.
func (st *State) app(id uint32) *AppState {
	k := strconv.FormatUint(uint64(id), 10)
	a := st.Apps[k]
	if a == nil {
		a = &AppState{}
		st.Apps[k] = a
	}
	return a
}

// peekApp returns the record of app id, or nil.
func (st *State) peekApp(id uint32) *AppState {
	return st.Apps[strconv.FormatUint(uint64(id), 10)]
}
