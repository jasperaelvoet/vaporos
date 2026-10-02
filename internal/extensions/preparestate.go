package extensions

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// maxSteamState bounds prepare's record and Steam's account list; real
// ones are a few KiB.
const maxSteamState = 1 << 20

// loginUsersRel is Steam's account list, relative to the gaming user's home.
const loginUsersRel = ".local/share/Steam/config/loginusers.vdf"

// prepareState is the part of `vos steam prepare`'s own record
// (~/.local/state/vaporos/steam.json) that vosd and the dispatcher read.
// It belongs to the gaming user: vosd reads it through gamerfs and uses it
// only to show shortcuts and to decide on a Steam restart.
type prepareState struct {
	Accounts []string `json:"accounts"`
	// Shortcuts by account id, then "<owner>/<key>".
	Shortcuts map[string]map[string]preparedShortcut `json:"shortcuts"`
}

// preparedShortcut is a shortcut as prepare left it in one account's
// shortcuts.vdf. Steam keeps the app id as a signed 32-bit number; either
// spelling reads.
type preparedShortcut struct {
	AppID   int64  `json:"appid"`
	GameID  string `json:"gameid"`
	Deleted bool   `json:"deleted"`
}

// readPrepareState reads prepare's record through gamerfs (as root, or
// as the gaming user in its own home); nil when it is missing or not
// readable as one.
func readPrepareState() *prepareState {
	b, err := gamerfs.ReadFile(config.GamerHome, config.ExtGamerStateFile, maxSteamState)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: Steam's prepare record: %v", err)
		}
		return nil
	}
	st := &prepareState{}
	if err := json.Unmarshal(b, st); err != nil {
		log.Printf("extensions: Steam's prepare record: %v", err)
		return nil
	}
	return st
}

// gameID returns the game id prepare recorded for the shortcut owner/key
// and not marked deleted. Accounts normally agree; when they do not, the
// id VaporOS gave it wins, then the lowest account's.
func (p *prepareState) gameID(owner, key string) (uint64, bool) {
	if p == nil {
		return 0, false
	}
	ref := owner + "/" + key
	want := ShortcutGameID(ShortcutAppID(owner, key))
	var first uint64
	found := false
	for _, acc := range slices.Sorted(maps.Keys(p.Shortcuts)) {
		r, ok := p.Shortcuts[acc][ref]
		if !ok || r.Deleted {
			continue
		}
		id, err := strconv.ParseUint(r.GameID, 10, 64)
		if err != nil || !isShortcutGameID(id) {
			continue
		}
		if id == want {
			return id, true
		}
		if !found {
			first, found = id, true
		}
	}
	return first, found
}

// shortcutByAppID returns the shortcut prepare recorded under appid in
// any account.
func (p *prepareState) shortcutByAppID(appid uint32) (owner, key string, ok bool) {
	if p == nil {
		return "", "", false
	}
	for _, acc := range slices.Sorted(maps.Keys(p.Shortcuts)) {
		for _, ref := range slices.Sorted(maps.Keys(p.Shortcuts[acc])) {
			if uint32(p.Shortcuts[acc][ref].AppID) == appid {
				if o, k, ok := strings.Cut(ref, "/"); ok {
					return o, k, true
				}
			}
		}
	}
	return "", "", false
}

// loginAccounts returns the account ids (SteamID64 & 0xffffffff, in
// decimal) loginusers.vdf lists, sorted.
func loginAccounts(data []byte) ([]string, error) {
	root, err := steam.ParseVDF(data)
	if err != nil {
		return nil, err
	}
	users := root.Child("users")
	if users == nil {
		return nil, nil
	}
	var out []string
	for _, u := range users.Children {
		id, err := strconv.ParseUint(u.Key, 10, 64)
		if err != nil || !u.Block || uint32(id) == 0 {
			continue
		}
		if a := strconv.FormatUint(id&0xffffffff, 10); !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out, nil
}
