package extensions

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// steamWatchEvery is how often WatchSteam looks for launch messages; it
// looks at Steam's accounts every accountsEvery-th time and writes
// steam.json again every syncEvery-th time (about once a minute).
// Variables for tests.
var (
	steamWatchEvery = 3 * time.Second
	accountsEvery   = 5
	syncEvery       = 20
)

// WatchSteam publishes what `vos ext launch` left for the user, asks for
// a Steam restart when Steam has an account prepare has not set up, and
// writes steam.json again when the slots change (SlotsChanged) and about
// once a minute, which catches a `vos update` or `vos rollback` run from a
// shell, until ctx ends. vosd runs and restarts it apart from Run, so a
// pass that cannot start or panics leaves it alone.
func (s *Service) WatchSteam(ctx context.Context) {
	if config.IsLive() {
		return
	}
	accounts, syncs := max(accountsEvery, 1), max(syncEvery, 1)
	t := time.NewTicker(steamWatchEvery)
	defer t.Stop()
	for i := 0; ; i++ {
		s.pollMessages()
		if i%accounts == 0 {
			s.checkAccounts()
		}
		if i%syncs == 0 {
			s.syncSteam()
		}
		select {
		case <-ctx.Done():
			return
		case <-s.slotsKick:
			s.syncSteam()
		case <-t.C:
		}
	}
}

// SlotsChanged tells WatchSteam that what the other slot boots may have
// changed (update.Service.SetSlotsChanged): steam.json's dispatcher
// follows it. It never blocks.
func (s *Service) SlotsChanged() { s.signal(s.slotsKick) }

// checkAccounts asks for a Steam restart when loginusers.vdf lists an
// account prepare's record lacks: someone signed in to Steam after
// prepare ran, and prepare sets their launch options only at Steam's
// start. Once per set of such accounts, so a prepare that cannot set one
// up does not restart Steam again and again. Without a record (prepare
// never ran) there is nothing to compare.
func (s *Service) checkAccounts() {
	b, err := gamerfs.ReadFile(config.GamerHome, loginUsersRel, maxSteamState)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: Steam's accounts: %v", err)
		}
		return
	}
	users, err := loginAccounts(b)
	if err != nil {
		return // Steam is writing it, or it is not Steam's
	}
	st := readPrepareState()
	if st == nil {
		return
	}
	var missing []string
	for _, a := range users {
		if !slices.Contains(st.Accounts, a) {
			missing = append(missing, a)
		}
	}
	key := strings.Join(missing, " ")
	if key == s.missingAccounts {
		return
	}
	s.missingAccounts = key
	if key != "" {
		log.Printf("extensions: Steam account %s is not set up for VaporOS yet", key)
		s.restartSteam("a Steam account signed in")
	}
}
