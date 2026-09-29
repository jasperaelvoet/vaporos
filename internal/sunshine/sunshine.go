// Package sunshine configures and talks to Sunshine: renders
// ~vapor/.config/sunshine/{sunshine.conf,apps.json}, manages the local API
// credentials, proxies pairing/clients/logs for the web UI, and watches for
// the KMS plane-loss freeze during sessions. See docs/CONTRACTS.md.
package sunshine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"text/template"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

const (
	unitName       = "vos-sunshine.service"
	defaultAPIBase = "https://127.0.0.1:47990"
	defaultInfoURL = "http://127.0.0.1:47989/serverinfo"

	// pollInterval paces stream detection, the watchdog and pairing
	// requests (Moonlight shows its PIN and waits for it).
	pollInterval = 3 * time.Second
	// confInterval re-renders sunshine.conf, to pick up a changed
	// virtual connector; appsInterval rescans the Steam libraries.
	confInterval = 30 * time.Second
	appsInterval = 10 * time.Minute
	// credsRetry spaces out repairs of rejected API credentials.
	credsRetry = 10 * time.Minute
	// restartRetry spaces out retries of a failed Sunshine restart.
	restartRetry = time.Minute
	// logLines is what GET /sunshine/logs returns at most.
	logLines = 2000
)

// sessionInfo is the current Moonlight session, from the display
// manager's session.begin event.
type sessionInfo struct {
	Client string `json:"client"`
	Mode   string `json:"mode"`
	HDR    bool   `json:"hdr"`
}

type Service struct {
	cfg *config.Config

	confMu sync.Mutex // serializes rendering + writing sunshine.conf

	mu             sync.Mutex
	client         *Client
	creds          apiCreds
	tmpl           *template.Template
	settings       Settings
	settingsLoaded bool
	session        *sessionInfo
	pairings       []Pairing
	seenPairings   map[string]bool
	version        string
	pendingRestart bool
	nextConf       time.Time
	nextApps       time.Time
	nextCredsFix   time.Time
	nextRestartTry time.Time
	cooldownUntil  time.Time
	followFailed   bool

	watch planeWatch // Run's goroutine only

	// Seams for tests; the defaults drive the real system.
	pollEvery     time.Duration
	apiBase       string
	infoURL       string
	infoHTTP      *http.Client
	sysDRM        string
	now           func() time.Time
	publish       func(topic string, data any)
	subscribe     func() (<-chan events.Event, func())
	userSystemctl func(ctx context.Context, args ...string) error
	unitActive    func(ctx context.Context) bool
	asGamer       func(ctx context.Context, name string, args ...string) (string, error)
	follow        func(ctx context.Context) (<-chan string, error)
	journalTail   func(ctx context.Context, n int) (string, error)
	games         func() []steam.App
}

func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg:          cfg,
		seenPairings: map[string]bool{},
		pollEvery:    pollInterval,
		apiBase:      defaultAPIBase,
		infoURL:      defaultInfoURL,
		infoHTTP: &http.Client{
			Timeout:   2 * time.Second,
			Transport: &http.Transport{Proxy: nil, MaxIdleConns: 1, IdleConnTimeout: 30 * time.Second},
		},
		sysDRM:        "/sys/class/drm",
		now:           time.Now,
		publish:       events.Publish,
		subscribe:     events.Default.Subscribe,
		userSystemctl: sysd.UserSystemctl,
		unitActive:    func(ctx context.Context) bool { return sysd.IsActive(ctx, unitName, true) },
		asGamer:       sysd.AsGamer,
		follow:        followJournal,
		journalTail:   journalTail,
		games:         func() []steam.App { return installedGames(config.GamerHome) },
	}
}

// Run renders config, ensures credentials, and runs the watchdog.
func (s *Service) Run(ctx context.Context) {
	if config.IsLive() {
		return // the user units do not run on the live ISO
	}
	evs, unsubscribe := s.subscribe()
	defer unsubscribe()
	s.prepare(ctx)

	tick := time.NewTicker(s.pollEvery)
	defer tick.Stop()
	var lines <-chan string
	stopFollow := context.CancelFunc(func() {})
	following := false
	stop := func() {
		stopFollow()
		stopFollow, lines, following = func() {}, nil, false
		s.watch.reset()
	}
	defer func() { stop() }()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evs:
			if !ok {
				evs = nil
				continue
			}
			s.onEvent(ev)
		case line, ok := <-lines:
			if !ok {
				stop() // journalctl went away; the next poll starts a new one
				continue
			}
			if s.watch.observe(line, s.now()) {
				stop()
				s.recoverFrozenStream(ctx)
			}
		case <-tick.C:
			busy := s.poll(ctx)
			switch {
			case busy && !following && !s.now().Before(s.cooldown()):
				fctx, cancel := context.WithCancel(ctx)
				ch, err := s.follow(fctx)
				if err != nil {
					cancel()
					s.noteFollowError(err)
					continue
				}
				s.noteFollowError(nil)
				lines, stopFollow, following = ch, cancel, true
			case !busy && following:
				stop()
			}
		}
	}
}

