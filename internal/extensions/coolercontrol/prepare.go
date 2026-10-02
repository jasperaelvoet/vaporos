package coolercontrol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

// daemonPackage is the package whose version decides a backup.
const daemonPackage = "coolercontrold"

// backupWait bounds `coolercontrold backup`.
const backupWait = 2 * time.Minute

// The settings vosd's proxy depends on, written at every start: it is the
// only way in (loopback, plain HTTP behind it). The others are VaporOS's
// choices, written only where config.toml lacks them, so a change made in
// CoolerControl stays.
var (
	forcedSettings = []setting{
		{"ipv4_address", `"127.0.0.1"`},
		{"ipv6_address", `"::1"`},
		{"port", "11986"},
		{"tls_enabled", "false"},
	}
	defaultSettings = []setting{
		{"poll_rate", "1.0"},
		{"drivetemp_suspend", "true"},
	}
)

// dirs are where coolercontrold keeps its files.
type dirs struct {
	area   string // the extension's system data area
	config string // CC_CONFIG_DIR: config.toml, .passwd
	data   string // CC_DATA_DIR
}

// areaDirs are the data area's own subdirectories (the system data area
// when area is "").
func areaDirs(area string) dirs {
	if area == "" {
		area = filepath.Join(config.ExtDataDir(), id)
	}
	return dirs{area: area, config: filepath.Join(area, "config"), data: filepath.Join(area, "data")}
}

// unitDirs reads the unit's environment, falling back to the data area's
// own subdirectories.
func unitDirs() dirs {
	d := areaDirs("")
	if v := os.Getenv("CC_CONFIG_DIR"); filepath.IsAbs(v) {
		d.config = filepath.Clean(v)
	}
	if v := os.Getenv("CC_DATA_DIR"); filepath.IsAbs(v) {
		d.data = filepath.Clean(v)
	}
	return d
}

// prepState is what prepare remembers, in the data area: the sha256 of the
// last .passwd it wrote, and the daemon version it last backed up for.
type prepState struct {
	Passwd string `json:"passwd_sha256,omitempty"`
	Daemon string `json:"daemon,omitempty"`
}

func statePath(d dirs) string { return filepath.Join(d.area, "vaporos.json") }

func loadState(d dirs) prepState {
	var st prepState
	if err := config.ReadJSON(statePath(d), &st); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("coolercontrol: %v (starting over)", err)
	}
	return st
}

func saveState(d dirs, st prepState) error {
	return config.WriteJSONAtomic(statePath(d), st, 0o600)
}

// prepare readies a start of coolercontrold (ExecStartPre, as root, in the
// unit's sandbox). An error stops the start: CoolerControl never runs with
// its own default password or listening anywhere but loopback.
func prepare(ctx context.Context, d dirs, backup func(context.Context, dirs) error) error {
	for _, dir := range []string{d.config, d.data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	st := loadState(d)
	if err := copyPassword(d, &st); err != nil {
		return err
	}
	cfg := filepath.Join(d.config, "config.toml")
	_, err := os.Lstat(cfg)
	existed := err == nil
	if v := daemonVersion(); v == "" {
		log.Printf("coolercontrol: no %s version in %s; no backup", daemonPackage, packagesPath())
	} else if v != st.Daemon {
		if !existed {
			st.Daemon = v // nothing to back up yet
		} else if err := backup(ctx, d); err != nil {
			log.Printf("coolercontrol: backup before the first start of %s: %v (trying again next start)", v, err)
		} else {
			log.Printf("coolercontrol: backed up its settings before the first start of %s", v)
			st.Daemon = v
		}
		if err := saveState(d, st); err != nil {
			return err
		}
	}
	return patchConfig(cfg)
}

// copyPassword gives CoolerControl the VaporOS admin password: auth.json's
// argon2id hash, which coolercontrold verifies as it is (PHC string,
// parameters from the hash), into .passwd. It writes the file when it is
// missing or still the copy it made last; one changed in CoolerControl is
// left as it is. Without a VaporOS password it fails: coolercontrold would
// fall back to its well-known default.
func copyPassword(d dirs, st *prepState) error {
	f, err := auth.Load()
	if err != nil {
		return fmt.Errorf("no VaporOS admin password to give CoolerControl: %w", err)
	}
	want := []byte(f.Hash)
	path := filepath.Join(d.config, ".passwd")
	cur, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case bytes.Equal(cur, want):
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		if st.Passwd == sum(want) {
			return nil
		}
		st.Passwd = sum(want)
		return saveState(d, *st)
	case st.Passwd == "" || sum(cur) != st.Passwd:
		log.Printf("coolercontrol: keeping the password set in CoolerControl")
		return nil
	}
	if err := config.WriteFileAtomic(path, want, 0o600); err != nil {
		return err
	}
	st.Passwd = sum(want)
	if err := saveState(d, *st); err != nil {
		return err
	}
	log.Printf("coolercontrol: copied the VaporOS admin password")
	return nil
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// packagesPath is the packages list of the mounted image.
func packagesPath() string {
	return filepath.Join(config.ExtMountedLibDir, id, "packages.txt")
}

// daemonVersion is coolercontrold's package version in the mounted image,
// "" when the list does not say.
func daemonVersion() string {
	f, err := os.Open(packagesPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name, ver, ok := strings.Cut(strings.TrimSpace(sc.Text()), " "); ok && name == daemonPackage {
			return strings.TrimSpace(ver)
		}
	}
	return ""
}

// runBackup runs `coolercontrold backup` with the unit's environment, in
// the data area (the sandbox leaves nothing else writable).
func runBackup(ctx context.Context, d dirs) error {
	ctx, cancel := context.WithTimeout(ctx, backupWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/coolercontrold", "backup")
	cmd.Dir = d.area
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// patchConfig writes the tables coolercontrold needs and VaporOS's
// settings into config.toml (made when missing), keeping everything else
// in it.
func patchConfig(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	out, err := addTables(string(b), requiredTables)
	if err == nil {
		out, err = patchSettings(out, forcedSettings, defaultSettings)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if out == string(b) {
		return nil
	}
	return config.WriteFileAtomic(path, []byte(out), mode)
}
