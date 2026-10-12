package sunshine

import (
	"context"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sunshine sets up its capture platform once, at startup. KMS capture
// needs a lit plane then: when Sunshine comes up before gamescope's first
// modeset (both start at boot, and gamescope's unit is active before it
// has a mode), it logs "Platform failed to initialize" and fails every
// stream with error 503 until it is restarted. So vosd reads each run's
// startup log and restarts a run whose capture never came up. It holds
// that restart until gamescope settles on one plane (Screens'
// CaptureIndex), up to captureSettleMax: a run restarted while gamescope
// still scans out on two planes maps the wrong monitor index (below).
const (
	runStartMarker     = "Sunshine version:"
	platformFailMarker = "Platform failed to initialize"
	// runReadyMarker follows the platform setup in a good run and a bad one.
	runReadyMarker = "Configuration UI available"
	// captureLogLines is how much of the journal the check reads; a run's
	// startup is well under that.
	captureLogLines  = 400
	captureRetryMin  = 5 * time.Second
	captureRetryMax  = 2 * time.Minute
	captureSettleMax = 30 * time.Second
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
		s.runReadyAt = now
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
		ci, _ := s.scr.(captureIndexer)
		if ci != nil && s.settleBy.IsZero() {
			s.settleBy = now.Add(captureSettleMax)
		}
		wait := ci != nil && now.Before(s.settleBy)
		s.mu.Unlock()
		if wait {
			if _, ok := ci.CaptureIndex(); !ok {
				return
			}
		}
		s.mu.Lock()
		s.settleBy = time.Time{}
		s.nextCaptureFix = now.Add(s.captureBackoff)
		s.captureBackoff = min(2*s.captureBackoff, captureRetryMax)
		s.mu.Unlock()
		log.Printf("sunshine: capture failed to initialize at startup (no lit display yet?); restarting Sunshine")
		if err := s.restart(ctx); err != nil {
			log.Printf("sunshine: restart: %v", err)
		}
	}
}

// Sunshine also maps output_name to a KMS monitor index (the n-th lit
// plane, cursor planes left out) once, at startup, and checks capture
// against that number before a stream's prep command runs. A run that
// started while gamescope still scanned out on two planes keeps an index
// that names no plane once gamescope composites into one: "Couldn't find
// monitor [1]", and error 503 for every stream until a restart. So, while
// idle, vosd compares the index the run logged with the one a fresh run
// would pick (Screens' CaptureIndex) and restarts a run whose index went
// stale. A run is checked on every poll for indexFreshFor after its
// capture came up: at boot, the first stream often comes within seconds.
const (
	indexCheckEvery = 15 * time.Second
	indexFreshFor   = 2 * time.Minute
	indexRetryMin   = time.Minute
	indexRetryMax   = 30 * time.Minute
)

// mappedRe is the line Sunshine logs whenever it maps output_name.
var mappedRe = regexp.MustCompile(`Mapped '[^']*' to kmsgrab monitor index (\d+)`)

// mappedIndex is the monitor index the newest run in Sunshine's log last
// mapped output_name to.
func mappedIndex(logText string) (int, bool) {
	if i := strings.LastIndex(logText, runStartMarker); i >= 0 {
		logText = logText[i:]
	}
	all := mappedRe.FindAllStringSubmatch(logText, -1)
	if len(all) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(all[len(all)-1][1])
	return n, err == nil
}

// captureIndexer is what Screens may also offer: the KMS monitor index a
// Sunshine started now would pick for the virtual connector, when that is
// settled.
type captureIndexer interface {
	CaptureIndex() (int, bool)
}

// checkIndex runs on idle polls once the run's capture came up: on each
// while the run is fresh, then at most every indexCheckEvery. A mismatch has to hold on two checks in a row
// before vosd restarts Sunshine, and restarts for it back off, so a
// counting that ever differed from Sunshine's own could not loop.
func (s *Service) checkIndex(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	due := !s.verifyRun && !now.Before(s.nextIndexCheck) && !now.Before(s.nextIndexFix)
	if due && !now.Before(s.runReadyAt.Add(indexFreshFor)) {
		s.nextIndexCheck = now.Add(indexCheckEvery)
	}
	ci, _ := s.scr.(captureIndexer)
	s.mu.Unlock()
	if !due || ci == nil {
		return
	}
	want, ok := ci.CaptureIndex()
	if !ok {
		s.setIndexStale(false)
		return
	}
	text, err := s.journalTail(ctx, captureLogLines)
	if err != nil {
		return
	}
	got, ok := mappedIndex(text)
	if !ok || got == want {
		s.mu.Lock()
		s.indexStale = false
		if ok {
			s.indexBackoff = 0
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	confirmed := s.indexStale
	s.indexStale = !confirmed
	if confirmed {
		s.indexBackoff = min(max(2*s.indexBackoff, indexRetryMin), indexRetryMax)
		s.nextIndexFix = now.Add(s.indexBackoff)
	}
	s.mu.Unlock()
	if !confirmed {
		return
	}
	log.Printf("sunshine: KMS capture maps the virtual display to monitor %d, but it is monitor %d now; restarting Sunshine", got, want)
	if err := s.restart(ctx); err != nil {
		log.Printf("sunshine: restart: %v", err)
	}
}

func (s *Service) setIndexStale(v bool) {
	s.mu.Lock()
	s.indexStale = v
	s.mu.Unlock()
}
