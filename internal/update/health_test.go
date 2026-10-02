package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

const mountinfo = `22 1 0:21 / / ro,relatime shared:1 - erofs /dev/sda2 ro,user_xattr
23 22 8:4 / /state rw,noatime shared:2 - ext4 /dev/sda4 rw
24 22 8:4 /var /var rw,noatime shared:2 - ext4 /dev/sda4 rw
25 22 0:30 / /etc rw,relatime shared:3 - overlay overlay rw,lowerdir=/etc,upperdir=/state/etc/upper,workdir=/state/etc/work,index=off
26 22 0:31 / /mnt/my\040disk rw shared:4 - ext4 /dev/sdb1 rw
`

func TestParseMountInfo(t *testing.T) {
	ms, err := parseMountInfo(strings.NewReader(mountinfo))
	if err != nil || len(ms) != 5 {
		t.Fatalf("%d mounts, %v", len(ms), err)
	}
	if ms[3].point != "/etc" || ms[3].fstype != "overlay" || ms[1].options != "rw,noatime" {
		t.Fatalf("parsed %+v", ms)
	}
	if ms[4].point != "/mnt/my disk" {
		t.Fatalf("escape: %q", ms[4].point)
	}
	if err := checkMounts(ms); err != nil {
		t.Fatal(err)
	}

	bad := map[string]string{
		"state read-only": strings.Replace(mountinfo, "/state rw,noatime", "/state ro,noatime", 1),
		"no state":        strings.Replace(mountinfo, "/state", "/other", 1),
		"etc not overlay": strings.Replace(mountinfo, "- overlay overlay", "- ext4 /dev/sda4", 1),
		// A later read-only mount on top of /state is the one that counts.
		"remounted ro": mountinfo + "27 22 8:4 / /state ro shared:5 - ext4 /dev/sda4 ro\n",
	}
	for name, text := range bad {
		ms, _ := parseMountInfo(strings.NewReader(text))
		if checkMounts(ms) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeHealth is a healthy machine; tests break parts of it.
type fakeHealth struct {
	mountsOK, pingOK, userOK, gpu, stream, counting, forced bool
	noFallback                                              bool       // nothing else would boot
	lan                                                     []lanState // what each probe sees; the last one repeats
	macs                                                    []string   // the network devices present

	probe func(lanMAC string) lanProbe // instead of lan and macs: a real probe

	ext        *store.BootReport // nil: no report (mode off)
	extErr     error
	healthyErr error

	mu      sync.Mutex
	probes  int
	lanMAC  string              // what the last probe was given
	healthy []*store.BootReport // AfterHealthy calls
	markers []string            // the trial-ok marker at each call ("-" when absent)
}

func (f *fakeHealth) env() healthEnv {
	return healthEnv{
		mounts: func() ([]mountInfo, error) {
			if !f.mountsOK {
				return nil, errors.New("no mountinfo")
			}
			return parseMountInfo(strings.NewReader(mountinfo))
		},
		ping: func(context.Context) error {
			if !f.pingOK {
				return errors.New("connection refused")
			}
			return nil
		},
		unitActive: func(_ context.Context, unit string, user bool) bool {
			if user {
				return unit == sunshineUnit && f.stream
			}
			return unit == "user@1000.service" && f.userOK
		},
		gpu:      func() bool { return f.gpu },
		counting: func() bool { return f.counting },
		fallback: func() bool { return !f.noFallback },
		forced:   func() bool { return f.forced },
		lan: func(lanMAC string) lanProbe {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.probes++
			f.lanMAC = lanMAC
			if f.probe != nil {
				return f.probe(lanMAC)
			}
			p := lanProbe{state: lanNoCarrier, macs: f.macs}
			if len(f.lan) > 0 {
				p.state = f.lan[min(f.probes, len(f.lan))-1]
			}
			switch p.state {
			case lanUp:
				if len(f.macs) > 0 {
					p.mac = f.macs[0]
				}
			case lanNoAddress:
				p.detail = "enp5s0 has a link but no address"
			}
			return p
		},
		extensions: func() (*store.BootReport, error) {
			if f.ext == nil && f.extErr == nil {
				return &store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoReport}, nil
			}
			return f.ext, f.extErr
		},
		healthy: func(rep *store.BootReport) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.healthy = append(f.healthy, rep)
			m, err := os.ReadFile(config.ExtTrialOKPath())
			if err != nil {
				m = []byte("-")
			}
			f.markers = append(f.markers, string(m))
			return f.healthyErr
		},
	}
}

