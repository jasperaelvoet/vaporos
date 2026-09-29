package sunshine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// Abandoned streams. Moonlight can disconnect without quitting the app
// (its "disconnect" button, a closed laptop lid, a phone that lost Wi-Fi).
// Sunshine then keeps the app running and serverinfo keeps saying
// SUNSHINE_SERVER_BUSY, for ever with the Steam entry, which has no command
// to exit: the prep-cmd undo (`vos session end`) never runs, the display
// manager believes a stream is live, and the machine never idles off.
//
// So vosd counts Moonlight's control connections in Sunshine's log, and
// once the app has had no client for abandonMinutes it closes the app
// (POST /api/apps/close), which makes Sunshine run the undo.
const (
	// abandonMinutes is how long a running app may wait for its client to
	// come back (resume in Moonlight) before vosd closes it.
	abandonMinutes = 10
	abandonAfter   = abandonMinutes * time.Minute
	// abandonRetry spaces out attempts to close an abandoned app.
	abandonRetry = time.Minute
	// historyLines is how much of Sunshine's journal a new follower
	// replays, to learn about clients that connected before it started.
	historyLines = 2000
)

// Sunshine's log lines about Moonlight's control stream (src/stream.cpp,
// control_server_t::iterate and controlBroadcastThread) and its own start
// (src/main.cpp). None of them names the client.
const (
	// An ENet connect or disconnect of a client's control stream.
	logClientConnected    = "CLIENT CONNECTED"
	logClientDisconnected = "CLIENT DISCONNECTED"
	// "<address>: Ping Timeout": the client went silent; Sunshine drops it
	// without a CLIENT DISCONNECTED line. ("Initial Ping Timeout", a video
	// or audio stream that never started, is a different line.)
	logPingTimeout = ": Ping Timeout"
	// The app is no longer running: Sunshine ends every session.
	logAppEnded = "Process terminated"
	// A (re)started Sunshine has no clients.
	logSunshineStarted = "Sunshine version: "
)

// clientEvent is what one log line meant to the client count.
type clientEvent int

const (
	clientNone clientEvent = iota
	clientConnected
	clientLeft
	clientReset
)

// clientTrack counts connected Moonlight clients from Sunshine's log.
//
// Until the first connection line it knows nothing, and "unknown" is
// treated as connected: an app is only ever closed after a client was
// seen leaving. The count is floored at zero. With several clients at
// once, a client that times out before its control stream connected can
// make it one too low; that is the price of log lines that name nobody.
type clientTrack struct {
	known     bool      // a connection line was seen, so connected is meaningful
	connected int       // clients connected now
	since     time.Time // when the last client left (connected == 0)
}

// observe feeds one log line, logged at at.
func (c *clientTrack) observe(line string, at time.Time) clientEvent {
	switch {
	case strings.Contains(line, logClientDisconnected), strings.Contains(line, logPingTimeout):
		c.known = true
		if c.connected > 0 {
			c.connected--
		}
		if c.connected == 0 {
			c.since = at
		}
		return clientLeft
	case strings.Contains(line, logClientConnected):
		c.known = true
		c.connected++
		return clientConnected
	case strings.Contains(line, logAppEnded), strings.Contains(line, logSunshineStarted):
		*c = clientTrack{known: true, since: at}
		return clientReset
	}
	return clientNone
}

// idle reports whether no client is connected, as far as the log tells.
func (c *clientTrack) idle() bool { return c.known && c.connected == 0 }

