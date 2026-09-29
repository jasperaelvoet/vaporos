package update

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// `vos health` gates a new image (docs/CONTRACTS.md "Health"). It runs
// before boot-complete.target; if it fails, OnFailure= reboots, and
// systemd-boot's counting falls back to the previous slot after three
// tries. The GPU and streaming checks apply only if they passed on the
// previous good boot (health-ok), so a machine without a GPU is healthy.

var (
	MountInfoPath = "/proc/self/mountinfo"
	// BootCountVar exists while systemd-boot is counting this boot, i.e.
	// the entry is still on trial (systemd-bless-boot reads it too).
	BootCountVar = "/sys/firmware/efi/efivars/LoaderBootCountPath-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

	healthTimeout = 90 * time.Second // the whole check
	pingTimeout   = 60 * time.Second // vosd must answer within this
	healthPoll    = time.Second

	// DRMClassDir and GPUDrivers decide whether a supported GPU is present.
	// The display package owns GPU profiles; this deliberately small copy
	// keeps update from importing display, which may want update's state.
	DRMClassDir = "/sys/class/drm"
	GPUDrivers  = []string{"amdgpu"}
)

const sunshineUnit = "vos-sunshine.service"

// HealthOK is /var/lib/vos/health-ok: what worked on the last good boot.
type HealthOK struct {
	GPU    bool `json:"gpu"`
	Stream bool `json:"stream"`
}

// healthEnv is everything a health check looks at, so tests can fake it.
type healthEnv struct {
	mounts     func() ([]mountInfo, error)
	ping       func(ctx context.Context) error
	unitActive func(ctx context.Context, unit string, user bool) bool
	gpu        func() bool
	counting   func() bool // is this boot on trial (boot counting)?
	forced     func() bool // vos.health.fail=1
}

func systemHealthEnv() healthEnv {
	return healthEnv{
		mounts: func() ([]mountInfo, error) {
			f, err := os.Open(MountInfoPath)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return parseMountInfo(f)
		},
		ping:       pingVosd,
		unitActive: sysd.IsActive,
		gpu:        supportedGPU,
		counting:   bootCounting,
		forced: func() bool {
			v, _ := config.KernelArg("vos.health.fail")
			return v == "1"
		},
	}
}

type healthResult struct {
	failures []string
	seen     HealthOK // what works now
}

// checkHealth runs every check within ctx's deadline, logging each line.
func checkHealth(ctx context.Context, env healthEnv, prev HealthOK, logf func(string, ...any)) healthResult {
	var res healthResult
	check := func(name string, err error) {
		if err != nil {
			res.failures = append(res.failures, name+": "+err.Error())
			logf("FAIL  %s: %v", name, err)
			return
		}
		logf("ok    %s", name)
	}
	// Polls f until it holds or the deadline passes.
	wait := func(ctx context.Context, f func() bool) bool {
		for {
			if f() {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(healthPoll):
			}
		}
	}

	ms, err := env.mounts()
	if err == nil {
		err = checkMounts(ms)
	}
	check("filesystems", err)

	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	var perr error
	if !wait(pctx, func() bool { perr = env.ping(pctx); return perr == nil }) {
		if perr == nil {
			perr = pctx.Err()
		}
		check("vosd", fmt.Errorf("no answer on /api/v1/ping: %w", perr))
	} else {
		check("vosd", nil)
	}
	cancel()

	userUnit := "user@" + strconv.Itoa(config.GamerUID) + ".service"
	if wait(ctx, func() bool { return env.unitActive(ctx, userUnit, false) }) {
		check(userUnit, nil)
	} else {
		check(userUnit, errors.New("not active"))
	}

	// GPU and stream: waited for only when they are required.
	if prev.GPU {
		res.seen.GPU = wait(ctx, env.gpu)
		if res.seen.GPU {
			check("gpu", nil)
		} else {
			check("gpu", errors.New("no supported GPU driver, but the last good boot had one"))
		}
	} else {
		res.seen.GPU = env.gpu()
	}
	streamUp := func() bool { return env.unitActive(ctx, sunshineUnit, true) }
	if prev.Stream {
		res.seen.Stream = wait(ctx, streamUp)
		if res.seen.Stream {
			check("stream", nil)
		} else {
			check("stream", fmt.Errorf("%s is not active, but it was on the last good boot", sunshineUnit))
		}
	} else {
		res.seen.Stream = streamUp()
	}

	if env.forced() {
		check("forced", errors.New("vos.health.fail=1 is on the kernel command line"))
	}
	return res
}

