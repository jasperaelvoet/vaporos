package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// maxSettingsFile bounds settings/<id>.json; a descriptor has a handful of
// settings.
const maxSettingsFile = 64 << 10

func settingsPath(id string) string  { return filepath.Join(config.ExtSettingsDir(), id+".json") }
func installedPath(id string) string { return filepath.Join(config.ExtSettingsDir(), id+".installed") }

// defaultValue is a setting's value until someone changes it: the
// descriptor's default when it is one the setting takes, else false, the
// first choice, or no disk.
func defaultValue(s descriptor.Setting) any {
	if s.Default != nil {
		if v, err := CheckSetting(s, s.Default); err == nil {
			return v
		}
	}
	switch s.Type {
	case "bool":
		return false
	case "choice":
		if len(s.Choices) > 0 {
			return s.Choices[0]
		}
	}
	return ""
}

// A disk setting's value: "" (none picked yet), SystemDrive or an adopted
// game drive's folder, GameDrives/<name> (GET /storage's mounted_at).
const (
	SystemDrive = "/var"
	GameDrives  = "/var/mnt"
)

// legacySystemDrive is the system drive's folder earlier control centers
// offered; a stored one reads as SystemDrive.
const legacySystemDrive = "/state"

var driveNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

// CheckSetting returns v (as JSON decodes it) if setting s takes it: what
// PUT /extensions/{id}/settings accepts (the dev server's fake checks the
// same). A required disk setting does not take "".
func CheckSetting(s descriptor.Setting, v any) (any, error) {
	switch s.Type {
	case "bool":
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("%s must be true or false", s.Key)
	case "choice":
		if c, ok := v.(string); ok && slices.Contains(s.Choices, c) {
			return c, nil
		}
		return nil, fmt.Errorf("%s must be one of %s", s.Key, strings.Join(s.Choices, ", "))
	case "disk":
		if p, ok := v.(string); ok && (drivePath(p) || (p == "" && !s.Required)) {
			return p, nil
		}
		if s.Required {
			return nil, fmt.Errorf("%s must be the system drive (%s) or a game drive (a folder in %s)", s.Key, SystemDrive, GameDrives)
		}
		return nil, fmt.Errorf("%s must be the system drive (%s), a game drive (a folder in %s) or empty", s.Key, SystemDrive, GameDrives)
	}
	return nil, fmt.Errorf("%s is a kind of setting this VaporOS does not know", s.Key)
}

// drivePath reports whether p names a drive the control center offers: the
// system drive, or a game drive's folder. The helper that uses it checks
// the drive itself.
func drivePath(p string) bool {
	name, ok := strings.CutPrefix(p, GameDrives+"/")
	return p == SystemDrive || (ok && driveNameRe.MatchString(name) && name != "." && name != "..")
}

// missingDrive is the first required disk setting of d that values leave
// without a drive, or false.
func missingDrive(d *descriptor.Descriptor, values map[string]any) (descriptor.Setting, bool) {
	if d != nil {
		for _, s := range d.Settings {
			if p, _ := values[s.Key].(string); s.Required && s.Type == "disk" && p == "" {
				return s, true
			}
		}
	}
	return descriptor.Setting{}, false
}

// loadSettings returns every setting of d with its value: the stored one
// where it is one the setting takes, else the default.
func loadSettings(id string, d *descriptor.Descriptor) map[string]any {
	out := map[string]any{}
	if d == nil {
		return out
	}
	stored := readSettingsFile(id)
	for _, s := range d.Settings {
		v := defaultValue(s)
		if raw, ok := stored[s.Key]; ok {
			if s.Type == "disk" && raw == legacySystemDrive {
				raw = SystemDrive
			}
			if c, err := CheckSetting(s, raw); err == nil {
				v = c
			}
		}
		out[s.Key] = v
	}
	return out
}

func readSettingsFile(id string) map[string]any {
	f, err := os.Open(settingsPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Printf("extensions: %s settings: %v", id, err)
		return nil
	}
	defer f.Close()
	var m map[string]any
	if err := json.NewDecoder(io.LimitReader(f, maxSettingsFile)).Decode(&m); err != nil {
		log.Printf("extensions: %s settings: %v (using the defaults)", id, err)
		return nil
	}
	return m
}

// checkSettings validates a change against d's settings: every key one of
// them and every value one it takes. It returns the values as stored.
func checkSettings(d *descriptor.Descriptor, change map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(change)) {
		var s descriptor.Setting
		ok := false
		if d != nil {
			s, ok = d.Setting(k)
		}
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		v, err := CheckSetting(s, change[k])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// saveSettings writes every setting of d, with change applied.
func saveSettings(id string, d *descriptor.Descriptor, change map[string]any) error {
	cur := loadSettings(id, d)
	maps.Copy(cur, change)
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(settingsPath(id), append(b, '\n'), 0o644)
}

// moduleSettings returns the keys of d's settings that feed kernel module
// options: changing one takes the admin password and a restart.
func moduleSettings(d *descriptor.Descriptor) map[string]bool {
	out := map[string]bool{}
	if d != nil {
		for _, m := range d.ModuleOptions {
			out[m.Setting] = true
		}
	}
	return out
}

// isInstalled reports whether the helper's Install finished for id (and no
// Remove came after it).
func isInstalled(id string) bool {
	_, err := os.Lstat(installedPath(id))
	return err == nil
}

func markInstalled(id string) error {
	return config.WriteFileAtomic(installedPath(id), nil, 0o644)
}

func unmarkInstalled(id string) error {
	err := os.Remove(installedPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ext is what id's helper works with.
func (s *Service) ext(id string, d *descriptor.Descriptor) *Ext { return newExt(id, d) }

// newExt is what id's helper works with, in vosd or in `vos ext action`.
func newExt(id string, d *descriptor.Descriptor) *Ext {
	return &Ext{ID: id, Desc: d, Settings: loadSettings(id, d),
		DataDir: filepath.Join(config.ExtDataDir(), id),
		HomeDir: filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, id)}
}

// moduleOptions renders the module options of a set's ids (the reconcile's
// Options): each one's helper renders them from its settings.
func (s *Service) moduleOptions(ids []string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, s.optionsOf(id)...)
	}
	return out
}

// optionsOf is the module option lines id's helper renders, only those for
// a module parameter its descriptor lists (the initramfs checks the same
// against the image's own list).
func (s *Service) optionsOf(id string) []string {
	d := s.desc(id)
	if d == nil || !d.HasModuleOptions() {
		return nil
	}
	return ownOptions(d, HelperFor(id).ModuleOptions(s.ext(id, d)))
}

// ownOptions keeps the "options <module> <param>=<value>" lines of lines
// whose module parameter d lists.
func ownOptions(d *descriptor.Descriptor, lines []string) []string {
	var out []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 3 || f[0] != "options" {
			continue
		}
		param, _, ok := strings.Cut(f[2], "=")
		if ok && slices.ContainsFunc(d.ModuleOptions, func(m descriptor.ModuleOption) bool {
			return m.Module == f[1] && m.Param == param
		}) {
			out = append(out, strings.Join(f, " "))
		}
	}
	return out
}

// normalized is lines sorted, without repeats, for comparing option sets.
func normalized(lines []string) []string {
	out := slices.Clone(lines)
	slices.Sort(out)
	return slices.Compact(out)
}
