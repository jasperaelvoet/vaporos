package coolercontrol

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// fakeSys is a sysfs in a temp dir: devices under devices/, their hwmon
// directories linked from class/hwmon as the kernel does.
type fakeSys struct {
	t    *testing.T
	root string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	root := t.TempDir()
	savedHwmon, savedRun := hwmonDir, config.RunDir
	t.Cleanup(func() { hwmonDir, config.RunDir = savedHwmon, savedRun })
	hwmonDir = filepath.Join(root, "class", "hwmon")
	config.RunDir = filepath.Join(root, "run", "vos")
	must(t, os.MkdirAll(hwmonDir, 0o755))
	return &fakeSys{t: t, root: root}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// chip adds hwmon<n> for device dev (a path under devices/, "" for a
// virtual chip without one) with fan control files pwm -> value.
func (f *fakeSys) chip(n, dev string, pwm map[string]string) {
	f.t.Helper()
	var dir string
	if dev == "" {
		dir = filepath.Join(f.root, "devices", "virtual", "hwmon", "hwmon"+n)
		must(f.t, os.MkdirAll(dir, 0o755))
	} else {
		dir = filepath.Join(f.root, "devices", dev, "hwmon", "hwmon"+n)
		must(f.t, os.MkdirAll(dir, 0o755))
		must(f.t, os.Symlink("../..", filepath.Join(dir, "device")))
	}
	must(f.t, os.Symlink(dir, filepath.Join(hwmonDir, "hwmon"+n)))
	must(f.t, os.WriteFile(filepath.Join(dir, "name"), []byte("chip\n"), 0o644))
	for name, v := range pwm {
		f.set(n, name, v)
		must(f.t, os.WriteFile(filepath.Join(dir, strings.TrimSuffix(name, "_enable")), []byte("128\n"), 0o644))
	}
}

// unplug removes hwmon<n>'s link, as unloading its driver does.
func (f *fakeSys) unplug(n string) {
	f.t.Helper()
	must(f.t, os.Remove(filepath.Join(hwmonDir, "hwmon"+n)))
}

func (f *fakeSys) set(n, name, v string) {
	f.t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Join(hwmonDir, "hwmon"+n))
	must(f.t, err)
	must(f.t, os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644))
}

func (f *fakeSys) get(n, name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(hwmonDir, "hwmon"+n, name))
	must(f.t, err)
	return strings.TrimSpace(string(b))
}

const gpu = "pci0000:00/0000:00:01.1/0000:03:00.0"

func TestFansRestoredAfterAStop(t *testing.T) {
	f := newFakeSys(t)
	f.chip("3", gpu, map[string]string{"pwm1_enable": "2"})
	f.chip("1", "", map[string]string{"pwm1_enable": "0"}) // a virtual chip: keyed by its own path
	must(t, snapshotFans())

	f.set("3", "pwm1_enable", "1") // CoolerControl takes over
	f.set("1", "pwm1_enable", "1")
	must(t, restoreFans())
	if f.get("3", "pwm1_enable") != "2" || f.get("1", "pwm1_enable") != "0" {
		t.Fatalf("restored: gpu %s, virtual %s", f.get("3", "pwm1_enable"), f.get("1", "pwm1_enable"))
	}
	if f.get("3", "pwm1") != "128" {
		t.Fatal("restore touched a fan's speed")
	}
	if fi, err := os.Stat(fansPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", fansPath(), fi, err)
	}
}

