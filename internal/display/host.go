package display

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// host is everything the Manager does to the world outside its own files:
// systemd units, sysfs and DRM, gamescope's control tools and activity
// probes. The real implementation shells out and issues ioctls; the policy
// tests substitute a fake.
type host interface {
	UnitActive(ctx context.Context, unit string, user bool) bool
	StartUnit(ctx context.Context, unit string, user bool) error
	StopUnit(ctx context.Context, unit string, user bool) error
	RestartUnit(ctx context.Context, unit string, user bool) error

	GPU() GPUInfo
	Connectors(card string) []drm.SysConnector
	// ConnectorModes lists the modes the kernel offers on a connector
	// (nil when it cannot tell, e.g. without DRM).
	ConnectorModes(card, name string) []edid.Mode
	// Scanout reports the mode a connector currently scans out with a
	// framebuffer; err != nil means the state cannot be observed at all.
	Scanout(card, name string) (mode edid.Mode, active bool, err error)

	Gamescopectl(ctx context.Context, args ...string) (string, error)
	Xprop(ctx context.Context, args ...string) error
	Busy(ctx context.Context) (bool, string)
	LocalIPs() []string
	Hotplug(ctx context.Context) <-chan struct{}
	// OwnByGamer hands a file or directory to the gaming user.
	OwnByGamer(path string) error
}

// realHost is the production host.
type realHost struct {
	mu    sync.Mutex
	cards map[string]*drm.Card // read-only observers, by device path

	idOnce   sync.Once
	uid, gid int
}

func newRealHost() *realHost { return &realHost{cards: map[string]*drm.Card{}} }

// cmdTimeout bounds every systemctl/gamescopectl/xprop call.
const cmdTimeout = 30 * time.Second

func (h *realHost) UnitActive(ctx context.Context, unit string, user bool) bool {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	return sysd.IsActive(ctx, unit, user)
}

func (h *realHost) systemctl(ctx context.Context, user bool, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	if user {
		return sysd.UserSystemctl(ctx, args...)
	}
	return sysd.Systemctl(ctx, args...)
}

func (h *realHost) StartUnit(ctx context.Context, unit string, user bool) error {
	return h.systemctl(ctx, user, "start", unit)
}

func (h *realHost) StopUnit(ctx context.Context, unit string, user bool) error {
	return h.systemctl(ctx, user, "stop", unit)
}

func (h *realHost) RestartUnit(ctx context.Context, unit string, user bool) error {
	return h.systemctl(ctx, user, "restart", unit)
}

func (h *realHost) GPU() GPUInfo { return Probe() }

func (h *realHost) Connectors(card string) []drm.SysConnector {
	c, _ := drm.Connectors(card)
	return c
}

// observer returns a DRM fd for read-only queries. It is opened once and
// kept: the kernel only hands master to a card's first opener, so opening
// while nobody holds master would make us master for an instant and could
// make gamescope's own SET_MASTER fail. Keeping one early fd (which drops
// master immediately) avoids reopening at such moments.
func (h *realHost) observer(card string) (*drm.Card, error) {
	if card == "" {
		return nil, errors.New("no DRM card")
	}
	dev := card
	if !filepath.IsAbs(dev) {
		dev = filepath.Join(drm.DevDRI, card)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.cards[dev]; c != nil {
		return c, nil
	}
	c, err := drm.Open(dev, false)
	if err != nil {
		return nil, err
	}
	c.SetClientCap(drm.CapUniversalPlanes, 1)
	h.cards[dev] = c
	return c, nil
}

func (h *realHost) ConnectorModes(card, name string) []edid.Mode {
	c, err := h.observer(card)
	if err != nil {
		return nil
	}
	conn, err := c.FindConnector(name)
	if err != nil {
		return nil
	}
	seen := map[edid.Mode]bool{}
	var out []edid.Mode
	for i := range conn.Modes {
		mi := &conn.Modes[i]
		m := edid.Mode{W: int(mi.HDisplay), H: int(mi.VDisplay), Refresh: mi.Refresh()}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func (h *realHost) Scanout(card, name string) (edid.Mode, bool, error) {
	c, err := h.observer(card)
	if err != nil {
		return edid.Mode{}, false, err
	}
	s, err := c.ScanoutOf(name)
	if err != nil {
		return edid.Mode{}, false, err
	}
	m := edid.Mode{W: int(s.Mode.HDisplay), H: int(s.Mode.VDisplay), Refresh: s.Mode.Refresh()}
	return m, s.Active, nil
}

func (h *realHost) Gamescopectl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	full := append([]string{"GAMESCOPE_WAYLAND_DISPLAY=" + waylandDisplay(), "gamescopectl"}, args...)
	return sysd.AsGamer(ctx, "env", full...)
}

func (h *realHost) Xprop(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	full := append([]string{"DISPLAY=" + xDisplay(), "xprop"}, args...)
	_, err := sysd.AsGamer(ctx, "env", full...)
	return err
}

func (h *realHost) Busy(ctx context.Context) (bool, string) { return gamerBusy(ctx) }

func (h *realHost) LocalIPs() []string { return sysd.LocalIPs() }

func (h *realHost) Hotplug(ctx context.Context) <-chan struct{} {
	ch, err := drm.WatchHotplug(ctx)
	if err != nil {
		return nil
	}
	return ch
}

// gamerIDs resolves the gaming user's uid and gid once (falling back to
// the fixed uid 1000 = gid 1000 that sysusers assigns).
func (h *realHost) gamerIDs() (int, int) {
	h.idOnce.Do(func() {
		h.uid, h.gid = config.GamerUID, config.GamerUID
		if u, err := user.Lookup(config.GamerUser); err == nil {
			if n, err := strconv.Atoi(u.Uid); err == nil {
				h.uid = n
			}
			if n, err := strconv.Atoi(u.Gid); err == nil {
				h.gid = n
			}
		}
	})
	return h.uid, h.gid
}

func (h *realHost) OwnByGamer(path string) error {
	if os.Geteuid() != 0 {
		return nil // development: we already own everything we write
	}
	uid, gid := h.gamerIDs()
	return os.Lchown(path, uid, gid)
}
