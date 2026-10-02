package coolercontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// hwmonDir is where the kernel lists its sensor chips. A variable for tests.
var hwmonDir = "/sys/class/hwmon"

// fansPath holds each fan's control mode as first seen this boot, before
// CoolerControl took it over (docs/CONTRACTS.md "Extensions").
func fansPath() string { return filepath.Join(config.RunDir, "coolercontrol-fans.json") }

// fanFile is "<hwmonN>/pwm<N>_enable": 0 full speed, 1 manual, 2 and up the
// chip's or firmware's own automatic modes.
var (
	fanFileRe  = regexp.MustCompile(`^pwm[0-9]+_enable$`)
	fanValueRe = regexp.MustCompile(`^[0-9]{1,3}$`)
)

// fanState is fansPath's content: by "<device realpath>/pwm<N>_enable",
// the value it had. hwmonN numbers change with every boot and every
// driver load; the device path does not.
type fanState struct {
	Fans map[string]string `json:"fans"`
}

// fanFiles maps each fan control file to its key.
func fanFiles() (map[string]string, error) {
	dirs, err := filepath.Glob(filepath.Join(hwmonDir, "hwmon*"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue // a chip that went away
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
		if err != nil {
			if dev, err = filepath.EvalSymlinks(dir); err != nil {
				continue
			}
		}
		for _, e := range ents {
			if fanFileRe.MatchString(e.Name()) {
				out[filepath.Join(dir, e.Name())] = dev + "/" + e.Name()
			}
		}
	}
	return out, nil
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

func loadFans() (*fanState, error) {
	st := &fanState{Fans: map[string]string{}}
	err := config.ReadJSON(fansPath(), st)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if st.Fans == nil {
		st.Fans = map[string]string{}
	}
	return st, nil
}

// snapshotFans records the mode of every fan the record lacks, before
// CoolerControl starts (ExecStartPre). A fan already recorded keeps its
// first value: a restart of CoolerControl, or a chip whose driver loads
// later, never replaces what the firmware had set.
func snapshotFans() error {
	st, err := loadFans()
	if err != nil {
		log.Printf("coolercontrol: %s: %v (starting a new record)", fansPath(), err)
	}
	files, err := fanFiles()
	if err != nil {
		return err
	}
	changed := false
	for _, path := range slices.Sorted(maps.Keys(files)) {
		key := files[path]
		if _, ok := st.Fans[key]; ok {
			continue
		}
		v, err := readFan(path)
		if err != nil {
			log.Printf("coolercontrol: %v", err)
			continue
		}
		st.Fans[key] = v
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

// restoreFans puts every recorded fan back in the mode it had, after
// CoolerControl stops however it stops (ExecStopPost), and when it is
// removed. Fans already in that mode are left alone.
func restoreFans() error {
	st, err := loadFans()
	if err != nil {
		return err
	}
	files, err := fanFiles()
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range slices.Sorted(maps.Keys(files)) {
		want, ok := st.Fans[files[path]]
		if !ok || !fanValueRe.MatchString(want) {
			continue
		}
		if cur, err := readFan(path); err == nil && cur == want {
			continue
		}
		if err := os.WriteFile(path, []byte(want+"\n"), 0o644); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("coolercontrol: %s back to %s", path, want)
	}
	return errors.Join(errs...)
}
