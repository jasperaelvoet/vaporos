// Package install writes VaporOS to a disk: the CLI `vos install` and the
// web installer (installer mode, live ISO) share Install. Port of the bash
// cmd_install with the A/B layout from docs/CONTRACTS.md.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

var ErrNotImplemented = errors.New("not implemented")

type Options struct {
	Disk      string   `json:"disk"`
	Mode      string   `json:"mode"` // "erase" | "repair"
	Hostname  string   `json:"hostname"`
	Password  string   `json:"password"` // web admin password
	Timezone  string   `json:"timezone"`
	Libraries []string `json:"libraries"` // filesystem UUIDs to adopt
	Source    string   `json:"source"`    // "" = live medium; else update source (oci://, http://, dir)
}

// Progress reports one step of an install.
type Progress func(step string, percent int, message string)

// Install performs a full install. It refuses to run outside live mode
// unless allowAnywhere is set (tests).
func Install(ctx context.Context, opts Options, progress Progress) error { return ErrNotImplemented }

type Service struct{ cfg *config.Config }

func NewService(cfg *config.Config) *Service { return &Service{cfg: cfg} }

// Routes registers /install/* (Setup access).
func (s *Service) Routes(srv *api.Server) {}

// CLI implements `vos install --disk D --hostname H --password P
// --timezone Z [--source SRC] [--library UUID]... [--yes]`.
func CLI(args []string) int {
	fmt.Fprintln(os.Stderr, "vos install: not implemented")
	return 1
}