const testMAC = "52:54:00:12:34:56"

func healthy() *fakeHealth {
	return &fakeHealth{mountsOK: true, pingOK: true, userOK: true, gpu: true, stream: true,
		lan: []lanState{lanUp}, macs: []string{testMAC}}
}

func runFake(t *testing.T, f *fakeHealth) (int, string) {
	t.Helper()
	healthPoll, pingTimeout, lanTimeout = 5*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var log strings.Builder
	code := runHealth(ctx, f.env(), func(format string, a ...any) {
		log.WriteString(strings.TrimSpace(format) + "\n")
		_ = a
	})
	return code, log.String()
}

func healthOK(t *testing.T) HealthOK {
	t.Helper()
	var h HealthOK
	if err := config.ReadJSON(config.HealthOKPath(), &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHealthOK(t *testing.T) {
	e := setup(t)
	e.setState(&State{Failed: []string{bootedVersion, "x"}, Staged: &Staged{Version: bootedVersion, Slot: "a"}})
	code, log := runFake(t, healthy())
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, log)
	}
	if h := healthOK(t); !h.GPU || !h.Stream || !h.LAN || h.LANMAC != testMAC {
		t.Fatalf("health-ok %+v", h)
	}
	// Running and healthy: no longer failed, no longer merely staged.
	st := e.state()
	if st.HasFailed(bootedVersion) || !st.HasFailed("x") || st.Staged != nil {
		t.Fatalf("state %+v", st)
	}
}

func TestHealthFailsOnTrial(t *testing.T) {
	cases := map[string]func(f *fakeHealth){
		"filesystems": func(f *fakeHealth) { f.mountsOK = false },
		"vosd":        func(f *fakeHealth) { f.pingOK = false },
		"user unit":   func(f *fakeHealth) { f.userOK = false },
		"gpu":         func(f *fakeHealth) { f.gpu = false },
		"stream":      func(f *fakeHealth) { f.stream = false },
		"lan":         func(f *fakeHealth) { f.lan = []lanState{lanNoAddress} },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.write(config.HealthOKPath(), `{"gpu":true,"stream":true,"lan":true}`)
			f := healthy()
			f.counting = true
			breakIt(f)
			if code, log := runFake(t, f); code != 1 {
				t.Fatalf("exit %d\n%s", code, log)
			}
			// The baseline is untouched while on trial.
			if h := healthOK(t); !h.GPU || !h.Stream || !h.LAN {
				t.Fatalf("health-ok %+v", h)
			}
		})
	}
}

// Without a GPU on the last good boot, a missing GPU is fine.
func TestHealthGPUOnlyIfSeenBefore(t *testing.T) {
	setup(t)
	f := healthy()
	f.counting, f.gpu, f.stream = true, false, false
	if code, log := runFake(t, f); code != 0 {
		t.Fatalf("exit %d\n%s", code, log)
	}
	if h := healthOK(t); h.GPU || h.Stream {
		t.Fatalf("health-ok %+v", h)
	}
}

// A blessed entry cannot fall back: report, re-baseline, and do not fail
// the boot into an endless reboot loop.
func TestHealthDegradedWithoutTrial(t *testing.T) {
	e := setup(t)
	e.write(config.HealthOKPath(), `{"gpu":true,"stream":true}`)
	f := healthy()
	f.gpu = false // the GPU was taken out
	code, log := runFake(t, f)
	if code != 0 || !strings.Contains(log, "degraded") {
		t.Fatalf("exit %d\n%s", code, log)
	}
	if h := healthOK(t); h.GPU || !h.Stream {
		t.Fatalf("health-ok %+v", h)
	}
}