// Busy reports an active stream (serverinfo SUNSHINE_SERVER_BUSY).
func (s *Service) Busy() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.streaming(ctx) {
		return true, "Moonlight stream"
	}
	return false, ""
}

func (s *Service) streaming(ctx context.Context) bool {
	busy, err := fetchServerInfo(ctx, s.infoHTTP, s.infoURL)
	return err == nil && busy
}

// api returns the Sunshine API client, nil until credentials exist.
func (s *Service) api() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Service) setCreds(c apiCreds) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds = c
	s.client = NewClient(s.apiBase, c.User, c.Password, certPath())
}

// prepare brings Sunshine's files in line at startup: API credentials,
// sunshine.conf and apps.json, then one restart if anything changed.
func (s *Service) prepare(ctx context.Context) {
	s.mu.Lock()
	s.tmpl = loadTemplate()
	now := s.now()
	s.nextConf, s.nextApps = now.Add(confInterval), now.Add(appsInterval)
	s.mu.Unlock()

	changed := false
	if c, err := s.writeConf(); err != nil {
		log.Printf("sunshine: writing %s: %v", confPath(), err)
	} else if c {
		log.Printf("sunshine: wrote %s", confPath())
		changed = true
	}
	if c, err := s.writeApps(); err != nil {
		log.Printf("sunshine: writing %s: %v", appsPath(), err)
	} else if c {
		log.Printf("sunshine: wrote %s", appsPath())
		changed = true
	}

	creds, created, err := loadOrCreateCreds()
	if err != nil {
		log.Printf("sunshine: API credentials: %v", err)
	} else {
		s.setCreds(creds)
		if created || stateUsername() != creds.User {
			if err := s.applyCreds(ctx, creds); err != nil {
				log.Printf("sunshine: setting API credentials: %v", err)
			} else {
				log.Printf("sunshine: API credentials set")
				changed = true
			}
		}
	}
	if changed {
		if err := s.requestRestart(ctx); err != nil {
			log.Printf("sunshine: restart: %v", err)
		}
	}
}

