package store

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Boot modes in the report.
const (
	ModePending = "pending"  // an extension trial: the pending set
	ModeEnabled = "enabled"  // the enabled set, proven images only
	ModeOSTrial = "os-trial" // systemd-boot counts this boot: enabled at the new catalog
	ModeOff     = "off"      // no extension mounted on purpose
)

// Reasons the initramfs gives for mounting nothing, and the one for a boot
// without a report.
const (
	ReasonNoExt    = "vos.ext=0"
	ReasonSkipOnce = "skip-once"
	ReasonNoReport = "no report"
)

// Skip reasons for one image.
const (
	SkipRequires     = "requires"
	SkipNotInCatalog = "not-in-catalog"
	SkipMissing      = "missing"
	SkipSize         = "size"
	SkipFSVerity     = "fsverity"
	SkipUnproven     = "unproven"
	SkipMount        = "mount"
	SkipNoUsr        = "no-usr"
	SkipOverlay      = "overlay"
)

const maxReport = 1 << 20

// BootReport is /run/vos/extensions.json, written by the initramfs: which
// set this boot chose and why, and what it mounted. Everything at runtime
// follows it, not intent.
type BootReport struct {
	Mode      string    `json:"mode"`
	Set       string    `json:"set"`
	TriesLeft int       `json:"tries_left"`
	Reason    string    `json:"reason"`
	Mounted   []Mounted `json:"mounted"`
	Skipped   []Skipped `json:"skipped"`
}

// Mounted is an image this boot mounted.
type Mounted struct {
	ID       string `json:"id"`
	SHA256   string `json:"sha256"`
	FSVerity string `json:"fsverity"`
}

// Skipped is an id of the set this boot did not mount.
type Skipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// LoadBootReport reads the boot report. With none (a live boot, or an
// image without extensions) it is mode off with ReasonNoReport.
func LoadBootReport() (*BootReport, error) {
	f, err := os.Open(config.ExtBootPath())
	if errors.Is(err, fs.ErrNotExist) {
		return &BootReport{Mode: ModeOff, Reason: ReasonNoReport}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rep := &BootReport{}
	if err := json.NewDecoder(io.LimitReader(f, maxReport)).Decode(rep); err != nil {
		return nil, err
	}
	return rep, nil
}

// IsTrial reports whether this boot is an extension trial.
func (r *BootReport) IsTrial() bool { return r != nil && r.Mode == ModePending }

// MountedPairs returns the valid id and fs-verity pairs this boot mounted.
func (r *BootReport) MountedPairs() []Pair {
	if r == nil {
		return nil
	}
	var out []Pair
	for _, m := range r.Mounted {
		if p := (Pair{ID: m.ID, FSVerity: m.FSVerity}); p.valid() {
			out = append(out, p)
		}
	}
	return out
}

// IsMounted reports whether this boot mounted id.
func (r *BootReport) IsMounted(id string) bool {
	if r == nil {
		return false
	}
	for _, m := range r.Mounted {
		if m.ID == id {
			return true
		}
	}
	return false
}

// SkipReason returns why this boot skipped id, or "".
func (r *BootReport) SkipReason(id string) string {
	if r == nil {
		return ""
	}
	for _, s := range r.Skipped {
		if s.ID == id {
			return s.Reason
		}
	}
	return ""
}