func TestHealthForced(t *testing.T) {
	setup(t)
	f := healthy()
	f.forced, f.counting = true, true
	if code, _ := runFake(t, f); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(config.HealthOKPath()); !os.IsNotExist(err) {
		t.Fatal("health-ok written on a forced failure")
	}
}

// The test knob on a blessed entry must not reboot into it forever
// (FailureAction=reboot): it only fails a boot that is on trial.
func TestHealthForcedUncounted(t *testing.T) {
	setup(t)
	f := healthy()
	f.forced = true
	code, log := runFake(t, f)
	if code != 0 || !strings.Contains(log, "not on trial") || !strings.Contains(log, "degraded") {
		t.Fatalf("exit %d\n%s", code, log)
	}
}

// A counted boot with nothing behind it (the last entry systemd-boot has
// left) would boot the same image again: report, do not fail.
func TestHealthNoFallback(t *testing.T) {
	e := setup(t)
	e.write(config.HealthOKPath(), `{"gpu":true,"stream":true}`)
	f := healthy()
	f.counting, f.noFallback, f.gpu = true, true, false
	code, log := runFake(t, f)
	if code != 0 || !strings.Contains(log, "no other entry") {
		t.Fatalf("exit %d\n%s", code, log)
	}
}

// hasFallback follows systemd-boot's order: an entry with tries left, or
// among the exhausted ones the newest (the same version counts).
func TestHasFallback(t *testing.T) {
	cases := []struct {
		other string // slot b's entry name ("" = none); slot a runs bootedVersion
		want  bool
	}{
		{"vos-" + oldIdleVersion + ".conf", true},
		{"vos-" + oldIdleVersion + "+2-1.conf", true},
		{"vos-" + oldIdleVersion + "+0-1.conf", false}, // older and bad: the failing entry sorts first
		{"vos-" + newVersion + "+0-3.conf", true},      // newer and bad: it sorts first
		{"vos-" + bootedVersion + "+0-1.conf", true},   // same version, fewer tries done
		{"", false},
	}
	for _, c := range cases {
		e := setup(t)
		b := e.entry("b")
		if c.other == "" {
			e.must(os.Remove(b.Path))
		} else {
			text, err := os.ReadFile(b.Path)
			e.must(err)
			version := strings.TrimPrefix(strings.SplitN(strings.TrimSuffix(c.other, ".conf"), "+", 2)[0], "vos-")
			e.must(os.Remove(b.Path))
			e.write(filepath.Join(filepath.Dir(b.Path), c.other),
				strings.Replace(string(text), "version "+oldIdleVersion, "version "+version, 1))
		}
		a := e.entry("a")
		e.must(os.Rename(a.Path, strings.TrimSuffix(a.Path, ".conf")+"+0-3.conf"))
		if got := hasFallback(); got != c.want {
			t.Errorf("slot b %q: hasFallback() = %v, want %v", c.other, got, c.want)
		}
	}
}

func TestSupportedGPU(t *testing.T) {
	setup(t)
	old := DRMClassDir
	DRMClassDir = t.TempDir()
	defer func() { DRMClassDir = old }()
	card := func(name, driver string) {
		dev := filepath.Join(DRMClassDir, name, "device")
		os.MkdirAll(dev, 0o755)
		os.Symlink(filepath.Join("drivers", driver), filepath.Join(dev, "driver"))
	}
	card("card0", "virtio-pci")
	card("card0-Virtual-1", "amdgpu") // connectors do not count
	if supportedGPU() {
		t.Fatal("virtio counted as supported")
	}
	card("card1", "amdgpu")
	if !supportedGPU() {
		t.Fatal("amdgpu not found")
	}
}

