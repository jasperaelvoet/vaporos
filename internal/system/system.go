// Package system serves /system/* and /ssh: machine info, hostname,
// reboot/poweroff, and the SSH toggle.
package system

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

type Service struct {
	// cfg is the config shared with every service: read it with Snapshot
	// or View, change it only with Mutate.
	cfg *config.Config
	// mu serialises the SSH and hostname changes, so two requests never
	// interleave their unit and file side effects.
	mu sync.Mutex
	// powerPending is set once a reboot or poweroff has been accepted, so a
	// double click does not queue a second one.
	powerPending atomic.Bool

	// Seams for tests; NewService wires the real ones.
	run          func(ctx context.Context, name string, args ...string) error
	lookPath     func(file string) (string, error)
	setKernelHn  func(name string) error
	reboot       func(ctx context.Context) error
	poweroff     func(ctx context.Context) error
	powerDelay   time.Duration
	uid, gid     int // owner of ~vapor/.ssh
	probeGPU     func() display.GPUInfo
	localIPs     func() []string
	publishEvent func(topic string, data any)
}

func NewService(cfg *config.Config) *Service {
	if cfg == nil {
		cfg = config.Defaults()
	}
	return &Service{
		cfg: cfg,
		run: func(ctx context.Context, name string, args ...string) error {
			_, err := sysd.Run(ctx, name, args...)
			return err
		},
		lookPath:     exec.LookPath,
		setKernelHn:  setKernelHostname,
		reboot:       sysd.Reboot,
		poweroff:     sysd.Poweroff,
		powerDelay:   time.Second,
		uid:          config.GamerUID,
		gid:          config.GamerUID, // vapor's primary group has the same id
		probeGPU:     display.Probe,
		localIPs:     sysd.LocalIPs,
		publishEvent: events.Publish,
	}
}

func (s *Service) Routes(srv *api.Server) {
	srv.Handle("GET", "/system", api.Authed, s.handleInfo)
	srv.Handle("PUT", "/system/hostname", api.Authed, s.handleHostname)
	srv.Handle("POST", "/system/reboot", api.Authed, s.handlePower("reboot", "Restarting…", func(ctx context.Context) error { return s.reboot(ctx) }))
	srv.Handle("POST", "/system/poweroff", api.Authed, s.handlePower("poweroff", "Shutting down…", func(ctx context.Context) error { return s.poweroff(ctx) }))
	srv.Handle("GET", "/ssh", api.Authed, s.handleGetSSH)
	srv.Handle("PUT", "/ssh", api.Authed, s.handlePutSSH)
}

// Run brings ~vapor/.ssh/authorized_keys in line with config.json once at
// startup; the generator already started (or not) sshd for this boot. That
// makes config.json the one source of truth even if the file was edited.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncAuthorizedKeys(s.cfg.Snapshot().SSH); err != nil {
		log.Printf("system: syncing authorized_keys: %v", err)
	}
}

// ---- GET /system

// Info is the GET /system response.
type Info struct {
	Hostname   string          `json:"hostname"`
	Version    string          `json:"version"`
	Channel    string          `json:"channel"`
	BootedSlot string          `json:"booted_slot"`
	UptimeS    int64           `json:"uptime_s"`
	CPU        string          `json:"cpu"`
	GPU        display.GPUInfo `json:"gpu"`
	IPs        []string        `json:"ips"`
	MDNS       string          `json:"mdns"`
	Disk       DiskUsage       `json:"disk"`
	Temps      []Temp          `json:"temps"`
}

type DiskUsage struct {
	DataTotal uint64 `json:"data_total"`
	DataFree  uint64 `json:"data_free"`
}

type Temp struct {
	Name string  `json:"name"`
	C    float64 `json:"c"`
}

// Info gathers the machine summary. Every field degrades to its zero value
// rather than failing the whole request.
func (s *Service) Info() Info {
	hn := config.Hostname()
	ips := s.localIPs()
	if ips == nil {
		ips = []string{}
	}
	var disk DiskUsage
	if total, free, err := diskUsage(config.StateDir); err == nil {
		disk = DiskUsage{DataTotal: total, DataFree: free}
	}
	return Info{
		Hostname:   hn,
		Version:    Version(),
		Channel:    s.channel(),
		BootedSlot: config.BootedSlot(),
		UptimeS:    uptimeSeconds(),
		CPU:        cpuModel(),
		GPU:        s.probeGPU(),
		IPs:        ips,
		MDNS:       mdnsName(hn),
		Disk:       disk,
		Temps:      readTemps(hwmonRoot),
	}
}

func (s *Service) handleInfo(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, s.Info())
}

// Version is the running image's version: image.json, else os-release,
// else the binary's own (a dev build outside an image).
func Version() string {
	if ii, err := config.LoadImageInfo(); err == nil && ii.Version != "" {
		return ii.Version
	}
	osr := readOSRelease(config.OSReleasePath)
	for _, k := range []string{"IMAGE_VERSION", "VERSION_ID"} {
		if v := osr[k]; v != "" && !strings.HasPrefix(v, "@") {
			return v
		}
	}
	return config.BinaryVersion
}

// channel is the image's build channel, else the configured one.
func (s *Service) channel() string {
	if ii, err := config.LoadImageInfo(); err == nil && ii.Channel != "" {
		return ii.Channel
	}
	var ch string
	s.cfg.View(func(c *config.Config) { ch = c.Update.Channel })
	return ch
}

// mdnsName is what avahi announces: the first label of the hostname.
func mdnsName(hostname string) string {
	label, _, _ := strings.Cut(hostname, ".")
	if label == "" {
		return ""
	}
	return label + ".local"
}

// ---- reboot / poweroff

// handlePower answers first and acts a second later, so the browser gets
// its response (and can show "restarting…") before the network goes away.
func (s *Service) handlePower(what, message string, act func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.powerPending.CompareAndSwap(false, true) {
			api.OK(w) // already on its way
			return
		}
		log.Printf("system: %s requested from the web UI", what)
		s.publishEvent("system.message", map[string]string{"level": "info", "text": message})
		api.OK(w)
		_ = http.NewResponseController(w).Flush()
		go func() {
			time.Sleep(s.powerDelay)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := act(ctx); err != nil {
				log.Printf("system: %s: %v", what, err)
				s.powerPending.Store(false)
				s.publishEvent("system.message", map[string]string{"level": "error", "text": fmt.Sprintf("%s failed: %v", what, err)})
			}
		}()
	}
}
