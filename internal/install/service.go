package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/storage"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Job states (GET /install/status).
const (
	StateIdle    = "idle"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// Status is GET /install/status.
type Status struct {
	State   string `json:"state"`
	Step    string `json:"step"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// progressEvent is the install.progress event.
type progressEvent struct {
	Step    string `json:"step"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	State   string `json:"state"`
}

// Service is the web installer: it probes the machine and runs one install
// job at a time in the background, reporting over SSE and the serial line.
type Service struct {
	env *env

	// Swappable for tests.
	install     func(ctx context.Context, opts Options, p Progress) error
	reboot      func(ctx context.Context) error
	rebootDelay time.Duration // lets the reboot response reach the browser
	publish     func(topic string, data any)

	mu     sync.Mutex
	status Status

	// scanMu serialises disk scans and library detection (both mount
	// filesystems) with the start of an install.
	scanMu    sync.Mutex
	libCache  map[string][]SteamLibrary
	hostnames map[string]string // installedHostname's answers, per vos_data
	lastScan  []storage.Disk    // the last disk scan, reused while an install runs
}

// NewService creates the installer service. The live system's machine
// configuration plays no part in an install; the target gets its own.
func NewService(cfg *config.Config) *Service {
	s := &Service{
		env:         defaultEnv(),
		reboot:      sysd.Reboot,
		rebootDelay: time.Second,
		publish:     events.Publish,
		status:      Status{State: StateIdle},
		libCache:    map[string][]SteamLibrary{},
		hostnames:   map[string]string{},
	}
	s.install = func(ctx context.Context, opts Options, p Progress) error {
		return runInstall(ctx, s.env, opts, p)
	}
	return s
}

// Routes registers /install/* (Setup access).
func (s *Service) Routes(srv *api.Server) {
	srv.Handle("GET", "/install/probe", api.Setup, s.handleProbe)
	srv.Handle("POST", "/install", api.Setup, s.handleInstall)
	srv.Handle("GET", "/install/status", api.Setup, s.handleStatus)
	srv.Handle("POST", "/install/reboot", api.Setup, s.handleReboot)
}

func (s *Service) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.State == StateRunning
}

// handleProbe is GET /install/probe. The optional ?source= and ?channel=
// are the image the wizard would install (as POST /install takes them);
// min_size in the answer is sized for that image.
func (s *Service) handleProbe(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := Options{Source: q.Get("source"), Channel: q.Get("channel")}
	if err := o.normalizeSource(); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s.probe(r.Context(), o.Source, o.Channel))
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handleInstall(w http.ResponseWriter, r *http.Request) {
	var opts Options
	if err := api.ReadJSON(r, &opts); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	// Held across validation and the switch to running: a library scan
	// either finishes (and unmounts) first, or sees the job and mounts
	// nothing, so it can never make the target look busy.
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.running() {
		api.Error(w, http.StatusConflict, "an installation is already running")
		return
	}
	if err := validateRequest(&opts); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	job := newJobID()
	s.mu.Lock()
	s.status = Status{State: StateRunning, Step: StepProbe, Message: "Starting the installation"}
	st := s.status
	s.mu.Unlock()

	s.publish("install.progress", eventOf(st))
	go s.runJob(opts)
	api.WriteJSON(w, http.StatusAccepted, map[string]string{"job": job})
}

// validateRequest checks what can be checked at once, so the wizard gets a
// 400 with a reason instead of a job that fails a second later. The job's
// probe step checks everything again.
func validateRequest(opts *Options) error {
	if err := opts.normalize(); err != nil {
		return err
	}
	disk, err := resolveDisk(opts.Disk)
	if err != nil {
		return err
	}
	if err := checkTarget(disk); err != nil {
		return err
	}
	if opts.Mode == ModeRepair {
		if _, err := vosLayout(disk); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runJob(opts Options) {
	err := s.install(context.Background(), opts, s.onProgress)

	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	if err != nil {
		st.State, st.Error = StateFailed, err.Error()
		st.Message = "Installation failed: " + err.Error()
		log.Printf("install: failed: %v", err)
	} else {
		st.State, st.Step, st.Percent, st.Error = StateDone, StepDone, 100, ""
		log.Printf("install: %s", st.Message)
	}
	// A repair may have renamed the machine. Probes mount nothing until
	// the state changes, so the next one reads the name again.
	s.scanMu.Lock()
	clear(s.hostnames)
	s.scanMu.Unlock()
	// The harness line goes out before the state changes: whoever sees
	// done or failed (the UI, then a reboot) sees a finished job.
	serialLine(fmt.Sprintf("VOS-INSTALL state=%s message=%s", st.State, oneLine(st.Message)))
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
	s.publish("install.progress", eventOf(st))
}

func (s *Service) onProgress(step string, percent int, message string) {
	s.mu.Lock()
	s.status.Step, s.status.Percent, s.status.Message = step, percent, message
	st := s.status
	s.mu.Unlock()
	s.publish("install.progress", eventOf(st))
	log.Printf("install: [%s %d%%] %s", step, percent, message)
}

func eventOf(st Status) progressEvent {
	return progressEvent{Step: st.Step, Percent: st.Percent, Message: st.Message, State: st.State}
}

func (s *Service) handleReboot(w http.ResponseWriter, r *http.Request) {
	if s.running() {
		api.Error(w, http.StatusConflict, "an installation is running")
		return
	}
	api.OK(w)
	delay := s.rebootDelay
	go func() {
		time.Sleep(delay)
		if err := s.reboot(context.Background()); err != nil {
			log.Printf("install: reboot: %v", err)
		}
	}()
}

func newJobID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// serialLine writes a harness line to the serial console, if there is
// one. Non-blocking: a console nobody reads must not hang the installer.
func serialLine(line string) {
	f, err := os.OpenFile(paths.Serial, os.O_WRONLY|os.O_APPEND|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