func TestBootCounting(t *testing.T) {
	e := setup(t)
	if bootCounting() {
		t.Fatal("a blessed entry counts as on trial")
	}
	e.write(config.BootCountVar, "x")
	if !bootCounting() {
		t.Fatal("LoaderBootCountPath ignored")
	}
	os.Remove(config.BootCountVar)
	a := e.entry("a")
	e.must(os.Rename(a.Path, strings.TrimSuffix(a.Path, ".conf")+"+2-1.conf"))
	if !bootCounting() {
		t.Fatal("counting entry ignored")
	}
}

// trialReport is the boot report of an extension trial.
func trialReport() *store.BootReport {
	return &store.BootReport{Mode: store.ModePending, Set: "3", TriesLeft: 1,
		Mounted: []store.Mounted{{ID: "proton", SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64)}}}
}

// An extension trial fails like a counted boot with a fallback: the
// initramfs boots the enabled set once the trial's tries are used up.
func TestHealthExtensionTrialFails(t *testing.T) {
	e := setup(t)
	e.write(config.HealthOKPath(), `{"gpu":true,"stream":true}`)
	f := healthy()
	f.stream = false
	f.ext = trialReport()
	code, log := runFake(t, f)
	if code != 1 || !strings.Contains(log, "tries new extensions") {
		t.Fatalf("exit %d\n%s", code, log)
	}
	if h := healthOK(t); !h.Stream || h.LAN {
		t.Fatalf("health-ok rewritten on a failed trial: %+v", h)
	}
	if len(f.healthy) != 0 {
		t.Fatalf("a failed trial was recorded as good: %v", f.healthy)
	}
	if _, err := os.Stat(config.ExtTrialOKPath()); !os.IsNotExist(err) {
		t.Fatalf("trial-ok marker on a failed trial: %v", err)
	}
}

func TestHealthExtensionTrialForced(t *testing.T) {
	setup(t)
	f := healthy()
	f.forced = true
	f.ext = trialReport()
	if code, log := runFake(t, f); code != 1 {
		t.Fatalf("exit %d\n%s", code, log)
	}
	if _, err := os.Stat(config.HealthOKPath()); !os.IsNotExist(err) {
		t.Fatal("health-ok written on a forced failure")
	}
	if len(f.healthy) != 0 {
		t.Fatal("a forced failure was recorded as good")
	}
}

// A good boot hands its report to the store (proven, promotion), and a
// store problem never fails the boot.
func TestHealthExtensionsRecorded(t *testing.T) {
	for _, mode := range []string{store.ModePending, store.ModeEnabled, store.ModeOSTrial} {
		t.Run(mode, func(t *testing.T) {
			setup(t)
			f := healthy()
			f.ext = trialReport()
			f.ext.Mode = mode
			f.healthyErr = errors.New("disk full")
			code, log := runFake(t, f)
			if code != 0 || !strings.Contains(log, "recording this good boot") {
				t.Fatalf("exit %d\n%s", code, log)
			}
			if len(f.healthy) != 1 || f.healthy[0] != f.ext {
				t.Fatalf("AfterHealthy calls: %v", f.healthy)
			}
			if h := healthOK(t); !h.GPU {
				t.Fatalf("health-ok %+v", h)
			}
			// A passed trial is on record before the store is touched, so
			// vosd can promote it should health not get the lock.
			want := "-"
			if mode == store.ModePending {
				want = "3\n"
			}
			if f.markers[0] != want {
				t.Fatalf("trial-ok marker %q when recording, want %q", f.markers[0], want)
			}
		})
	}

	// A trial that names no set leaves vosd nothing to promote: no marker.
	setup(t)
	f := healthy()
	f.ext = &store.BootReport{Mode: store.ModePending, Set: ""}
	if code, log := runFake(t, f); code != 0 || len(f.healthy) != 1 || f.markers[0] != "-" {
		t.Fatalf("exit %d, AfterHealthy calls %v, markers %q\n%s", code, f.healthy, f.markers, log)
	}
	if _, err := os.Stat(config.ExtTrialOKPath()); !os.IsNotExist(err) {
		t.Fatalf("trial-ok marker for a trial without a set: %v", err)
	}

	// Nothing mounted on purpose (or no report): nothing to record.
	setup(t)
	f = healthy()
	if code, _ := runFake(t, f); code != 0 || len(f.healthy) != 0 {
		t.Fatalf("exit %d, AfterHealthy calls %v", code, f.healthy)
	}
}

