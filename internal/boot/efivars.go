package boot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// EFIVarsDir is where efivarfs is mounted; tests point it into a temp dir.
var EFIVarsDir = "/sys/firmware/efi/efivars"

// loaderVendor is systemd-boot's EFI variable vendor GUID.
const loaderVendor = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

// loaderOverrides are systemd-boot's persistent settings that beat
// loader.conf and the entry order: a menu timeout (the +/-/t keys, `bootctl
// set-timeout`) and a pinned default (d/D, `bootctl set-default` or
// set-preferred). They live in NVRAM, so they survive a reinstall: an old
// systemd-boot on the same PC can leave a menu that waits for a key on
// every boot, and a pin keeps booting one version while updates pile up
// unused. One-shot variables clear themselves and are left alone.
var loaderOverrides = []string{"LoaderConfigTimeout", "LoaderEntryDefault", "LoaderEntryPreferred"}

// ClearLoaderOverrides deletes loaderOverrides. A missing variable is fine.
func ClearLoaderOverrides() error {
	var errs []error
	for _, name := range loaderOverrides {
		p := filepath.Join(EFIVarsDir, name+"-"+loaderVendor)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		clearImmutable(p)
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