// currentSettings returns the user-tunable settings, reading them from
// the existing sunshine.conf the first time.
func (s *Service) currentSettings() Settings {
	s.mu.Lock()
	loaded := s.settingsLoaded
	s.mu.Unlock()
	if !loaded {
		prev, _ := readRegular(confPath())
		set := settingsFromConf(parseConf(prev))
		s.mu.Lock()
		if !s.settingsLoaded {
			s.settings, s.settingsLoaded = set, true
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// writeConf renders sunshine.conf from the settings, the configured
// virtual connector and the template, and writes it if it changed.
func (s *Service) writeConf() (bool, error) {
	s.confMu.Lock()
	defer s.confMu.Unlock()
	set := s.currentSettings()
	prevData, _ := readRegular(confPath())
	s.mu.Lock()
	tmpl := s.tmpl
	if tmpl == nil {
		tmpl = loadTemplate()
		s.tmpl = tmpl
	}
	s.mu.Unlock()
	conn := outputName(s.cfg.Display.VirtualConnector)
	data := renderConf(tmpl, confData{
		Encoder:     set.Encoder,
		AdapterName: adapterFor(s.sysDRM, conn),
		OutputName:  conn,
		MaxBitrate:  set.BitrateKbpsMax,
		AudioSink:   set.AudioSink,
		Gamepad:     set.Gamepad,
		PrepCmd:     sessionPrepCmd,
	}, parseConf(prevData))
	return writeGamerFile(confPath(), data, 0o600)
}

func (s *Service) writeApps() (bool, error) {
	return writeGamerFile(appsPath(), renderApps(s.games()), 0o600)
}

// requestRestart restarts Sunshine now, or after the current stream: a
// configuration change is never worth cutting someone's game off.
func (s *Service) requestRestart(ctx context.Context) error {
	if s.streaming(ctx) {
		s.mu.Lock()
		s.pendingRestart = true
		s.mu.Unlock()
		log.Printf("sunshine: restart deferred until the stream ends")
		return nil
	}
	return s.restart(ctx)
}

// restart restarts the vos-sunshine user unit. If the gaming user's
// service manager cannot be reached, Sunshine is asked to re-exec itself.
func (s *Service) restart(ctx context.Context) error {
	err := s.userSystemctl(ctx, "restart", unitName)
	if err != nil {
		if cl := s.api(); cl != nil && cl.Restart(ctx) == nil {
			err = nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.pendingRestart = true
		s.nextRestartTry = s.now().Add(restartRetry)
		return err
	}
	s.pendingRestart = false
	s.version = ""
	s.pairings = nil
	return nil
}

// poll runs every pollInterval and reports whether a stream is live.
func (s *Service) poll(ctx context.Context) bool {
	busy := s.streaming(ctx)
	if !busy {
		s.mu.Lock()
		s.session = nil
		s.mu.Unlock()
		s.maintain(ctx)
	}
	s.refreshPairings(ctx)
	s.refreshVersion(ctx)
	return busy
}

// maintain applies what waits for an idle moment: re-rendered files and
// deferred restarts.
func (s *Service) maintain(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	checkConf, checkApps := !now.Before(s.nextConf), !now.Before(s.nextApps)
	if checkConf {
		s.nextConf = now.Add(confInterval)
	}
	if checkApps {
		s.nextApps = now.Add(appsInterval)
	}
	pending := s.pendingRestart && !now.Before(s.nextRestartTry)
	s.mu.Unlock()

	changed := false
	if checkConf {
		if c, err := s.writeConf(); err != nil {
			log.Printf("sunshine: writing %s: %v", confPath(), err)
		} else if c {
			log.Printf("sunshine: %s changed", confPath())
			changed = true
		}
	}
	if checkApps {
		if c, err := s.writeApps(); err != nil {
			log.Printf("sunshine: writing %s: %v", appsPath(), err)
		} else if c {
			log.Printf("sunshine: game list changed")
			changed = true
		}
	}
	if changed || pending {
		if err := s.restart(ctx); err != nil {
			log.Printf("sunshine: restart: %v", err)
		}
	}
}

// refreshPairings tracks clients waiting for a PIN and announces new ones
// (pairing.pending), so the web UI and welcome screen can prompt for it.
func (s *Service) refreshPairings(ctx context.Context) {
	cl := s.api()
	if cl == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pollInterval)
	defer cancel()
	ps, err := cl.PendingPairings(pctx)
	if errors.Is(err, errUnauthorized) {
		s.repairCreds(ctx)
	}
	if err != nil {
		ps = nil // down, older than pairing ids, or not ours to ask
	}
	s.mu.Lock()
	var fresh []Pairing
	seen := map[string]bool{}
	for _, p := range ps {
		seen[p.ID] = true
		if !s.seenPairings[p.ID] {
			fresh = append(fresh, p)
		}
	}
	s.seenPairings, s.pairings = seen, ps
	s.mu.Unlock()
	for _, p := range fresh {
		ev := map[string]string{}
		if p.Name != "" {
			ev["name"] = p.Name
		}
		s.publish("pairing.pending", ev)
	}
}

func (s *Service) refreshVersion(ctx context.Context) {
	s.mu.Lock()
	have := s.version != ""
	s.mu.Unlock()
	cl := s.api()
	if have || cl == nil {
		return
	}
	vctx, cancel := context.WithTimeout(ctx, pollInterval)
	defer cancel()
	if v, err := cl.Version(vctx); err == nil {
		s.mu.Lock()
		s.version = v
		s.mu.Unlock()
	}
}

// repairCreds re-applies vosd's API credentials after Sunshine rejected
// them (its state was reset, or the credentials file was replaced), at
// most every credsRetry.
func (s *Service) repairCreds(ctx context.Context) {
	s.mu.Lock()
	if s.now().Before(s.nextCredsFix) || s.creds.User == "" {
		s.mu.Unlock()
		return
	}
	s.nextCredsFix = s.now().Add(credsRetry)
	c := s.creds
	s.mu.Unlock()
	log.Printf("sunshine: API credentials rejected; setting them again")
	if err := s.applyCreds(ctx, c); err != nil {
		log.Printf("sunshine: setting API credentials: %v", err)
		return
	}
	if err := s.requestRestart(ctx); err != nil {
		log.Printf("sunshine: restart: %v", err)
	}
}

func (s *Service) onEvent(ev events.Event) {
	switch ev.Topic {
	case "session.begin":
		var si sessionInfo
		if json.Unmarshal(ev.Data, &si) == nil {
			s.mu.Lock()
			s.session = &si
			s.mu.Unlock()
		}
	case "session.end":
		s.mu.Lock()
		s.session = nil
		s.mu.Unlock()
	}
}

func (s *Service) cooldown() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cooldownUntil
}

func (s *Service) noteFollowError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && !s.followFailed {
		log.Printf("sunshine: cannot follow the Sunshine journal (freeze watchdog off): %v", err)
	}
	s.followFailed = err != nil
}

// recoverFrozenStream ends a stream whose capture lost its plane and
// restarts Sunshine, so Moonlight reconnects to a working capture instead
// of showing a frozen frame until someone gives up.
func (s *Service) recoverFrozenStream(ctx context.Context) {
	log.Printf("sunshine: KMS capture lost its plane (repeated \"Couldn't get drm fb\"); ending the app and restarting Sunshine")
	if cl := s.api(); cl != nil {
		if err := cl.CloseApp(ctx); err != nil {
			log.Printf("sunshine: closing the app: %v", err)
		}
	}
	if err := s.restart(ctx); err != nil {
		log.Printf("sunshine: restart: %v", err)
	}
	s.mu.Lock()
	s.cooldownUntil = s.now().Add(watchCooldown)
	s.mu.Unlock()
	s.publish("system.message", map[string]string{
		"level": "warning",
		"text":  "The stream froze because the display capture lost its picture. VaporOS restarted Sunshine; reconnect from Moonlight.",
	})
}