// An unreadable report is not a trial: the forced failure of a blessed
// boot stays degraded.
func TestHealthExtensionReportUnreadable(t *testing.T) {
	setup(t)
	f := healthy()
	f.forced = true
	f.extErr = errors.New("unexpected EOF")
	code, log := runFake(t, f)
	if code != 0 || !strings.Contains(log, "not treated as a trial") || !strings.Contains(log, "degraded") {
		t.Fatalf("exit %d\n%s", code, log)
	}
}

func TestHealthLAN(t *testing.T) {
	const other = "52:54:00:aa:bb:cc"
	cases := []struct {
		name     string
		prevLAN  bool
		prevMAC  string
		lan      []lanState
		macs     []string
		code     int
		seenLAN  bool
		seenMAC  string
		minProbe int // at least this many probes
	}{
		{name: "up", prevLAN: true, lan: []lanState{lanUp}, macs: []string{testMAC}, seenLAN: true, seenMAC: testMAC},
		{name: "comes up", prevLAN: true, lan: []lanState{lanNoCarrier, lanNoAddress, lanNoAddress, lanUp},
			macs: []string{testMAC}, seenLAN: true, seenMAC: testMAC, minProbe: 4},
		// A device down, or with a link and no address, at the deadline.
		{name: "no address", prevLAN: true, lan: []lanState{lanNoAddress}, macs: []string{testMAC}, code: 1},
		// Nothing plugged in: inconclusive, so it passes and keeps the baseline.
		{name: "no carrier", prevLAN: true, prevMAC: testMAC, lan: []lanState{lanNoCarrier}, macs: []string{testMAC},
			seenLAN: true, seenMAC: testMAC},
		{name: "no carrier, no device known", prevLAN: true, lan: []lanState{lanNoCarrier}, seenLAN: true},
		// The device that had the LAN is gone (a driver the image lost).
		{name: "device gone", prevLAN: true, prevMAC: other, lan: []lanState{lanNoCarrier}, macs: []string{testMAC}, code: 1},
		{name: "device gone, no other", prevLAN: true, prevMAC: other, lan: []lanState{lanNoCarrier}, code: 1},
		// Another device has the LAN now: up is up.
		{name: "device replaced", prevLAN: true, prevMAC: other, lan: []lanState{lanUp}, macs: []string{testMAC},
			seenLAN: true, seenMAC: testMAC},
		{name: "first seen", lan: []lanState{lanUp}, macs: []string{testMAC}, seenLAN: true, seenMAC: testMAC},
		{name: "not required", lan: []lanState{lanNoAddress}},
		{name: "not required, device gone", prevMAC: other, lan: []lanState{lanNoCarrier}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			e.write(config.HealthOKPath(), fmt.Sprintf(`{"gpu":true,"stream":true,"lan":%v,"lan_mac":%q}`, c.prevLAN, c.prevMAC))
			f := healthy()
			f.counting = true
			f.lan, f.macs = c.lan, c.macs
			code, log := runFake(t, f)
			if code != c.code {
				t.Fatalf("exit %d, want %d\n%s", code, c.code, log)
			}
			if f.probes < c.minProbe {
				t.Fatalf("%d probes", f.probes)
			}
			if c.code == 0 {
				if h := healthOK(t); h.LAN != c.seenLAN || h.LANMAC != c.seenMAC {
					t.Fatalf("health-ok %+v, want lan %v %q", h, c.seenLAN, c.seenMAC)
				}
			}
		})
	}
}
