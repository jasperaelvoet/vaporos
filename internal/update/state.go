package update

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// State is /var/lib/vos/update-state.json (docs/CONTRACTS.md "Update
// state"). vosd and `vos update` both write it, under a lock.
type State struct {
	Booted    string     `json:"booted"`
	Staged    *Staged    `json:"staged"`
	Failed    []string   `json:"failed"`
	Available *Available `json:"available"`
	Checked   string     `json:"checked,omitempty"` // last successful check, RFC3339
	LastError string     `json:"last_error"`
	// Held is the newest version the user went back from (a rollback or
	// an explicit downgrade). Automatic staging skips it and anything
	// older; picking a version, --force or a newer build lifts it.
	Held *Held `json:"held,omitempty"`
}

// Held is a version automatic updates leave alone.
type Held struct {
	Version       string `json:"version"`
	RollbackIndex int64  `json:"rollback_index"`
}

// hold records that the user went back from img, keeping the newest hold.
func (st *State) hold(img *config.ImageInfo) {
	if st.Held == nil || img.RollbackIndex > st.Held.RollbackIndex {
		st.Held = &Held{Version: img.Version, RollbackIndex: img.RollbackIndex}
	}
}

// Staged is a version written to the idle slot that has not booted yet.
type Staged struct {
	Version string `json:"version"`
	Slot    string `json:"slot"`
	At      string `json:"at"`
}

// Available is a newer version the source offers.
type Available struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	Checked string `json:"checked"`
}

// maxFailed bounds failed[]: old failures stop mattering once newer images
// exist.
const maxFailed = 20

func updateLockPath() string { return filepath.Join(config.RunDir, "update.lock") }
func stateLockPath() string  { return filepath.Join(config.RunDir, "update-state.lock") }

// LoadState reads update-state.json. A missing file is an empty state.
func LoadState() (*State, error) {
	st := &State{}
	err := config.ReadJSON(config.UpdateStatePath(), st)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if st.Failed == nil {
		st.Failed = []string{}
	}
	return st, err
}

// HasFailed reports whether version failed to boot before.
func (st *State) HasFailed(version string) bool { return slices.Contains(st.Failed, version) }

func (st *State) addFailed(version string) {
	if st.HasFailed(version) {
		return
	}
	st.Failed = append(st.Failed, version)
	if n := len(st.Failed); n > maxFailed {
		st.Failed = st.Failed[n-maxFailed:]
	}
}

func (st *State) removeFailed(version string) {
	st.Failed = slices.DeleteFunc(st.Failed, func(v string) bool { return v == version })
}

// modifyState applies f to the state under a lock that vosd and the CLI
// share, writes it atomically and publishes it as update.state.
func modifyState(f func(st *State) error) (*State, error) {
	l, err := lockFile(stateLockPath(), true)
	if err != nil {
		return nil, err
	}
	defer l.Unlock()
	st, err := LoadState()
	if err != nil {
		// Without a terminal nobody could repair a corrupt file by hand, and
		// refusing to update because of it would strand the machine.
		log.Printf("update state: %v; starting over", err)
		st = &State{Failed: []string{}}
	}
	st.Booted = bootedImage().Version
	if err := f(st); err != nil {
		return st, err
	}
	if err := config.WriteJSONAtomic(config.UpdateStatePath(), st, 0o644); err != nil {
		return st, err
	}
	events.Publish("update.state", st)
	return st, nil
}

// bootedImage describes the running image: image.json, or IMAGE_VERSION
// from os-release for images built before image.json existed.
func bootedImage() *config.ImageInfo {
	if ii, err := config.LoadImageInfo(); err == nil && ii.Version != "" {
		return ii
	}
	ii := &config.ImageInfo{Version: osReleaseValue("IMAGE_VERSION")}
	if ii.Version == "" {
		ii.Version = "unknown"
	}
	return ii
}

func osReleaseValue(key string) string {
	b, err := os.ReadFile(config.OSReleasePath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// Reconcile is the post-boot bookkeeping vosd runs at start
// (docs/CONTRACTS.md "Update state"). A staged version that is now running
// is done. One that is not running and has no tries left, or no entry at
// all, failed to boot and moves to failed[].
//
// "Running" only counts once the boot is no longer on trial. vosd starts
// before `vos health` has passed, and if the new version then fails its
// tries and falls back, staged must still be there for the fallback boot
// to find and record. `vos health` clears it when it passes.
func Reconcile() (*State, error) {
	espErr := boot.EnsureESP(config.ESP)
	return modifyState(func(st *State) error {
		booted := st.Booted
		if st.Available != nil && st.Available.Version == booted {
			st.Available = nil
		}
		if st.Staged == nil {
			return nil
		}
		if st.Staged.Version == booted {
			if !bootCounting() {
				st.Staged = nil
			}
			return nil
		}
		if espErr != nil {
			return nil // cannot tell; look again next start
		}
		e, err := boot.EntryForSlot(config.ESP, st.Staged.Slot)
		if err != nil {
			return nil
		}
		if e != nil && e.Version == st.Staged.Version && e.Bootable() {
			return nil // not tried yet: no reboot since it was staged
		}
		st.addFailed(st.Staged.Version)
		st.LastError = fmt.Sprintf("VaporOS %s did not start correctly; still running %s", st.Staged.Version, booted)
		st.Staged = nil
		return nil
	})
}