// A restart keeps the first values; a chip whose driver loads after the
// first start (detect --load on a later start, a hub plugged in) is added;
// a chip renumbered by a driver reload is still found by its device.
func TestFansFirstSeenAndLateDevices(t *testing.T) {
	f := newFakeSys(t)
	f.chip("3", gpu, map[string]string{"pwm1_enable": "2"})
	must(t, snapshotFans())

	// CoolerControl runs, crashes without its ExecStopPost having run yet;
	// meanwhile it87 appears.
	f.set("3", "pwm1_enable", "1")
	f.chip("4", "platform/it87.2608", map[string]string{"pwm1_enable": "5", "pwm2_enable": "2", "pwm10_enable": "0"})
	must(t, snapshotFans())
	st, err := loadFans()
	must(t, err)
	if len(st.Fans) != 4 {
		t.Fatalf("record %v", st.Fans)
	}
	for key, want := range map[string]string{"/" + gpu + "/pwm1_enable": "2", "/platform/it87.2608/pwm2_enable": "2", "/platform/it87.2608/pwm10_enable": "0"} {
		found := false
		for k, v := range st.Fans {
			if strings.HasSuffix(k, key) {
				found = true
				if v != want {
					t.Errorf("%s = %s, want %s (first seen)", k, v, want)
				}
			}
		}
		if !found {
			t.Errorf("no record for %s in %v", key, st.Fans)
		}
	}

	for _, p := range []string{"pwm1_enable", "pwm2_enable", "pwm10_enable"} {
		f.set("4", p, "1")
	}
	f.unplug("3")
	if err := os.Symlink(filepath.Join(f.root, "devices", gpu, "hwmon", "hwmon3"), filepath.Join(hwmonDir, "hwmon7")); err != nil {
		t.Fatal(err)
	}
	must(t, restoreFans())
	if got := f.get("7", "pwm1_enable"); got != "2" {
		t.Errorf("renumbered gpu fan: %s, want 2", got)
	}
	if got := f.get("4", "pwm1_enable") + f.get("4", "pwm2_enable") + f.get("4", "pwm10_enable"); got != "520" {
		t.Errorf("it87 fans: %s, want 5 2 0", got)
	}
}

func TestFansWithoutARecord(t *testing.T) {
	f := newFakeSys(t)
	f.chip("3", gpu, map[string]string{"pwm1_enable": "1"})
	must(t, restoreFans())
	if f.get("3", "pwm1_enable") != "1" {
		t.Fatal("restore without a record changed a fan")
	}
	// Values that are not a mode are neither recorded nor written.
	f.set("3", "pwm1_enable", "auto")
	must(t, snapshotFans())
	if _, err := os.Stat(fansPath()); err == nil {
		t.Fatal("recorded a fan whose mode could not be read")
	}
	must(t, os.MkdirAll(filepath.Dir(fansPath()), 0o755))
	dev, err := filepath.EvalSymlinks(filepath.Join(hwmonDir, "hwmon3", "device"))
	must(t, err)
	must(t, os.WriteFile(fansPath(), []byte(`{"fans":{"`+dev+`/pwm1_enable":"1; reboot"}}`), 0o600))
	must(t, restoreFans())
	if f.get("3", "pwm1_enable") != "auto" {
		t.Fatal("restore wrote a value that is not a mode")
	}
}

// writes records every sysfs write, as "<file name>=<value>", while still
// writing it.
func (f *fakeSys) writes() *[]string {
	f.t.Helper()
	var got []string
	saved := sysWrite
	f.t.Cleanup(func() { sysWrite = saved })
	sysWrite = func(path, value string) error {
		got = append(got, filepath.Base(path)+"="+value)
		return saved(path, value)
	}
	return &got
}

