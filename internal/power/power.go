// Package power is the idle-shutdown policy (port of the reference
// gaming-idle-shutdown), keep-awake, and Wake-on-LAN status.
package power

import (
	"context"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

// BusyFunc reports whether something keeps the machine awake, and why.
type BusyFunc func() (bool, string)

type Service struct {
	cfg  *config.Config
	busy []BusyFunc
}

func NewService(cfg *config.Config, busy ...BusyFunc) *Service {
	return &Service{cfg: cfg, busy: busy}
}

// Routes registers /power*.
func (s *Service) Routes(srv *api.Server) {}

// Run polls every 15 s and powers off after idle_minutes of idleness.
func (s *Service) Run(ctx context.Context) {}

// Touch marks user activity (e.g. web UI requests) as busy for a while.
func (s *Service) Touch() {}
