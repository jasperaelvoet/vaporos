// Package sunshine configures and talks to Sunshine: renders
// ~vapor/.config/sunshine/{sunshine.conf,apps.json}, manages the local API
// credentials, proxies pairing/clients/logs for the web UI, and watches for
// the KMS plane-loss freeze during sessions. See docs/CONTRACTS.md.
package sunshine

import (
	"context"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

type Service struct{ cfg *config.Config }

func NewService(cfg *config.Config) *Service { return &Service{cfg: cfg} }

// Routes registers /sunshine/*.
func (s *Service) Routes(srv *api.Server) {}

// Run renders config, ensures credentials, and runs the watchdog.
func (s *Service) Run(ctx context.Context) {}

// Busy reports an active stream (serverinfo SUNSHINE_SERVER_BUSY).
func (s *Service) Busy() (bool, string) { return false, "" }