// A fan the firmware left in manual mode gets its duty back, after its
// mode; one at full speed (0) gets it before, while CoolerControl's manual
// mode still takes it; an automatic one never gets a duty.
func TestFansDutyRestored(t *testing.T) {
	f := newFakeSys(t)
	f.chip("4", "platform/nct6775.656", map[string]string{"pwm1_enable": "1", "pwm2_enable": "0", "pwm3_enable": "5"})
	f.set("4", "pwm1", "200")
	f.set("4", "pwm2", "255")
	must(t, snapshotFans())
	st, err := loadFans()
	must(t, err)
	if len(st.Duty) != 2 {
		t.Fatalf("duty record %v, want pwm1 and pwm2", st.Duty)
	}

	// CoolerControl keeps pwm1 manual at 50, takes pwm2 and pwm3 over.
	f.set("4", "pwm1", "50")
	f.set("4", "pwm2_enable", "1")
	f.set("4", "pwm2", "60")
	f.set("4", "pwm3_enable", "1")
	f.set("4", "pwm3", "70")
	w := f.writes()
	must(t, restoreFans())
	want := []string{"pwm1=200", "pwm2=255", "pwm2_enable=0", "pwm3_enable=5"}
	if !slices.Equal(*w, want) {
		t.Fatalf("writes %q, want %q", *w, want)
	}
	if f.get("4", "pwm1_enable")+" "+f.get("4", "pwm1") != "1 200" || f.get("4", "pwm3") != "70" {
		t.Fatalf("pwm1 %s/%s, pwm3 duty %s", f.get("4", "pwm1_enable"), f.get("4", "pwm1"), f.get("4", "pwm3"))
	}
	*w = nil
	must(t, restoreFans())
	if len(*w) != 0 {
		t.Fatalf("a second restore wrote %q", *w)
	}

	// A fan already at full speed is not touched for its duty.
	f.set("4", "pwm2", "10")
	must(t, restoreFans())
	if len(*w) != 0 {
		t.Fatalf("wrote %q to a fan in mode 0", *w)
	}
}

// curve gives the chip of device dev an amdgpu OverDrive fan curve that
// reads text; committing a reset makes it read text again.
func (f *fakeSys) curve(dev, text string) string {
	f.t.Helper()
	p := filepath.Join(f.root, "devices", dev, "gpu_od", "fan_ctrl", "fan_curve")
	must(f.t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(f.t, os.WriteFile(p, []byte(text), 0o644))
	p, err := filepath.EvalSymlinks(p) // as it is recorded: a temp dir may be behind a link
	must(f.t, err)
	saved := sysWrite
	f.t.Cleanup(func() { sysWrite = saved })
	reset := false
	sysWrite = func(path, value string) error {
		if path != p {
			return saved(path, value)
		}
		switch {
		case value == "r":
			reset = true
		case value == "c" && reset:
			reset = false
			return os.WriteFile(p, []byte(text), 0o644)
		}
		return nil
	}
	return p
}

const defaultCurve = "OD_FAN_CURVE:\n0: 0C 0%\n1: 0C 0%\nOD_RANGE:\nFAN_CURVE(hotspot temp): 25C 100C\nFAN_CURVE(fan speed): 15% 100%\n"

// The graphics card's fan curve CoolerControl set is reset, then the reset
// committed, when it stops; one it left alone, or a card without one, is
// not written.
func TestFanCurveReset(t *testing.T) {
	f := newFakeSys(t)
	f.chip("3", gpu, map[string]string{"pwm1_enable": "2"})
	f.chip("5", "pci0000:00/0000:00:03.1/0000:0a:00.0", map[string]string{"pwm1_enable": "2"})
	p := f.curve(gpu, defaultCurve)
	must(t, snapshotFans())
	st, err := loadFans()
	must(t, err)
	if len(st.Curves) != 1 || st.Curves[p] == "" {
		t.Fatalf("curves %v", st.Curves)
	}

	w := f.writes()
	must(t, restoreFans())
	if len(*w) != 0 {
		t.Fatalf("an untouched curve was written: %q", *w)
	}
	must(t, os.WriteFile(p, []byte(strings.Replace(defaultCurve, "0: 0C 0%", "0: 40C 30%", 1)), 0o644))
	must(t, snapshotFans()) // a restart keeps the first curve
	must(t, restoreFans())
	if !slices.Equal(*w, []string{"fan_curve=r", "fan_curve=c"}) {
		t.Fatalf("writes %q, want r then c", *w)
	}
	if b, _ := os.ReadFile(p); string(b) != defaultCurve {
		t.Fatalf("curve after the reset:\n%s", b)
	}
}
