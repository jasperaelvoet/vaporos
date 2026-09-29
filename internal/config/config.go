// Package config holds VaporOS paths, the machine configuration
// (/var/lib/vos/config.json) and small helpers shared by every package.
// Paths are variables so tests can point them into a temp dir.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Set by cmd/vos from -ldflags.
var BinaryVersion, BinaryCommit = "dev", ""

var (
	StateDir         = "/var/lib/vos"
	RunDir           = "/run/vos"
	ESP              = "/efi"
	LiveMedium       = "/run/vos/medium/vos"
	LibDir           = "/usr/lib/vos"
	ShareDir         = "/usr/share/vos"
	KeysDir          = "/usr/lib/vos/keys"
	ImageInfoPath    = "/usr/lib/vos/image.json"
	ImageCmdlinePath = "/usr/lib/vos/cmdline"
	ProcCmdline      = "/proc/cmdline"
	OSReleasePath    = "/usr/lib/os-release"
	HostnamePath     = "/etc/hostname"
	ImageEDIDPath    = "/usr/lib/firmware/edid/vaporos.bin"
	GamerUser        = "vapor"
	GamerUID         = 1000
	GamerHome        = "/var/home/vapor"
	DefaultUpdateSrc = "oci://ghcr.io/jasperaelvoet/vaporos"
	HTTPPort         = 80
)

// Derived paths (functions so they follow StateDir/RunDir overrides).
func ConfigPath() string         { return filepath.Join(StateDir, "config.json") }
func AuthPath() string           { return filepath.Join(StateDir, "auth.json") }
func SessionsPath() string       { return filepath.Join(StateDir, "sessions.json") }
func UpdateStatePath() string    { return filepath.Join(StateDir, "update-state.json") }
func ClientsPath() string        { return filepath.Join(StateDir, "clients.json") }
func SunshineAPIPath() string    { return filepath.Join(StateDir, "sunshine-api.json") }
func MachineCmdlinePath() string { return filepath.Join(StateDir, "cmdline") }
func FirmwareDir() string        { return filepath.Join(StateDir, "firmware") }
func LearnedEDIDPath() string    { return filepath.Join(StateDir, "firmware", "edid", "vaporos.bin") }
func HealthOKPath() string       { return filepath.Join(StateDir, "health-ok") }
func SessionSock() string        { return filepath.Join(RunDir, "session.sock") }
func WelcomeStatePath() string   { return filepath.Join(RunDir, "welcome.json") }

type Config struct {
	Schema  int           `json:"schema"`
	Update  UpdateConfig  `json:"update"`
	Power   PowerConfig   `json:"power"`
	Display DisplayConfig `json:"display"`
	Storage StorageConfig `json:"storage"`
	SSH     SSHConfig     `json:"ssh"`
	Web     WebConfig     `json:"web"`
}

type UpdateConfig struct {
	Source  string `json:"source"`
	Channel string `json:"channel"`
	Auto    string `json:"auto"` // "stage" | "off"
}

type PowerConfig struct {
	IdleShutdown bool `json:"idle_shutdown"`
	IdleMinutes  int  `json:"idle_minutes"`
}

type DisplayConfig struct {
	VirtualConnector string   `json:"virtual_connector"`
	HDR              bool     `json:"hdr"`
	ExtraModes       []string `json:"extra_modes"`
}

type StorageConfig struct {
	Libraries []Library `json:"libraries"`
}

type Library struct {
	UUID       string `json:"uuid"`
	Label      string `json:"label"`
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fstype"`
}

type SSHConfig struct {
	Enabled bool     `json:"enabled"`
	Keys    []string `json:"keys"`
}

type WebConfig struct {
	HTTPS       bool `json:"https"`
	AllowPublic bool `json:"allow_public"`
}

