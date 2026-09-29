// Package update implements `vos update|rollback|status|health|sign|keygen`
// and vosd's update service: signed manifests from OCI (ghcr), HTTP or a
// directory, streamed into the idle A/B slot, boot counting, health, and
// the update-state bookkeeping. See docs/CONTRACTS.md "Update format".
package update

import (
	"context"
	"fmt"
	"os"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

type Service struct{ cfg *config.Config }

func NewService(cfg *config.Config) *Service { return &Service{cfg: cfg} }

// Routes registers /update/* (see CONTRACTS.md).
func (s *Service) Routes(srv *api.Server) {}

// Run does post-boot bookkeeping (failed[] detection) and, when
// config.update.auto == "stage", periodic check+stage (6 h, jittered).
func (s *Service) Run(ctx context.Context) {}

// Busy reports whether an update is downloading/writing (keeps power awake).
func (s *Service) Busy() (bool, string) { return false, "" }

// CLI runs update, rollback, status, health, sign or keygen.
func CLI(cmd string, args []string) int {
	fmt.Fprintf(os.Stderr, "vos %s: not implemented\n", cmd)
	return 1
}
