package system

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

var errHostname = errors.New("the name must be 1-63 letters, digits or hyphens, and cannot start or end with a hyphen")

// ValidateHostname accepts one RFC 1123 label (it becomes <name>.local, so
// dots are out) in lower case. "localhost" is refused: it would shadow the
// loopback name on every client.
func ValidateHostname(name string) error {
	if len(name) < 1 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return errHostname
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return errHostname
		}
	}
	if name == "localhost" {
		return errors.New(`"localhost" is reserved`)
	}
	return nil
}

func (s *Service) handleHostname(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Hostname))
	if err := ValidateHostname(name); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := s.SetHostname(r.Context(), name); err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	api.OK(w)
}

// SetHostname persists name to /etc/hostname, applies it to the running
// system, and restarts avahi so <name>.local answers right away. Only the
// file write can fail the call: it is what survives a reboot, and a
// half-applied change fixes itself on the next boot.
func (s *Service) SetHostname(ctx context.Context, name string) error {
	if err := ValidateHostname(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := config.WriteFileAtomic(config.HostnamePath, []byte(name+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", config.HostnamePath, err)
	}
	applied := false
	if _, err := s.lookPath("hostnamectl"); err == nil {
		if err := s.run(ctx, "hostnamectl", "set-hostname", name); err != nil {
			log.Printf("system: hostnamectl: %v", err)
		} else {
			applied = true
		}
	}
	if !applied {
		if err := s.setKernelHn(name); err != nil {
			log.Printf("system: setting the kernel hostname: %v", err)
		}
	}
	// try-restart: follow the new name if avahi runs, never start it.
	if err := s.run(ctx, "systemctl", "try-restart", "avahi-daemon.service"); err != nil {
		log.Printf("system: restarting avahi: %v", err)
	}
	log.Printf("system: hostname is now %s", name)
	return nil
}
