package system

import (
	"context"
	"crypto/rsa"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"golang.org/x/crypto/ssh"
)

// Units the SSH toggle drives. The firewall unit re-reads the SSH state on
// reload to open or close port 22.
const (
	sshdUnit     = "sshd.service"
	firewallUnit = "vos-firewall.service"
)

const (
	maxSSHKeys    = 64
	maxCommentLen = 200
	minRSABits    = 2048
)

// SSHState is the GET/PUT /ssh body.
type SSHState struct {
	Enabled bool     `json:"enabled"`
	Keys    []string `json:"keys"`
}

func (s *Service) handleGetSSH(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st := SSHState{Enabled: s.cfg.SSH.Enabled, Keys: append([]string{}, s.cfg.SSH.Keys...)}
	s.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handlePutSSH(w http.ResponseWriter, r *http.Request) {
	var req SSHState
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	keys, err := NormalizeKeys(req.Keys)
	if err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Enabled && len(keys) == 0 {
		api.Error(w, http.StatusBadRequest, "add at least one public key before enabling SSH: the vapor account has no password")
		return
	}
	st, err := s.SetSSH(r.Context(), SSHState{Enabled: req.Enabled, Keys: keys})
	if err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, st)
}

// SetSSH saves the SSH settings to config.json and applies them: the keys
// go to ~vapor/.ssh/authorized_keys, sshd is enabled and started (or
// stopped and disabled), and the firewall reloads to open or close port 22.
// keys must already be normalised (NormalizeKeys).
func (s *Service) SetSSH(ctx context.Context, want SSHState) (SSHState, error) {
	if want.Keys == nil {
		want.Keys = []string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.cfg.SSH
	s.cfg.SSH = config.SSHConfig{Enabled: want.Enabled, Keys: want.Keys}
	if err := s.cfg.Save(); err != nil {
		s.cfg.SSH = old
		return SSHState{}, fmt.Errorf("saving config: %w", err)
	}
	if want.Enabled {
		// Keys first, so sshd never runs without them; the port opens last.
		if err := writeAuthorizedKeys(config.GamerHome, s.uid, s.gid, want.Keys); err != nil {
			return want, fmt.Errorf("saved, but writing authorized_keys failed: %w", err)
		}
		if err := s.run(ctx, "systemctl", "enable", "--now", sshdUnit); err != nil {
			return want, fmt.Errorf("saved, but starting sshd failed: %w", err)
		}
		if err := s.run(ctx, "systemctl", "reload-or-restart", firewallUnit); err != nil {
			return want, fmt.Errorf("saved, but reloading the firewall failed: %w", err)
		}
		return want, nil
	}
	// Reverse: stop sshd, close the port, then drop the keys. Every step
	// runs even if one fails, so as much as possible ends up closed.
	var errs []string
	if err := s.run(ctx, "systemctl", "disable", "--now", sshdUnit); err != nil {
		errs = append(errs, "stopping sshd: "+err.Error())
	}
	if err := s.run(ctx, "systemctl", "reload-or-restart", firewallUnit); err != nil {
		errs = append(errs, "reloading the firewall: "+err.Error())
	}
	if err := removeAuthorizedKeys(config.GamerHome); err != nil {
		errs = append(errs, "removing authorized_keys: "+err.Error())
	}
	if len(errs) > 0 {
		return want, fmt.Errorf("saved, but %s", strings.Join(errs, "; "))
	}
	return want, nil
}

// syncAuthorizedKeys makes the key file match conf without touching units.
func (s *Service) syncAuthorizedKeys(conf config.SSHConfig) error {
	if !conf.Enabled {
		return removeAuthorizedKeys(config.GamerHome)
	}
	keys, err := NormalizeKeys(conf.Keys)
	if err != nil {
		return err
	}
	return writeAuthorizedKeys(config.GamerHome, s.uid, s.gid, keys)
}

// NormalizeKeys validates OpenSSH public keys and returns them as
// "<type> <base64> [comment]", de-duplicated, blank entries dropped.
// authorized_keys options (command=, from=, …) are refused rather than
// silently dropped, and so are keys sshd would not accept anyway.
func NormalizeKeys(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for i, raw := range in {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		n := i + 1
		if strings.ContainsAny(k, "\r\n\x00") {
			return nil, fmt.Errorf("key %d: one key per entry, on a single line", n)
		}
		pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil || len(rest) != 0 {
			return nil, fmt.Errorf("key %d is not an OpenSSH public key (it should start with ssh-ed25519, ecdsa-sha2-… or ssh-rsa)", n)
		}
		if len(options) > 0 {
			return nil, fmt.Errorf("key %d: authorized_keys options are not supported", n)
		}
		if err := acceptableKey(pub); err != nil {
			return nil, fmt.Errorf("key %d: %v", n, err)
		}
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
		if seen[line] {
			continue
		}
		seen[line] = true
		if c := cleanComment(comment); c != "" {
			line += " " + c
		}
		out = append(out, line)
	}
	if len(out) > maxSSHKeys {
		return nil, fmt.Errorf("at most %d keys", maxSSHKeys)
	}
	return out, nil
}

func acceptableKey(pub ssh.PublicKey) error {
	switch pub.Type() {
	case ssh.KeyAlgoDSA:
		return fmt.Errorf("DSA keys are no longer accepted by sshd; use ed25519")
	case ssh.KeyAlgoRSA:
		if cp, ok := pub.(ssh.CryptoPublicKey); ok {
			if rk, ok := cp.CryptoPublicKey().(*rsa.PublicKey); ok && rk.N.BitLen() < minRSABits {
				return fmt.Errorf("RSA keys need at least %d bits", minRSABits)
			}
		}
	}
	return nil
}

// cleanComment keeps a key comment printable and short.
func cleanComment(c string) string {
	c = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, c)
	c = strings.Join(strings.Fields(c), " ")
	if len(c) > maxCommentLen {
		c = strings.ToValidUTF8(c[:maxCommentLen], "")
	}
	return c
}
