package sunshine

import (
	"context"
	"slices"
)

// Summary is GET /sunshine without the session.
type Summary struct {
	Running        bool      `json:"running"`
	Version        string    `json:"version"`
	Streaming      bool      `json:"streaming"`
	PendingPairing bool      `json:"pending_pairing"`
	Pairings       []Pairing `json:"pairings"` // who is waiting, for choosing one in POST /sunshine/pair
}

// Summary answers from what the poll loop keeps, at most pollInterval old,
// for GET /status: it never waits on Sunshine. Only running is asked now
// (one systemctl call).
func (s *Service) Summary(ctx context.Context) Summary {
	sum := Summary{Running: s.unitActive(ctx), Pairings: []Pairing{}}
	s.mu.Lock()
	sum.Streaming = !s.busySince.IsZero()
	if s.pairings != nil {
		sum.Pairings = slices.Clone(s.pairings)
	}
	sum.Version = s.version
	s.mu.Unlock()
	sum.PendingPairing = len(sum.Pairings) > 0
	if sum.Version == "" {
		sum.Version = s.installedVersion()
	}
	return sum
}
