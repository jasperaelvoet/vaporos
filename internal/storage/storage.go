// Package storage finds disks and Steam libraries, adopts game library
// disks (config.json -> mount units via the systemd generator), and
// implements the generator itself.
package storage

import (
	"context"
	"fmt"
	"os"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

type Disk struct {
	Path         string `json:"path"` // partition or disk device
	Parent       string `json:"parent,omitempty"`
	Model        string `json:"model"`
	Size         int64  `json:"size"`
	Transport    string `json:"transport,omitempty"`
	Removable    bool   `json:"removable"`
	UUID         string `json:"uuid,omitempty"`
	Label        string `json:"label,omitempty"`
	FSType       string `json:"fstype,omitempty"`
	MountedAt    string `json:"mounted_at,omitempty"`
	IsSystem     bool   `json:"is_system"`
	SteamLibrary bool   `json:"steam_library"`
	Adopted      bool   `json:"adopted"`
	Free         int64  `json:"free,omitempty"`
}

// ScanDisks lists block devices and filesystems (lsblk -J -b -O).
func ScanDisks(ctx context.Context) ([]Disk, error) { return nil, nil }

type Service struct{ cfg *config.Config }

func NewService(cfg *config.Config) *Service { return &Service{cfg: cfg} }

// Routes registers /storage*.
func (s *Service) Routes(srv *api.Server) {}

// CLIGenerator is the systemd generator (argv: normal-dir [early-dir late-dir]).
func CLIGenerator(args []string) int {
	fmt.Fprintln(os.Stderr, "vos-generator: not implemented")
	return 0
}
