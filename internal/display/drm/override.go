package drm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// DebugfsDRI is DRM's debugfs root, a variable so tests can redirect it.
var DebugfsDRI = "/sys/kernel/debug/dri"

// ErrNoOverride means the kernel offers no EDID override for a connector:
// debugfs is not mounted, locked down, or lacks the file.
var ErrNoOverride = errors.New("drm: no debugfs EDID override")

// OverrideEDID makes the kernel use edid for a connector of card ("card1")
// instead of what the sink or drm.edid_firmware= gives it. It takes effect
// on the connector's next probe (see Reprobe) and lasts until reboot.
func OverrideEDID(card, connector string, edid []byte) error {
	dir, err := debugfsConnector(card, connector)
	if err != nil {
		return err
	}
	return writeSys(filepath.Join(dir, "edid_override"), edid)
}

// debugfsConnector finds a connector's debugfs directory. Kernels name a
// card's directory by its minor number, newer ones by its PCI address.
func debugfsConnector(card, connector string) (string, error) {
	names := []string{strconv.Itoa(cardIndex(card))}
	if cards, err := Cards(); err == nil {
		for _, c := range cards {
			if c.Name == card && c.PCISlot != "" {
				names = append(names, c.PCISlot)
			}
		}
	}
	for _, n := range names {
		dir := filepath.Join(DebugfsDRI, n, connector)
		if _, err := os.Stat(filepath.Join(dir, "edid_override")); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w for %s-%s", ErrNoOverride, card, connector)
}

// Reprobe makes the kernel re-read a forced connector's EDID while it stays
// connected, then announces a hotplug so display servers re-read its modes.
// A probing GETCONNECTOR would re-read it too, but the kernel only honours
// that for the DRM master (gamescope). Writing the connector's sysfs status
// probes it whenever the force changes, so this toggles between "on" and
// "on-digital", both forced on, and ends at "on", as video=<C>:e sets it.
func Reprobe(card, connector string) error {
	status := filepath.Join(SysClassDRM, card+"-"+connector, "status")
	for _, v := range []string{"on-digital", "on"} {
		if err := writeSys(status, []byte(v)); err != nil {
			return err
		}
	}
	// A synthetic uevent carries HOTPLUG=1 only when asked for it; without
	// it display servers ignore the event. Best effort: the probe is done.
	_ = writeSys(filepath.Join(SysClassDRM, card, "uevent"),
		[]byte("change 00000000-0000-0000-0000-000000000000 HOTPLUG=1"))
	return nil
}

// writeSys writes b in a single write, as sysfs and debugfs files expect.
func writeSys(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0) // as `echo >` opens it
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if err := f.Close(); werr == nil {
		werr = err
	}
	if werr != nil {
		return fmt.Errorf("drm: writing %s: %w", path, werr)
	}
	return nil
}
