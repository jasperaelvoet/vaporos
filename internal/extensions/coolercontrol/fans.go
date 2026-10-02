package coolercontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// hwmonDir is where the kernel lists its sensor chips. A variable for tests.
var hwmonDir = "/sys/class/hwmon"

// sysWrite writes one sysfs attribute. A variable for tests.
var sysWrite = func(path, value string) error { return os.WriteFile(path, []byte(value+"\n"), 0o644) }

// fansPath holds each fan's control as first seen this boot, before
// CoolerControl took it over (docs/CONTRACTS.md "Extensions").
func fansPath() string { return filepath.Join(config.RunDir, "coolercontrol-fans.json") }

// fanFile is "<hwmonN>/pwm<N>_enable": 0 full speed, 1 manual, 2 and up the
// chip's or firmware's own automatic modes. "<hwmonN>/pwm<N>" is its duty,
// 0-255, which sets the speed in manual mode.
var (
	fanFileRe  = regexp.MustCompile(`^pwm[0-9]+_enable$`)
	fanValueRe = regexp.MustCompile(`^[0-9]{1,3}$`)
)

// fanState is fansPath's content, keyed by device: hwmonN numbers change
// with every boot and every driver load; the device path does not.
type fanState struct {
	Fans   map[string]string `json:"fans"`             // "<device>/pwm<N>_enable": its mode
	Duty   map[string]string `json:"duty,omitempty"`   // "<device>/pwm<N>": its duty, for a mode of 0 or 1
	Curves map[string]string `json:"curves,omitempty"` // an amdgpu fan_curve's path: the sha256 of its text
}

// fan is one fan control: its mode and duty files, each with its key.
type fan struct{ mode, modeKey, duty, dutyKey string }

// fanCurvePath is where amdgpu's OverDrive fan curve sits on its card's
// device (RDNA3 and later).
const fanCurvePath = "gpu_od/fan_ctrl/fan_curve"

// scanFans lists every fan control and every amdgpu fan curve, sorted.
func scanFans() ([]fan, []string, error) {
	dirs, err := filepath.Glob(filepath.Join(hwmonDir, "hwmon*"))
	if err != nil {
		return nil, nil, err
	}
	var fans []fan
	var curves []string
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue // a chip that went away
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
		if err == nil {
			p := filepath.Join(dev, fanCurvePath)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && !slices.Contains(curves, p) {
				curves = append(curves, p)
			}
		} else if dev, err = filepath.EvalSymlinks(dir); err != nil {
			continue
		}
		for _, e := range ents {
			if fanFileRe.MatchString(e.Name()) {
				duty := strings.TrimSuffix(e.Name(), "_enable")
				fans = append(fans, fan{mode: filepath.Join(dir, e.Name()), modeKey: dev + "/" + e.Name(),
					duty: filepath.Join(dir, duty), dutyKey: dev + "/" + duty})
			}
		}
	}
	slices.SortFunc(fans, func(a, b fan) int { return strings.Compare(a.mode, b.mode) })
	slices.Sort(curves)
	return fans, curves, nil
}

func readFan(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if !fanValueRe.MatchString(v) {
		return "", fmt.Errorf("%s: unexpected value %q", path, v)
	}
	return v, nil
}

func validDuty(v string) bool {
	n, err := strconv.Atoi(v)
	return fanValueRe.MatchString(v) && err == nil && n <= 255
}

func loadFans() (*fanState, error) {
	st := &fanState{}
	err := config.ReadJSON(fansPath(), st)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	} else if err != nil {
		st = &fanState{}
	}
	for _, m := range []*map[string]string{&st.Fans, &st.Duty, &st.Curves} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	return st, err
}

// snapshotFans records what the record lacks, before CoolerControl starts
// (ExecStartPre): each fan's mode, the duty of one in mode 0 or 1, and
// each amdgpu fan curve. What is recorded keeps its first value: a restart
// of CoolerControl, or a chip whose driver loads later, never replaces
// what the firmware had set.
func snapshotFans() error {
	st, err := loadFans()
	if err != nil {
		log.Printf("coolercontrol: %s: %v (starting a new record)", fansPath(), err)
	}
	fans, curves, err := scanFans()
	if err != nil {
		return err
	}
	changed := false
	for _, f := range fans {
		if _, ok := st.Fans[f.modeKey]; ok {
			continue
		}
		v, err := readFan(f.mode)
		if err != nil {
			log.Printf("coolercontrol: %v", err)
			continue
		}
		st.Fans[f.modeKey] = v
		changed = true
		if v != "0" && v != "1" {
			continue
		}
		d, err := readFan(f.duty)
		if err == nil && !validDuty(d) {
			err = fmt.Errorf("%s: unexpected value %q", f.duty, d)
		}
		if err != nil {
			log.Printf("coolercontrol: %v", err)
			continue
		}
		st.Duty[f.dutyKey] = d
	}
	for _, p := range curves {
		if _, ok := st.Curves[p]; ok {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			log.Printf("coolercontrol: %v", err)
			continue
		}
		st.Curves[p] = sum(b)
		changed = true
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(fansPath(), append(b, '\n'), 0o600)
}

// restoreFans puts back what snapshotFans recorded, after CoolerControl
// stops however it stops (ExecStopPost), and when it is removed: each
// amdgpu fan curve that changed to its default, then each fan's mode and
// duty. What already matches is left alone.
func restoreFans() error {
	st, err := loadFans()
	if err != nil {
		return err
	}
	fans, curves, err := scanFans()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range curves {
		want, ok := st.Curves[p]
		if !ok {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && sum(b) == want {
			continue
		}
		// Reset, then commit: the card's firmware has the fan again.
		if err := sysWrite(p, "r"); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := sysWrite(p, "c"); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("coolercontrol: %s back to its default", p)
	}
	for _, f := range fans {
		if err := restoreFan(st, f); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// restoreFan puts one fan back in its recorded mode, with its recorded
// duty. A duty is written only while the fan is in manual mode, the one
// mode every driver takes it in (nct6775 would leave mode 0 for it): after
// the mode when that is 1, before it when it is 0.
func restoreFan(st *fanState, f fan) error {
	want, ok := st.Fans[f.modeKey]
	if !ok || !fanValueRe.MatchString(want) {
		return nil
	}
	duty, ok := st.Duty[f.dutyKey]
	hasDuty := ok && validDuty(duty) && (want == "0" || want == "1")
	cur, err := readFan(f.mode)
	if hasDuty && want == "0" && err == nil && cur == "1" {
		if err := setDuty(f, duty); err != nil {
			log.Printf("coolercontrol: %v (mode 0 runs it at full speed anyway)", err)
		}
	}
	if err != nil || cur != want {
		if err := sysWrite(f.mode, want); err != nil {
			return err
		}
		log.Printf("coolercontrol: %s back to %s", f.mode, want)
	}
	if hasDuty && want == "1" {
		return setDuty(f, duty)
	}
	return nil
}

func setDuty(f fan, duty string) error {
	if cur, err := readFan(f.duty); err == nil && cur == duty {
		return nil
	}
	if err := sysWrite(f.duty, duty); err != nil {
		return err
	}
	log.Printf("coolercontrol: %s back to %s", f.duty, duty)
	return nil
}
