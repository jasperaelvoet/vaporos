package coolercontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

// Argon2id PHC strings as auth.json holds them (cheap parameters).
const (
	adminHash  = "$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	adminHash2 = "$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	theirHash  = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$BgYGBgYGBgYGBgYGBgYGBgYGBgYGBgYGBgYGBgYGBgY"
)

// prepRig is an installed box with CoolerControl 5.0.1 mounted: auth.json,
// the image's packages list and the data area in temp dirs.
type prepRig struct {
	t       *testing.T
	d       dirs
	backups int
	fail    error
}

func newPrepRig(t *testing.T) *prepRig {
	t.Helper()
	root := t.TempDir()
	savedState, savedLib := config.StateDir, config.ExtMountedLibDir
	t.Cleanup(func() { config.StateDir, config.ExtMountedLibDir = savedState, savedLib })
	config.StateDir = filepath.Join(root, "state")
	config.ExtMountedLibDir = filepath.Join(root, "usr", "lib", "vos", "ext")
	t.Setenv("CC_CONFIG_DIR", "")
	t.Setenv("CC_DATA_DIR", "")
	r := &prepRig{t: t, d: unitDirs()}
	r.version("5.0.1-1")
	r.admin(adminHash)
	return r
}

func (r *prepRig) admin(hash string) {
	r.t.Helper()
	must(r.t, config.WriteJSONAtomic(config.AuthPath(), auth.File{User: "admin", Hash: hash}, 0o600))
}

func (r *prepRig) version(v string) {
	r.t.Helper()
	p := packagesPath()
	must(r.t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(r.t, os.WriteFile(p, []byte("liquidctl 1.15.0-1\ncoolercontrold "+v+"\n"), 0o644))
}

func (r *prepRig) prepare() error {
	return prepare(context.Background(), r.d, func(_ context.Context, d dirs) error {
		if d != r.d {
			r.t.Errorf("backup in %+v", d)
		}
		r.backups++
		return r.fail
	})
}

func (r *prepRig) passwd() string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.d.config, ".passwd"))
	if errors.Is(err, os.ErrNotExist) {
		return "<missing>"
	}
	must(r.t, err)
	return string(b)
}

func (r *prepRig) configToml() string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.d.config, "config.toml"))
	must(r.t, err)
	return string(b)
}

func TestUnitDirs(t *testing.T) {
	t.Setenv("CC_CONFIG_DIR", "/var/lib/vos/ext/data/coolercontrol/config/")
	t.Setenv("CC_DATA_DIR", "relative/data")
	d := unitDirs()
	want := filepath.Join(config.ExtDataDir(), "coolercontrol")
	if d.area != want || d.config != "/var/lib/vos/ext/data/coolercontrol/config" || d.data != filepath.Join(want, "data") {
		t.Fatalf("dirs %+v", d)
	}
}

