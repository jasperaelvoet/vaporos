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
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// `vos health` gates a new image (docs/CONTRACTS.md "Health"). It runs
// before boot-complete.target; if it fails, FailureAction= reboots, and
// systemd-boot's counting falls back to the previous slot after three
// tries. It only ever fails a boot that is counted and has a slot to fall
// back to, or an extension trial (the initramfs falls back to the enabled
// set once the trial's tries are used up): failing any other boot would
// reboot into the same entry forever. The GPU, streaming and LAN checks
// apply only if they passed on the previous good boot (health-ok), so a
// machine without a GPU is healthy.

var (
	MountInfoPath = "/proc/self/mountinfo"
	// BootCountVar exists while systemd-boot is counting this boot, i.e.
	// the entry is still on trial (systemd-bless-boot reads it too).
	BootCountVar = "/sys/firmware/efi/efivars/LoaderBootCountPath-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

	healthTimeout = 90 * time.Second // the whole check
	pingTimeout   = 60 * time.Second // vosd must answer within this
	lanTimeout    = 60 * time.Second // from the start of the check
	healthPoll    = time.Second
	extLockWait   = 30 * time.Second // for ext.lock, to promote a set

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
	LAN    bool `json:"lan"`
}

// healthEnv is everything a health check looks at, so tests can fake it.
type healthEnv struct {
	mounts     func() ([]mountInfo, error)
	ping       func(ctx context.Context) error
	unitActive func(ctx context.Context, unit string, user bool) bool
	gpu        func() bool
	counting   func() bool // is this boot on trial (boot counting)?
	fallback   func() bool // would another entry boot once this one runs out of tries?
	forced     func() bool // vos.health.fail=1
	lan        func() lanState
	extensions func() (*store.BootReport, error) // what the initramfs mounted
	// healthy records a boot that passed (store.AfterHealthy, under ext.lock).
	healthy func(rep *store.BootReport) error
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
		fallback:   hasFallback,
		forced: func() bool {
			v, _ := config.KernelArg("vos.health.fail")
			return v == "1"
		},
		lan:        probeLAN,
		extensions: store.LoadBootReport,
		healthy: func(rep *store.BootReport) error {
			ctx, cancel := context.WithTimeout(context.Background(), extLockWait)
			defer cancel()
			unlock, err := store.Lock(ctx)
			if err != nil {
				return err
			}
			defer unlock()
			return store.AfterHealthy(rep)
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

	// The LAN check runs alongside the others, from the start.
	lctx, lcancel := context.WithTimeout(ctx, lanTimeout)
	defer lcancel()
	lanc := make(chan lanState, 1)
	go func() {
		st := env.lan()
		for st != lanUp {
			select {
			case <-lctx.Done():
				lanc <- st
				return
			case <-time.After(healthPoll):
			}
			st = env.lan()
		}
		lanc <- st
	}()

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

	// LAN: waited for (up to lanTimeout) only when it is required; else
	// whatever it found by now.
	if !prev.LAN {
		lcancel()
	}
	switch st := <-lanc; {
	case st == lanUp:
		res.seen.LAN = true
		if prev.LAN {
			check("lan", nil)
		}
	case st == lanNoCarrier:
		// Nothing is plugged in: that says nothing about this image.
		res.seen.LAN = prev.LAN
		if prev.LAN {
			logf("ok    lan: no interface has a link; not checked")
		}
	case prev.LAN:
		check("lan", errors.New("an interface has a link but no address, but the last good boot had one"))
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
	rep, err := env.extensions()
	if err != nil {
		logf("extensions: %v (not treated as a trial)", err)
		rep = nil
	}
	trial := rep.IsTrial()
	if trial {
		logf("extensions: this boot tries set %s (%d tries left after it)", rep.Set, rep.TriesLeft)
	}
	res := checkHealth(ctx, env, prev, logf)
	if len(res.failures) == 0 {
		if err := config.WriteJSONAtomic(config.HealthOKPath(), res.seen, 0o644); err != nil {
			logf("health-ok: %v", err)
		}
		// This version demonstrably works: it is no longer "failed", no
		// longer merely staged, and a hold on it or anything older is moot.
		modifyState(func(st *State) error {
			st.removeFailed(st.Booted)
			if st.Staged != nil && st.Staged.Version == st.Booted {
				st.Staged = nil
			}
			if st.Held != nil && boot.CompareVersions(st.Held.Version, st.Booted) <= 0 {
				st.Held = nil
			}
			return nil
		})
		if rep != nil && rep.Mode != store.ModeOff {
			if err := env.healthy(rep); err != nil {
				logf("extensions: recording this good boot: %v", err)
			}
		}
		logf("health: ok")
		healthSerial("ok", nil)
		return 0
	}
	// Failing reboots, and only a counted entry with another one behind it,
	// or an extension trial, ever boots anything else. On any other boot (a
	// blessed entry, or the last entry systemd-boot has left) failing would
	// boot the same image forever: report it, and let the baseline follow
	// what the hardware does now (a GPU that was taken out stays out). The
	// test knob vos.health.fail=1 follows the same rule.
	switch {
	case env.counting() && env.fallback():
		logf("health: FAILED; this boot is on trial, so the next boot can fall back")
		healthSerial("failed", res.failures)
		return 1
	case trial:
		logf("health: FAILED; this boot tries new extensions, so the next boot can fall back")
		healthSerial("failed", res.failures)
		return 1
	case env.counting():
		logf("health: this boot is on trial, but no other entry would boot instead")
	case env.forced():
		logf("health: vos.health.fail=1 ignored: this boot is not on trial")
	}
	if err := config.WriteJSONAtomic(config.HealthOKPath(), res.seen, 0o644); err != nil {
		logf("health-ok: %v", err)
	}
	logf("health: degraded (%s); not failing a boot that has nothing to fall back to", strings.Join(res.failures, "; "))
	healthSerial("degraded", res.failures)
	return 0
}

// HealthSerial is where vos health announces its verdict for test harnesses
// (docs/CONTRACTS.md "Serial lines"); tests point it elsewhere.
var HealthSerial = "/dev/ttyS0"

// healthSerial writes "VOS-HEALTH result=<r> failures=<a; b|->" to the
// serial console, best effort: no serial port is not an error.
func healthSerial(result string, failures []string) {
	f, err := os.OpenFile(HealthSerial, os.O_WRONLY|os.O_APPEND|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	list := "-"
	if len(failures) > 0 {
		list = strings.Join(strings.Fields(strings.Join(failures, "; ")), " ")
	}
	fmt.Fprintf(f, "VOS-HEALTH result=%s failures=%s\n", result, list)
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

// hasFallback reports whether systemd-boot would start the other slot once
// the running entry has no tries left: when the other entry still has
// tries, or, among entries without, when it sorts first (it is newer, or
// the same version). When the ESP cannot be read it assumes so, as before.
func hasFallback() bool {
	if boot.EnsureESP(config.ESP) != nil {
		return true
	}
	booted := config.BootedSlot()
	cur, err1 := boot.EntryForSlot(config.ESP, booted)
	other, err2 := boot.EntryForSlot(config.ESP, config.OtherSlot(booted))
	switch {
	case err1 != nil || err2 != nil || cur == nil:
		return true
	case other == nil:
		return false
	}
	return other.Bootable() || boot.CompareVersions(other.Version, cur.Version) >= 0
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
