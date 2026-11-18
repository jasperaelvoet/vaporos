package power

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// The power plan (docs/CONTRACTS.md, Units, Power plan): config.json's
// power.profile, which vosd makes power-profiles-daemon's active profile
// at start and whenever PUT /power changes it. The daemon is asked through
// busctl, as sysd asks systemd through systemctl.

// ProfileBalanced is the default plan, and the one every PC offers.
const ProfileBalanced = "balanced"

// knownProfiles are the plans vosd knows, least power first: the order
// the API lists them in.
var knownProfiles = []string{"power-saver", ProfileBalanced, "performance"}

const (
	// ppdName is the daemon's bus name and its interface.
	ppdName = "org.freedesktop.UPower.PowerProfiles"
	ppdPath = "/org/freedesktop/UPower/PowerProfiles"
	// profileTimeout bounds one call to the daemon. busctl gives up a
	// second earlier, so its own words reach the log.
	profileTimeout = 5 * time.Second
	// profileAttempts is how often the plan of the start is tried: once
	// at start, then on the policy's passes. vosd starts before the
	// daemon, whose start a call triggers and waits for.
	profileAttempts = 8
)

func busctl(ctx context.Context, args ...string) (string, error) {
	return sysd.Run(ctx, "busctl", append([]string{"--system", "--timeout=4"}, args...)...)
}

// busProfiles reads the daemon's Profiles property: the plans this PC has.
func busProfiles(ctx context.Context) ([]string, error) {
	out, err := busctl(ctx, "--json=short", "get-property", ppdName, ppdPath, ppdName, "Profiles")
	if err != nil {
		return nil, err
	}
	return parseProfiles(out)
}

func busSetProfile(ctx context.Context, name string) error {
	_, err := busctl(ctx, "set-property", ppdName, ppdPath, ppdName, "ActiveProfile", "s", name)
	return err
}

// parseProfiles takes the names out of busctl's JSON for Profiles, an
// aa{sv}: {"data":[{"Profile":{"type":"s","data":"balanced"},…},…]}. It
// returns the plans vosd knows, least power first.
func parseProfiles(out string) ([]string, error) {
	var v struct {
		Data []map[string]struct {
			Data json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("power-profiles-daemon's profiles: %w", err)
	}
	have := map[string]bool{}
	for _, p := range v.Data {
		var name string
		if json.Unmarshal(p["Profile"].Data, &name) == nil {
			have[name] = true
		}
	}
	var list []string
	for _, name := range knownProfiles {
		if have[name] {
			list = append(list, name)
		}
	}
	if len(list) == 0 {
		return nil, errors.New("power-profiles-daemon offers no power plan")
	}
	return list, nil
}

// effectiveProfile is the plan in use: the saved one, or balanced when the
// saved one is no plan or the PC is known not to offer it.
func effectiveProfile(saved string, offered []string) string {
	if !slices.Contains(knownProfiles, saved) || (len(offered) > 0 && !slices.Contains(offered, saved)) {
		return ProfileBalanced
	}
	return saved
}

// readProfiles asks the daemon which plans the PC offers and keeps the
// answer for Summary. No answer forgets the last one.
func (s *Service) readProfiles(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	list, err := s.listProfiles(ctx)
	if err != nil {
		list = nil
	}
	s.mu.Lock()
	s.profiles = list
	s.mu.Unlock()
	return list, err
}

func (s *Service) switchProfile(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	return s.setProfile(ctx, name)
}

// applyProfile makes the saved plan the daemon's. Run calls it at start
// and on every pass; it does nothing once the daemon has taken a plan or
// the attempts are used up.
func (s *Service) applyProfile(ctx context.Context) {
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	s.mu.Lock()
	left := s.profileLeft
	if left > 0 {
		s.profileLeft--
	}
	s.mu.Unlock()
	if left <= 0 {
		return
	}
	saved := s.settings().Profile
	offered, err := s.readProfiles(ctx)
	want := effectiveProfile(saved, offered)
	if err == nil {
		err = s.switchProfile(ctx, want)
	}
	if err != nil {
		if left == 1 {
			log.Printf("power: the power plan was not set: %v", err)
		}
		return
	}
	if want != saved && slices.Contains(knownProfiles, saved) {
		log.Printf("power: this PC does not offer the %s power plan", saved)
	}
	log.Printf("power: power plan %s", want)
	s.profileSettled()
}

// profileSettled ends the attempts of the start: the daemon has the plan.
func (s *Service) profileSettled() {
	s.mu.Lock()
	s.profileLeft = 0
	s.mu.Unlock()
}

// changeProfile is PUT /power's profile: the daemon switches before
// anything is saved. It answers the HTTP status and message of a refusal,
// 0 when the daemon switched. The caller holds profileMu.
func (s *Service) changeProfile(ctx context.Context, name string) (int, string) {
	offered, err := s.readProfiles(ctx)
	if err != nil {
		log.Printf("power: power plan: %v", err)
		return http.StatusBadGateway, "power-profiles-daemon is not answering"
	}
	if !slices.Contains(offered, name) {
		return http.StatusConflict, fmt.Sprintf("this PC does not offer the %s power plan", name)
	}
	if err := s.switchProfile(ctx, name); err != nil {
		log.Printf("power: power plan: %v", err)
		return http.StatusBadGateway, fmt.Sprintf("power-profiles-daemon did not switch to the %s power plan", name)
	}
	return 0, ""
}