// journalTime parses the timestamp `journalctl --output=short-unix` puts
// in front of each line ("1759179791.512345 host sunshine[42]: …").
func journalTime(line string) (time.Time, bool) {
	field, _, ok := strings.Cut(line, " ")
	if !ok {
		return time.Time{}, false
	}
	secs, frac, ok := strings.Cut(field, ".")
	if !ok || !allDigits(secs) || !allDigits(frac) || len(secs) > 12 || len(frac) > 9 {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	nsec, _ := strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 64)
	return time.Unix(sec, nsec), true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// lineAt says when a followed journal line was logged and whether it is
// new. Lines from before the follower started are replayed history, which
// only the client count uses; the freeze watchdog must never act on them.
// A line without a journal timestamp counts as new.
func lineAt(line string, start, now time.Time) (time.Time, bool) {
	if at, ok := journalTime(line); ok && at.Before(start) {
		return at, false
	}
	return now, true
}

// observeClients feeds one Sunshine log line to the client count.
func (s *Service) observeClients(line string, at time.Time, live bool) {
	s.mu.Lock()
	wasIdle := s.clients.idle()
	lastLeft, lastBegin := s.clients.since, s.sessionAt
	ev := s.clients.observe(line, at)
	isIdle := s.clients.idle()
	s.mu.Unlock()
	if !live {
		return
	}
	switch {
	case ev == clientLeft && isIdle && !wasIdle:
		log.Printf("sunshine: the Moonlight client disconnected; the app stays open for %d minutes for it to reconnect", abandonMinutes)
	case ev == clientConnected && wasIdle:
		log.Printf("sunshine: a Moonlight client connected")
		if lastBegin.Before(lastLeft) {
			s.noteResume()
		}
	}
}

// noteResume explains a resumed stream. Sunshine runs the prep command
// (`vos session begin`, which sets the virtual display's mode) when an app
// is launched, never on a resume, and nothing it logs tells vosd the mode
// the resuming client wants. So the display keeps the mode, refresh rate
// and HDR of the launch, which is wrong when another device (or the same
// one with other settings) takes over.
func (s *Service) noteResume() {
	log.Printf("sunshine: a Moonlight client resumed the running app; the display keeps the mode it was launched with")
	s.publish("system.message", map[string]string{
		"level": "info",
		"text": "A Moonlight device resumed the stream that was already running, so the screen keeps the resolution, refresh rate and HDR " +
			"it was started with. If this device wants other settings, choose Quit in Moonlight and start it again.",
	})
}

func (s *Service) resetClients() {
	s.mu.Lock()
	s.clients = clientTrack{}
	s.mu.Unlock()
}

// noteBusy records when the current busy period (an app running) began.
func (s *Service) noteBusy(busy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !busy:
		s.busySince = time.Time{}
	case s.busySince.IsZero():
		s.busySince = s.now()
	}
}

// unattendedLocked reports how long the running app has had no client;
// ok is false while a client is, or may be, connected. The time counts
// from the later of the last client leaving and vosd seeing the app run,
// so a new launch is never closed before its client had time to connect.
func (s *Service) unattendedLocked(now time.Time) (time.Duration, bool) {
	if !s.clients.idle() {
		return 0, false
	}
	since := s.clients.since
	if s.busySince.After(since) {
		since = s.busySince
	}
	return now.Sub(since), true
}

// closeIfAbandoned closes the running app once it has had no client for
// abandonAfter. Sunshine then runs the prep-cmd undo (`vos session end`),
// the display manager ends the session, and the machine may idle.
func (s *Service) closeIfAbandoned(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	left, ok := s.unattendedLocked(now)
	due := ok && left >= abandonAfter && !now.Before(s.nextAbandonTry)
	if due {
		s.nextAbandonTry = now.Add(abandonRetry)
	}
	s.mu.Unlock()
	if !due {
		return
	}
	cl := s.api()
	if cl == nil {
		log.Printf("sunshine: the app has had no Moonlight client for %d minutes, but vosd has no API credentials to close it", int(left/time.Minute))
		return
	}
	log.Printf("sunshine: the app has had no Moonlight client for %d minutes; closing it", int(left/time.Minute))
	if err := cl.CloseApp(ctx); err != nil {
		if errors.Is(err, errUnauthorized) {
			s.repairCreds(ctx)
		}
		log.Printf("sunshine: closing the abandoned app: %v", err)
		return
	}
	s.publish("system.message", map[string]string{
		"level": "info",
		"text": fmt.Sprintf("No Moonlight client came back for %d minutes after the last one disconnected, so VaporOS ended the stream "+
			"(the machine can now idle and switch off). Start it again from Moonlight to keep playing.", abandonMinutes),
	})
}
