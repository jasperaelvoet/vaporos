// Package system serves /system/* and /ssh: machine info, hostname,
// reboot/poweroff, and the SSH toggle.
package system

import (
	"context"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

type Service struct{ cfg *config.Config }

func NewService(cfg *config.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) Routes(srv *api.Server) {}

func (s *Service) Run(ctx context.Context) {}
