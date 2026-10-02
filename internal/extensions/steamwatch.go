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

// steamWatchEvery is how often watchSteam looks for launch messages; it
// looks at Steam's accounts every accountsEvery-th time. Variables for
// tests.
var (
	steamWatchEvery = 3 * time.Second
	accountsEvery   = 5
)

// watchSteam publishes what `vos ext launch` left for the user and asks
// for a Steam restart when Steam has an account prepare has not set up,
// until ctx ends.
func (s *Service) watchSteam(ctx context.Context) {
	t := time.NewTicker(steamWatchEvery)
	defer t.Stop()
	for i := 0; ; i++ {
		s.pollMessages()
		if i%accountsEvery == 0 {
			s.checkAccounts()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

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