// runHealth decides the outcome of `vos health` and returns its exit code.
func runHealth(ctx context.Context, env healthEnv, logf func(string, ...any)) int {
	var prev HealthOK
	if err := config.ReadJSON(config.HealthOKPath(), &prev); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logf("health-ok: %v (checking the basics only)", err)
	}
	res := checkHealth(ctx, env, prev, logf)
	switch {
	case env.forced():
		logf("health: FAILED (forced)")
		return 1
	case len(res.failures) == 0:
		if err := config.WriteJSONAtomic(config.HealthOKPath(), res.seen, 0o644); err != nil {
			logf("health-ok: %v", err)
		}
		// This version demonstrably works: it is no longer "failed", and
		// no longer merely staged.
		modifyState(func(st *State) error {
			st.removeFailed(st.Booted)
			if st.Staged != nil && st.Staged.Version == st.Booted {
				st.Staged = nil
			}
			return nil
		})
		logf("health: ok")
		return 0
	case env.counting():
		logf("health: FAILED; this boot is on trial, so the next boot can fall back")
		return 1
	}
	// A blessed entry has no fallback: failing would reboot into the same
	// image forever. Report it and let the baseline follow what the
	// hardware does now (a GPU that was taken out stays out).
	if err := config.WriteJSONAtomic(config.HealthOKPath(), res.seen, 0o644); err != nil {
		logf("health-ok: %v", err)
	}
	logf("health: degraded (%s); not failing a boot that has nothing to fall back to", strings.Join(res.failures, "; "))
	return 0
}

// supportedGPU reports whether a DRM card is driven by a supported driver.
func supportedGPU() bool {
	cards, _ := filepath.Glob(filepath.Join(DRMClassDir, "card*"))
	for _, c := range cards {
		if strings.Contains(filepath.Base(c), "-") {
			continue // a connector (card0-DP-1), not a card
		}
		link, err := os.Readlink(filepath.Join(c, "device", "driver"))
		if err == nil && slices.Contains(GPUDrivers, filepath.Base(link)) {
			return true
		}
	}
	return false
}

// bootCounting reports whether this boot is still on trial: systemd-boot
// set LoaderBootCountPath, or the running slot's entry has a counter.
func bootCounting() bool {
	if _, err := os.Stat(BootCountVar); err == nil {
		return true
	}
	if boot.EnsureESP(config.ESP) != nil {
		return false
	}
	e, err := boot.EntryForSlot(config.ESP, config.BootedSlot())
	return err == nil && e != nil && e.Counting && e.Version == bootedImage().Version
}

func pingVosd(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u := fmt.Sprintf("http://127.0.0.1:%d/api/v1/ping", config.HTTPPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return errors.New(`"ok" is false`)
	}
	return nil
}

type mountInfo struct {
	point   string
	fstype  string
	options string // per-mount options (rw/ro, ...)
}

// parseMountInfo reads /proc/self/mountinfo:
// id parent maj:min root point options [tags...] - fstype source superoptions
func parseMountInfo(r io.Reader) ([]mountInfo, error) {
	var out []mountInfo
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := slices.Index(f, "-")
		if sep < 6 || sep+1 >= len(f) {
			continue
		}
		out = append(out, mountInfo{point: unescapeMount(f[4]), options: f[5], fstype: f[sep+1]})
	}
	return out, sc.Err()
}

// unescapeMount decodes the \ooo octal escapes mountinfo uses for spaces
// and other awkward characters.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// checkMounts: /state is mounted read-write and /etc is an overlay. The
// last mount of a mount point is the one in effect.
func checkMounts(ms []mountInfo) error {
	find := func(point string) (mountInfo, bool) {
		for i := len(ms) - 1; i >= 0; i-- {
			if ms[i].point == point {
				return ms[i], true
			}
		}
		return mountInfo{}, false
	}
	st, ok := find("/state")
	if !ok {
		return errors.New("/state is not mounted")
	}
	if !slices.Contains(strings.Split(st.options, ","), "rw") {
		return errors.New("/state is not mounted read-write")
	}
	if etc, ok := find("/etc"); !ok || etc.fstype != "overlay" {
		return errors.New("/etc is not an overlay")
	}
	return nil
}
