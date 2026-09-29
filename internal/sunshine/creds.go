package sunshine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// apiCreds is /var/lib/vos/sunshine-api.json: the account vosd uses on
// Sunshine's local API. Nobody types it; it is generated once, randomly,
// and handed to Sunshine with `sunshine --creds`.
type apiCreds struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

const apiUser = "vosd"

// loadOrCreateCreds returns the stored credentials, creating them (mode
// 0600) on first use or when the file is unusable. created tells the
// caller Sunshine does not know them yet.
func loadOrCreateCreds() (c apiCreds, created bool, err error) {
	err = config.ReadJSON(config.SunshineAPIPath(), &c)
	if err == nil && c.User != "" && len(c.Password) >= 16 {
		return c, false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("sunshine: %v; generating new API credentials", err)
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return apiCreds{}, false, err
	}
	c = apiCreds{User: apiUser, Password: hex.EncodeToString(b[:])}
	if err := config.WriteJSONAtomic(config.SunshineAPIPath(), c, 0o600); err != nil {
		return apiCreds{}, false, err
	}
	return c, true, nil
}

// stateUsername is the web UI user Sunshine has stored (its
// sunshine_state.json, which also holds the paired clients), "" if none.
// Sunshine stores only a salted hash of the password, so a matching user
// name is the best offline hint that vosd's credentials are in place; a
// wrong password shows up as errUnauthorized later and is repaired then.
func stateUsername() string {
	var st struct {
		Username string `json:"username"`
	}
	data, err := readRegular(statePath())
	if err != nil {
		return ""
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return ""
	}
	return st.Username
}

// applyCreds stores the credentials in Sunshine's state, running Sunshine
// as the gaming user so the file keeps its owner. Sunshine reads them at
// startup, so a restart must follow.
func (s *Service) applyCreds(ctx context.Context, c apiCreds) error {
	_, err := s.asGamer(ctx, "sunshine", confPath(), "--creds", c.User, c.Password)
	return err
}