// Defaults returns the configuration of a machine that never saved one.
func Defaults() *Config {
	ch := "main"
	if ii, err := LoadImageInfo(); err == nil && ii.Channel != "" {
		ch = ii.Channel
	}
	return &Config{
		Schema:  1,
		Update:  UpdateConfig{Source: DefaultUpdateSrc, Channel: ch, Auto: "stage"},
		Power:   PowerConfig{IdleShutdown: false, IdleMinutes: 15}, // the installer turns it on when Wake-on-LAN can wake the PC again
		Display: DisplayConfig{HDR: true},
		Storage: StorageConfig{Libraries: []Library{}},
		SSH:     SSHConfig{Keys: []string{}},
	}
}

// Load reads config.json over the defaults. A missing file is not an error.
func Load() (*Config, error) {
	c := Defaults()
	err := ReadJSON(ConfigPath(), c)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	return c, err
}

// mu guards every *Config shared inside vosd. Services read fields through
// Snapshot or View and change them only through Mutate, so two services
// never race on the struct or save each other's half-made changes.
var mu sync.RWMutex

// Mutate applies f under the config lock, then saves the whole config.
// f must not call Save, Mutate, View or Snapshot.
func (c *Config) Mutate(f func(*Config)) error {
	mu.Lock()
	defer mu.Unlock()
	f(c)
	return c.saveLocked()
}

// View runs f with the config read-locked. f must not change it.
func (c *Config) View(f func(*Config)) {
	mu.RLock()
	defer mu.RUnlock()
	f(c)
}

// Snapshot returns a deep copy that is safe to read without the lock.
func (c *Config) Snapshot() Config {
	mu.RLock()
	defer mu.RUnlock()
	cp := *c
	cp.Display.ExtraModes = slices.Clone(c.Display.ExtraModes)
	cp.Storage.Libraries = slices.Clone(c.Storage.Libraries)
	cp.SSH.Keys = slices.Clone(c.SSH.Keys)
	return cp
}

// Save writes config.json atomically. Prefer Mutate, which also takes the lock
// for the change itself.
func (c *Config) Save() error {
	mu.Lock()
	defer mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	c.Schema = 1
	return WriteJSONAtomic(ConfigPath(), c, 0o644)
}

func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// WriteJSONAtomic writes v as indented JSON via tmp file + fsync + rename.
func WriteJSONAtomic(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), perm)
}

// WriteFileAtomic writes data via tmp file + fsync + rename + dir fsync.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// ImageInfo describes the running image (/usr/lib/vos/image.json).
type ImageInfo struct {
	Version       string `json:"version"`
	Channel       string `json:"channel"`
	Git           string `json:"git"`
	Kernel        string `json:"kernel"`
	RollbackIndex int64  `json:"rollback_index"`
	Debug         bool   `json:"debug"`
}

func LoadImageInfo() (*ImageInfo, error) {
	ii := &ImageInfo{}
	if err := ReadJSON(ImageInfoPath, ii); err != nil {
		return nil, err
	}
	return ii, nil
}

// KernelArgs returns /proc/cmdline split into words.
func KernelArgs() []string {
	b, err := os.ReadFile(ProcCmdline)
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

// KernelArg returns the value of name=value on the kernel command line.
// For a bare flag it returns "" and true.
func KernelArg(name string) (string, bool) {
	for _, a := range KernelArgs() {
		if a == name {
			return "", true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// IsLive reports whether this boot is the live ISO (installer mode).
func IsLive() bool {
	v, _ := KernelArg("vos.mode")
	return v == "live"
}

// BootedSlot is "a", "b" or "" (live).
func BootedSlot() string {
	v, _ := KernelArg("vos.slot")
	return v
}

// OtherSlot returns the A/B partner of slot.
func OtherSlot(slot string) string {
	if slot == "a" {
		return "b"
	}
	return "a"
}

// Hostname returns /etc/hostname or the kernel's.
func Hostname() string {
	if b, err := os.ReadFile(HostnamePath); err == nil {
		if h := strings.TrimSpace(string(bytes.SplitN(b, []byte("\n"), 2)[0])); h != "" {
			return h
		}
	}
	h, _ := os.Hostname()
	return h
}

// ReadLine returns the first line of a file, trimmed; "" if missing.
func ReadLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}
