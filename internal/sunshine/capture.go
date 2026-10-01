package sunshine

import (
	"context"
	"log"
	"strings"
	"time"
)

// Sunshine sets up its capture platform once, at startup. KMS capture
// needs a lit plane then: when Sunshine comes up before gamescope's first
// modeset (both start at boot, and gamescope's unit is active before it
// has a mode), it logs "Platform failed to initialize" and fails every
// stream with error 503 until it is restarted. So vosd reads each run's
// startup log and restarts a run whose capture never came up.
const (
	runStartMarker     = "Sunshine version:"
	platformFailMarker = "Platform failed to initialize"
	// runReadyMarker follows the platform setup in a good run and a bad one.
	runReadyMarker = "Configuration UI available"
	// captureLogLines is how much of the journal the check reads; a run's
	// startup is well under that.
	captureLogLines = 400
	captureRetryMin = 5 * time.Second
	captureRetryMax = 2 * time.Minute
)

type captureVerdict int

const (
	capturePending captureVerdict = iota // the run has not got that far yet
	captureOK
	captureFailed
)

// captureState judges the newest run in Sunshine's log.
func captureState(logText string) captureVerdict {
	i := strings.LastIndex(logText, runStartMarker)
	if i < 0 {
		return capturePending
	}
	run := logText[i:]
	switch {
	case strings.Contains(run, platformFailMarker):
		return captureFailed
	case strings.Contains(run, runReadyMarker):
		return captureOK
	}
	return capturePending
}

// checkCapture runs on idle polls while a started run is unverified and
// restarts Sunshine when that run's capture platform failed.
func (s *Service) checkCapture(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	due := s.verifyRun && !now.Before(s.nextCaptureFix)
	s.mu.Unlock()
	if !due {
		return
	}
	text, err := s.journalTail(ctx, captureLogLines)
	if err != nil {
		return
	}
	switch captureState(text) {
	case captureOK:
		s.mu.Lock()
		s.verifyRun, s.captureBackoff = false, 0
		s.mu.Unlock()
	case captureFailed:
		s.mu.Lock()
		if s.captureBackoff == 0 {
			// Give gamescope a moment to light the virtual display first.
			s.captureBackoff = captureRetryMin
			s.nextCaptureFix = now.Add(s.captureBackoff)
			s.mu.Unlock()
			return
		}
		s.nextCaptureFix = now.Add(s.captureBackoff)
		s.captureBackoff = min(2*s.captureBackoff, captureRetryMax)
		s.mu.Unlock()
		log.Printf("sunshine: capture failed to initialize at startup (no lit display yet?); restarting Sunshine")
		if err := s.restart(ctx); err != nil {
			log.Printf("sunshine: restart: %v", err)
		}
	}
}
