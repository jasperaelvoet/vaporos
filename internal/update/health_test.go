package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
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
		forced:   func() bool { return f.forced },
	}
}

func healthy() *fakeHealth {
	return &fakeHealth{mountsOK: true, pingOK: true, userOK: true, gpu: true, stream: true}
}

func runFake(t *testing.T, f *fakeHealth) (int, string) {
	t.Helper()
	healthPoll, pingTimeout = 5*time.Millisecond, 50*time.Millisecond
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
	if h := healthOK(t); !h.GPU || !h.Stream {
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
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.write(config.HealthOKPath(), `{"gpu":true,"stream":true}`)
			f := healthy()
			f.counting = true
			breakIt(f)
			if code, log := runFake(t, f); code != 1 {
				t.Fatalf("exit %d\n%s", code, log)
			}
			// The baseline is untouched while on trial.
			if h := healthOK(t); !h.GPU || !h.Stream {
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
	f.forced = true
	if code, _ := runFake(t, f); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(config.HealthOKPath()); !os.IsNotExist(err) {
		t.Fatal("health-ok written on a forced failure")
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
	e.write(BootCountVar, "x")
	if !bootCounting() {
		t.Fatal("LoaderBootCountPath ignored")
	}
	os.Remove(BootCountVar)
	a := e.entry("a")
	e.must(os.Rename(a.Path, strings.TrimSuffix(a.Path, ".conf")+"+2-1.conf"))
	if !bootCounting() {
		t.Fatal("counting entry ignored")
	}
}