func TestPrepareFirstStart(t *testing.T) {
	r := newPrepRig(t)
	must(t, r.prepare())
	if got := r.passwd(); got != adminHash {
		t.Fatalf(".passwd = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(r.d.config, ".passwd")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".passwd: %v %v", fi, err)
	}
	cfg := r.configToml()
	for _, want := range []string{"[settings]\n", `trusted_proxies = ["127.0.0.1", "::1"]`, "\npoll_rate = 1.0\n",
		"\ndrivetemp_suspend = true\n", "\nport = 11986\n", "\ntls_enabled = false\n", `ipv4_address = "127.0.0.1"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.toml lacks %q:\n%s", want, cfg)
		}
	}
	if r.backups != 0 {
		t.Fatal("backed up before there was anything to back up")
	}
	if st := loadState(r.d); st.Daemon != "5.0.1-1" || st.Passwd == "" {
		t.Fatalf("state %+v", st)
	}
	for _, d := range []string{r.d.config, r.d.data} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s: %v", d, err)
		}
	}
}

// Without a VaporOS password, or with one that is not argon2id, the start
// stops: coolercontrold would fall back to its default password.
func TestPrepareFailsClosed(t *testing.T) {
	r := newPrepRig(t)
	must(t, os.Remove(config.AuthPath()))
	if err := r.prepare(); err == nil {
		t.Fatal("prepared without auth.json")
	}
	if r.passwd() != "<missing>" {
		t.Fatal("wrote .passwd without a password")
	}
	r.admin("$2b$12$abcdefghijklmnopqrstuuJ5bc3fvl0sCC9rT5hS3XSmzyT6mrmRS")
	if err := r.prepare(); err == nil {
		t.Fatal("prepared with a bcrypt hash")
	}
	if r.passwd() != "<missing>" {
		t.Fatal("wrote .passwd from a hash that is not argon2id")
	}
}

// A new admin password follows into CoolerControl while .passwd is still
// VaporOS's copy; one set in CoolerControl stays.
func TestPreparePasswordFollowsOnlyItsOwnCopy(t *testing.T) {
	r := newPrepRig(t)
	must(t, r.prepare())
	r.admin(adminHash2)
	must(t, r.prepare())
	if got := r.passwd(); got != adminHash2 {
		t.Fatalf("after a password change: %q", got)
	}
	must(t, os.WriteFile(filepath.Join(r.d.config, ".passwd"), []byte(theirHash), 0o644))
	r.admin(adminHash)
	must(t, r.prepare())
	if got := r.passwd(); got != theirHash {
		t.Fatalf("replaced the password set in CoolerControl: %q", got)
	}
	// A purge starts over with VaporOS's.
	must(t, os.RemoveAll(r.d.area))
	must(t, r.prepare())
	if got := r.passwd(); got != adminHash {
		t.Fatalf("after a purge: %q", got)
	}
}

// A password changed in VaporOS reaches a running CoolerControl at once,
// on the same terms: only while .passwd is VaporOS's copy.
func TestPasswordChanged(t *testing.T) {
	r := newPrepRig(t)
	h := newHelper()
	x := &extensions.Ext{ID: id, DataDir: r.d.area}
	must(t, h.PasswordChanged(context.Background(), x))
	if r.passwd() != "<missing>" {
		t.Fatal("wrote .passwd before CoolerControl's first start")
	}

	must(t, r.prepare())
	p := filepath.Join(r.d.config, ".passwd")
	old := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(p, old, old))
	r.admin(adminHash2)
	must(t, h.PasswordChanged(context.Background(), x))
	if got := r.passwd(); got != adminHash2 {
		t.Fatalf("after a password change: %q", got)
	}
	if fi, err := os.Stat(p); err != nil || !fi.ModTime().After(old) || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".passwd %v %v: coolercontrold sees a change by its mtime", fi, err)
	}
	if st := loadState(r.d); st.Passwd != sum([]byte(adminHash2)) {
		t.Fatalf("state %+v", st)
	}

	must(t, os.WriteFile(p, []byte(theirHash), 0o600))
	r.admin(adminHash)
	must(t, h.PasswordChanged(context.Background(), x))
	if got := r.passwd(); got != theirHash {
		t.Fatalf("replaced the password set in CoolerControl: %q", got)
	}
}

func TestPrepareBacksUpBeforeANewVersion(t *testing.T) {
	r := newPrepRig(t)
	must(t, r.prepare())
	must(t, r.prepare())
	if r.backups != 0 {
		t.Fatalf("%d backups without a new version", r.backups)
	}
	r.version("5.0.2-1")
	r.fail = errors.New("disk full")
	must(t, r.prepare()) // a failed backup does not stop the fans' daemon
	if r.backups != 1 || loadState(r.d).Daemon != "5.0.1-1" {
		t.Fatalf("%d backups, state %+v", r.backups, loadState(r.d))
	}
	r.fail = nil
	must(t, r.prepare())
	must(t, r.prepare())
	if r.backups != 2 || loadState(r.d).Daemon != "5.0.2-1" {
		t.Fatalf("%d backups, state %+v", r.backups, loadState(r.d))
	}
}

func TestPrepareKeepsTheUsersConfig(t *testing.T) {
	r := newPrepRig(t)
	must(t, os.MkdirAll(r.d.config, 0o700))
	cfg := filepath.Join(r.d.config, "config.toml")
	must(t, os.WriteFile(cfg, []byte("[devices]\n[settings]\npoll_rate = 0.5\nport = 9000\n[legacy690]\n"), 0o640))
	must(t, r.prepare())
	got := r.configToml()
	if !strings.Contains(got, "poll_rate = 0.5\n") || strings.Contains(got, "port = 9000") || !strings.HasSuffix(got, "[legacy690]\n") {
		t.Fatalf("config.toml:\n%s", got)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	must(t, os.WriteFile(cfg, []byte("settings.port = 1\n"), 0o640))
	if err := r.prepare(); err == nil {
		t.Fatal("started with a config.toml it could not set up")
	}
}
