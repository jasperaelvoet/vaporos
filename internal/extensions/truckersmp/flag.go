package truckersmp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// The multiplayer flag: `vos ext truckersmp mp` asks Steam to start the
// game itself, so the launch is Steam's own (its prefix, its overlay, no
// second game running), and leaves this flag for the launch hook, which
// turns that one start into multiplayer. The hook takes the flag once, and
// only within flagTTL, so a later single-player start stays single-player.

const flagTTL = 15 * time.Minute

// flagSkew is how far in the future a flag's time may be (clock changes).
const flagSkew = time.Minute

type mpFlag struct {
	Game    string    `json:"game"`
	Created time.Time `json:"created"`
	Nonce   string    `json:"nonce"`
}

var nonceRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// flagPath is the flag in the gaming user's runtime directory.
func flagPath() string { return filepath.Join(runtimeDir(), "vos", "truckersmp-mp.json") }

func (f *mpFlag) valid(now time.Time) bool {
	_, known := gameByKey(f.Game)
	age := now.Sub(f.Created)
	return known && nonceRe.MatchString(f.Nonce) && !f.Created.IsZero() && age >= -flagSkew && age <= flagTTL
}

// writeFlag leaves a fresh flag for g.
func writeFlag(path string, g game, now time.Time) (mpFlag, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return mpFlag{}, err
	}
	f := mpFlag{Game: g.key, Created: now.UTC(), Nonce: hex.EncodeToString(b)}
	data, err := json.Marshal(f)
	if err != nil {
		return mpFlag{}, err
	}
	return f, config.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

func readFlag(path string) (*mpFlag, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var f mpFlag
	if err := json.NewDecoder(io.LimitReader(fh, 4096)).Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// takeFlag consumes a valid flag for g and reports whether there was one.
// A flag that is not valid (expired, malformed) is deleted; one for the
// other game is left for it. Renaming it away first makes taking it
// atomic: of two launches, one gets it.
func takeFlag(path string, g game, now time.Time) bool {
	f, err := readFlag(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil || !f.valid(now) {
		os.Remove(path)
		return false
	}
	if f.Game != g.key {
		return false
	}
	taken := path + ".taken-" + strconv.Itoa(os.Getpid())
	if err := os.Rename(path, taken); err != nil {
		return false
	}
	defer os.Remove(taken)
	t, err := readFlag(taken)
	if err != nil || !t.valid(now) {
		return false
	}
	if t.Game != g.key {
		// Replaced for the other game in between: give it back.
		os.Rename(taken, path)
		return false
	}
	return true
}

// dropFlag deletes the flag if it is still the one with nonce: the start
// it was for did not happen.
func dropFlag(path, nonce string) {
	if f, err := readFlag(path); err == nil && f.Nonce == nonce {
		os.Remove(path)
	}
}
