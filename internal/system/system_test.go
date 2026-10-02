package system

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"golang.org/x/crypto/ssh"
)

// fixture points config paths into a temp dir and returns a Service whose
// commands are recorded instead of run.
type fixture struct {
	t    *testing.T
	dir  string
	svc  *Service
	mu   sync.Mutex
	cmds []string
	fail map[string]error // command line -> error
	evs  []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), fail: map[string]error{}}
	save := []*string{&config.StateDir, &config.HostnamePath, &config.GamerHome, &config.ImageInfoPath,
		&config.OSReleasePath, &config.ProcCmdline, &hwmonRoot, &uptimePath, &cpuinfoPath}
	old := make([]string, len(save))
	for i, p := range save {
		old[i] = *p
	}
	t.Cleanup(func() {
		for i, p := range save {
			*p = old[i]
		}
	})
	config.StateDir = filepath.Join(f.dir, "state")
	config.HostnamePath = filepath.Join(f.dir, "hostname")
	config.GamerHome = filepath.Join(f.dir, "home")
	config.ImageInfoPath = filepath.Join(f.dir, "image.json")
	config.OSReleasePath = filepath.Join(f.dir, "os-release")
	config.ProcCmdline = filepath.Join(f.dir, "cmdline")
	hwmonRoot = filepath.Join(f.dir, "hwmon")
	uptimePath = filepath.Join(f.dir, "uptime")
	cpuinfoPath = filepath.Join(f.dir, "cpuinfo")
	for _, d := range []string{config.StateDir, config.GamerHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.svc = NewService(config.Defaults())
	f.svc.uid, f.svc.gid = os.Getuid(), os.Getgid()
	f.svc.run = func(ctx context.Context, name string, args ...string) error {
		line := strings.Join(append([]string{name}, args...), " ")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cmds = append(f.cmds, line)
		return f.fail[line]
	}
	f.svc.lookPath = func(string) (string, error) { return "/usr/bin/hostnamectl", nil }
	f.svc.setKernelHn = func(string) error { t.Error("unexpected kernel hostname call"); return nil }
	f.svc.probeGPU = func() display.GPUInfo {
		return display.GPUInfo{Vendor: "amd", Name: "RX 9070 XT", Driver: "amdgpu", Card: "/dev/dri/card1", Supported: true}
	}
	f.svc.localIPs = func() []string { return []string{"192.168.1.50"} }
	f.svc.publishEvent = func(topic string, data any) {
		b, _ := json.Marshal(data)
		f.mu.Lock()
		f.evs = append(f.evs, topic+" "+string(b))
		f.mu.Unlock()
	}
	return f
}

func (f *fixture) write(path, content string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cmds
	f.cmds = nil
	return out
}

func call(h http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestValidateHostname(t *testing.T) {
	good := []string{"vapor", "a", "den-pc", "pc2", "1a", strings.Repeat("x", 63)}
	bad := []string{"", "-vapor", "vapor-", "Vapor", "va_por", "va.por", "vapör", "localhost", strings.Repeat("x", 64), "a b"}
	for _, n := range good {
		if err := ValidateHostname(n); err != nil {
			t.Errorf("%q rejected: %v", n, err)
		}
	}
	for _, n := range bad {
		if ValidateHostname(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestSetHostname(t *testing.T) {
	f := newFixture(t)
	f.write(config.HostnamePath, "vapor\n")
	w := call(f.svc.handleHostname, "PUT", `{"hostname":" Den-PC "}`)
	if w.Code != 200 {
		t.Fatalf("PUT hostname: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(config.HostnamePath); string(b) != "den-pc\n" {
		t.Fatalf("/etc/hostname = %q", b)
	}
	want := []string{"hostnamectl set-hostname den-pc", "systemctl try-restart avahi-daemon.service"}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands %q, want %q", got, want)
	}

	// No hostnamectl: the kernel hostname is set directly.
	var kernel string
	f.svc.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	f.svc.setKernelHn = func(n string) error { kernel = n; return nil }
	if err := f.svc.SetHostname(context.Background(), "gamer"); err != nil {
		t.Fatal(err)
	}
	if kernel != "gamer" {
		t.Fatalf("kernel hostname %q", kernel)
	}
	if got := f.commands(); !reflect.DeepEqual(got, []string{"systemctl try-restart avahi-daemon.service"}) {
		t.Fatalf("commands %q", got)
	}

	for _, body := range []string{`{"hostname":"bad name"}`, `{"hostname":""}`, `nope`} {
		if w := call(f.svc.handleHostname, "PUT", body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if b, _ := os.ReadFile(config.HostnamePath); string(b) != "gamer\n" {
		t.Fatalf("invalid names changed the hostname: %q", b)
	}
}

func TestPowerRespondsThenActs(t *testing.T) {
	f := newFixture(t)
	f.svc.powerDelay = 50 * time.Millisecond
	acted := make(chan time.Time, 4)
	var failNext atomic.Bool
	h := f.svc.handlePower("reboot", "Restarting…", func(ctx context.Context) error {
		failing := failNext.Load()
		acted <- time.Now()
		if failing {
			return errors.New("boom")
		}
		return nil
	})
	start := time.Now()
	w := call(h, "POST", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("response %d %q", w.Code, w.Body)
	}
	if len(acted) != 0 {
		t.Fatal("acted before responding")
	}
	// A second click while pending does not act twice.
	call(h, "POST", "")
	at := <-acted
	if at.Sub(start) < 50*time.Millisecond {
		t.Fatalf("acted after %v, before the delay", at.Sub(start))
	}
	select {
	case <-acted:
		t.Fatal("acted twice")
	case <-time.After(150 * time.Millisecond):
	}

	// A failure is reported and allows a retry.
	f.svc.powerPending.Store(false)
	failNext.Store(true)
	call(h, "POST", "")
	<-acted
	deadline := time.Now().Add(2 * time.Second)
	for f.svc.powerPending.Load() {
		if time.Now().After(deadline) {
			t.Fatal("pending not cleared after a failure")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The error event is published right after pending clears.
	for !func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.evs) >= 3 }() {
		if time.Now().After(deadline) {
			t.Fatal("no error event")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	evs := strings.Join(f.evs, "\n")
	f.mu.Unlock()
	if !strings.Contains(evs, `system.message {"level":"info","text":"Restarting…"}`) || !strings.Contains(evs, `"level":"error"`) {
		t.Fatalf("events: %s", evs)
	}
}

// Reboot shares the web UI's guard: it never reboots while a reboot or
// poweroff is on its way, and a failure lets the next one through.
func TestRebootSharesTheGuard(t *testing.T) {
	f := newFixture(t)
	reboots := 0
	var fail error
	f.svc.reboot = func(context.Context) error { reboots++; return fail }
	if ok, err := f.svc.Reboot(context.Background(), "Restarting to finish adding CoolerControl"); !ok || err != nil || reboots != 1 {
		t.Fatalf("Reboot = %v, %v (%d reboots)", ok, err, reboots)
	}
	if ok, _ := f.svc.Reboot(context.Background(), "again"); ok || reboots != 1 {
		t.Fatalf("rebooted twice: %v, %d", ok, reboots)
	}
	f.mu.Lock()
	evs := strings.Join(f.evs, "\n")
	f.mu.Unlock()
	if evs != `system.message {"level":"info","text":"Restarting to finish adding CoolerControl"}` {
		t.Fatalf("events: %s", evs)
	}

	f.svc.powerPending.Store(false)
	fail = errors.New("boom")
	if ok, err := f.svc.Reboot(context.Background(), "x"); !ok || err == nil || f.svc.powerPending.Load() {
		t.Fatalf("failed Reboot = %v, %v, pending %v", ok, err, f.svc.powerPending.Load())
	}
	f.svc.powerPending.Store(true) // the web UI's poweroff is on its way
	if ok, _ := f.svc.Reboot(context.Background(), "x"); ok {
		t.Fatal("rebooted while a poweroff was pending")
	}
}

func TestInfo(t *testing.T) {
	f := newFixture(t)
	f.write(config.HostnamePath, "vapor\n")
	f.write(config.ImageInfoPath, `{"version":"20260929.123456","channel":"main","rollback_index":1}`)
	f.write(config.ProcCmdline, "quiet vos.slot=b console=ttyS0\n")
	f.write(uptimePath, "3725.42 1000.00\n")
	f.write(cpuinfoPath, "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD Ryzen 7  9800X3D 8-Core Processor\n\nprocessor\t: 1\nmodel name\t: other\n")
	f.write(filepath.Join(hwmonRoot, "hwmon0", "name"), "k10temp\n")
	f.write(filepath.Join(hwmonRoot, "hwmon0", "temp1_input"), "54250\n")
	f.write(filepath.Join(hwmonRoot, "hwmon0", "temp1_label"), "Tctl\n")

	w := call(f.svc.handleInfo, "GET", "")
	if w.Code != 200 {
		t.Fatalf("GET /system: %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{
		"hostname": "vapor", "version": "20260929.123456", "channel": "main", "booted_slot": "b",
		"uptime_s": 3725.0, "cpu": "AMD Ryzen 7 9800X3D 8-Core Processor", "mdns": "vapor.local",
	} {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	gpu := got["gpu"].(map[string]any)
	if gpu["vendor"] != "amd" || gpu["supported"] != true || gpu["driver"] != "amdgpu" {
		t.Errorf("gpu = %v", gpu)
	}
	if ips := got["ips"].([]any); len(ips) != 1 || ips[0] != "192.168.1.50" {
		t.Errorf("ips = %v", ips)
	}
	disk := got["disk"].(map[string]any)
	if disk["data_total"].(float64) <= 0 || disk["data_free"].(float64) <= 0 || disk["data_free"].(float64) > disk["data_total"].(float64) {
		t.Errorf("disk = %v", disk)
	}
	temps := got["temps"].([]any)
	if len(temps) != 1 || temps[0].(map[string]any)["name"] != "k10temp Tctl" || temps[0].(map[string]any)["c"] != 54.3 {
		t.Errorf("temps = %v", temps)
	}
}

func TestInfoDegradesGracefully(t *testing.T) {
	f := newFixture(t)
	f.svc.localIPs = func() []string { return nil }
	config.StateDir = filepath.Join(f.dir, "missing")
	info := f.svc.Info()
	if info.UptimeS != 0 || info.Disk.DataTotal != 0 || info.IPs == nil || info.Temps == nil || info.CPU == "" {
		t.Fatalf("info = %+v", info)
	}
	b, _ := json.Marshal(info)
	if !strings.Contains(string(b), `"ips":[]`) || !strings.Contains(string(b), `"temps":[]`) {
		t.Fatalf("empty lists must be [] not null: %s", b)
	}
}

func TestReadTemps(t *testing.T) {
	f := newFixture(t)
	w := func(chip, file, v string) { f.write(filepath.Join(hwmonRoot, chip, file), v+"\n") }
	w("hwmon10", "name", "nvme")
	w("hwmon10", "temp1_input", "41850")
	w("hwmon10", "temp1_label", "Composite")
	w("hwmon2", "name", "amdgpu")
	w("hwmon2", "temp1_input", "48000")
	w("hwmon2", "temp1_label", "edge")
	w("hwmon2", "temp2_input", "61000")
	w("hwmon2", "temp2_label", "junction")
	w("hwmon2", "temp10_input", "55000")
	w("hwmon3", "name", "nvme")
	w("hwmon3", "temp1_input", "39900")
	w("hwmon3", "temp1_label", "Composite")
	w("hwmon4", "name", "acpitz")
	w("hwmon4", "temp1_input", "-273000") // absent sensor
	w("hwmon4", "temp2_input", "garbage")
	w("hwmon4", "temp3_input", "27800")
	w("hwmon5", "temp1_input", "30000") // no name file
	got := readTemps(hwmonRoot)
	want := []Temp{
		{"amdgpu edge", 48},
		{"amdgpu junction", 61},
		{"amdgpu temp10", 55},
		{"nvme Composite", 39.9},
		{"acpitz temp3", 27.8},
		{"hwmon5", 30},
		{"nvme Composite #2", 41.9},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("temps\n got %v\nwant %v", got, want)
	}
	if got := readTemps(filepath.Join(f.dir, "none")); got == nil || len(got) != 0 {
		t.Fatalf("missing hwmon: %v", got)
	}
}

func TestVersionFallbacks(t *testing.T) {
	newFixture(t)
	if v := Version(); v != config.BinaryVersion {
		t.Fatalf("no files: %q", v)
	}
	os.WriteFile(config.OSReleasePath, []byte("NAME=\"VaporOS\"\nIMAGE_VERSION=@VERSION@\nVERSION_ID=20260101.000000\n"), 0o644)
	if v := Version(); v != "20260101.000000" {
		t.Fatalf("os-release: %q", v)
	}
	os.WriteFile(config.OSReleasePath, []byte("IMAGE_VERSION=\"20260102.000000\"\nVERSION_ID=x\n"), 0o644)
	if v := Version(); v != "20260102.000000" {
		t.Fatalf("os-release IMAGE_VERSION: %q", v)
	}
	os.WriteFile(config.ImageInfoPath, []byte(`{"version":"20260929.1"}`), 0o644)
	if v := Version(); v != "20260929.1" {
		t.Fatalf("image.json: %q", v)
	}
}

func TestNaturalLess(t *testing.T) {
	s := []string{"hwmon10", "hwmon2", "hwmon1", "temp10_input", "temp2_input", "hwmon02"}
	sortNatural(s)
	want := []string{"hwmon1", "hwmon2", "hwmon02", "hwmon10", "temp2_input", "temp10_input"}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("%v", s)
	}
}

// ---- SSH

func edKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

func rsaKey(t *testing.T, bits int) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
}

func TestNormalizeKeys(t *testing.T) {
	a, b := edKey(t, "jasper@mac"), edKey(t, "")
	aBare := strings.Join(strings.Fields(a)[:2], " ")
	got, err := NormalizeKeys([]string{"  " + a + "  ", "", b, aBare + " duplicate", " \t"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("normalized %q", got)
	}
	if got, _ := NormalizeKeys(nil); got == nil || len(got) != 0 {
		t.Fatal("nil keys must normalize to []")
	}
	if got, err := NormalizeKeys([]string{aBare + " weird\tcomment\x7f  here"}); err != nil || got[0] != aBare+" weird comment here" {
		t.Fatalf("comment cleaning: %q %v", got, err)
	}
	if _, err := NormalizeKeys([]string{rsaKey(t, 2048)}); err != nil {
		t.Fatalf("2048-bit RSA rejected: %v", err)
	}
	bad := map[string]string{
		"garbage":        "not a key",
		"truncated":      aBare[:40],
		"two lines":      a + "\n" + b,
		"options":        `command="/bin/sh" ` + a,
		"weak rsa":       rsaKey(t, 1024),
		"private marker": "-----BEGIN OPENSSH PRIVATE KEY-----",
	}
	for name, k := range bad {
		if _, err := NormalizeKeys([]string{k}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	many := make([]string, maxSSHKeys+1)
	for i := range many {
		many[i] = edKey(t, "")
	}
	if _, err := NormalizeKeys(many); err == nil {
		t.Error("too many keys accepted")
	}
}

func keysPath() string { return filepath.Join(config.GamerHome, ".ssh", "authorized_keys") }

func TestSSHEnableDisable(t *testing.T) {
	f := newFixture(t)
	k1, k2 := edKey(t, "one"), edKey(t, "two")

	w := call(f.svc.handleGetSSH, "GET", "")
	if strings.TrimSpace(w.Body.String()) != `{"enabled":false,"keys":[]}` {
		t.Fatalf("initial GET: %s", w.Body)
	}
	if w := call(f.svc.handlePutSSH, "PUT", `{"enabled":true,"keys":[]}`); w.Code != 400 {
		t.Fatalf("enable without keys: %d", w.Code)
	}
	if w := call(f.svc.handlePutSSH, "PUT", `{"enabled":true,"keys":["nope"]}`); w.Code != 400 {
		t.Fatalf("bad key: %d", w.Code)
	}
	if cmds := f.commands(); len(cmds) != 0 {
		t.Fatalf("rejected requests ran %q", cmds)
	}

	body, _ := json.Marshal(SSHState{Enabled: true, Keys: []string{k1, k2, k1}})
	w = call(f.svc.handlePutSSH, "PUT", string(body))
	if w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	var st SSHState
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.Enabled || !reflect.DeepEqual(st.Keys, []string{k1, k2}) {
		t.Fatalf("PUT response %+v", st)
	}
	want := []string{"systemctl enable --now sshd.service", "systemctl reload-or-restart vos-firewall.service"}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("enable ran %q", got)
	}
	b, err := os.ReadFile(keysPath())
	if err != nil || string(b) != k1+"\n"+k2+"\n" {
		t.Fatalf("authorized_keys = %q, %v", b, err)
	}
	if fi, _ := os.Stat(keysPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(keysPath())); fi.Mode().Perm() != 0o700 {
		t.Fatalf(".ssh mode %v", fi.Mode().Perm())
	}
	saved, err := config.Load()
	if err != nil || !saved.SSH.Enabled || len(saved.SSH.Keys) != 2 {
		t.Fatalf("config.json ssh = %+v, %v", saved.SSH, err)
	}
	if w := call(f.svc.handleGetSSH, "GET", ""); !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("GET after enable: %s", w.Body)
	}
	entries, _ := os.ReadDir(filepath.Dir(keysPath()))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}

	w = call(f.svc.handlePutSSH, "PUT", `{"enabled":false,"keys":["`+k2+`"]}`)
	if w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	want = []string{"systemctl disable --now sshd.service", "systemctl reload-or-restart vos-firewall.service"}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("disable ran %q", got)
	}
	if _, err := os.Stat(keysPath()); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys still there: %v", err)
	}
	saved, _ = config.Load()
	if saved.SSH.Enabled || len(saved.SSH.Keys) != 1 {
		t.Fatalf("config after disable: %+v", saved.SSH)
	}
}

func TestSSHCommandFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.fail["systemctl enable --now sshd.service"] = errors.New("unit not found")
	w := call(f.svc.handlePutSSH, "PUT", `{"enabled":true,"keys":["`+edKey(t, "")+`"]}`)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "starting sshd failed") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestSSHSaveFailureChangesNothing(t *testing.T) {
	f := newFixture(t)
	// config.json cannot be written: its directory is a file.
	if err := os.RemoveAll(config.StateDir); err != nil {
		t.Fatal(err)
	}
	f.write(config.StateDir, "not a directory")
	w := call(f.svc.handlePutSSH, "PUT", `{"enabled":true,"keys":["`+edKey(t, "")+`"]}`)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "saving config") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if cmds := f.commands(); len(cmds) != 0 {
		t.Fatalf("an unsaved change ran %q", cmds)
	}
	if w := call(f.svc.handleGetSSH, "GET", ""); strings.TrimSpace(w.Body.String()) != `{"enabled":false,"keys":[]}` {
		t.Fatalf("the unsaved change stayed in memory: %s", w.Body)
	}
}

// Other services change the shared config at the same time; the SSH
// handlers must neither race with them nor save over their changes.
func TestSSHSharesTheConfigLock(t *testing.T) {
	f := newFixture(t)
	k := edKey(t, "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 20 {
			_ = f.svc.cfg.Mutate(func(c *config.Config) { c.Power.IdleMinutes = 30 + i })
		}
	}()
	go func() {
		defer wg.Done()
		for range 10 {
			call(f.svc.handlePutSSH, "PUT", `{"enabled":true,"keys":["`+k+`"]}`)
			call(f.svc.handleGetSSH, "GET", "")
		}
	}()
	wg.Wait()
	saved, err := config.Load()
	if err != nil || saved.Power.IdleMinutes != 49 || !saved.SSH.Enabled {
		t.Fatalf("config.json lost an update: power %+v ssh %+v (%v)", saved.Power, saved.SSH, err)
	}
}

func TestAuthorizedKeysNeverFollowsSymlinks(t *testing.T) {
	f := newFixture(t)
	k := edKey(t, "")
	target := filepath.Join(f.dir, "elsewhere")
	os.MkdirAll(target, 0o755)

	// ~/.ssh is a symlink to somewhere else: it is replaced, not followed.
	if err := os.Symlink(target, filepath.Join(config.GamerHome, ".ssh")); err != nil {
		t.Fatal(err)
	}
	if err := writeAuthorizedKeys(config.GamerHome, os.Getuid(), os.Getgid(), []string{k}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(config.GamerHome, ".ssh")); err != nil || !fi.IsDir() {
		t.Fatalf(".ssh not a real directory: %v %v", fi, err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("wrote through the symlink: %v", entries)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
		t.Fatalf("chmodded the symlink target: %v", fi.Mode().Perm())
	}

	// authorized_keys itself a symlink: replaced by rename, target untouched.
	victim := filepath.Join(f.dir, "victim")
	os.WriteFile(victim, []byte("precious\n"), 0o644)
	os.Remove(keysPath())
	os.Symlink(victim, keysPath())
	if err := writeAuthorizedKeys(config.GamerHome, os.Getuid(), os.Getgid(), []string{k}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious\n" {
		t.Fatalf("victim overwritten: %q", b)
	}
	if fi, _ := os.Lstat(keysPath()); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("authorized_keys still a symlink")
	}
	// A planted symlink at the temp name is not followed either.
	os.Symlink(victim, filepath.Join(config.GamerHome, ".ssh", tmpKeysName))
	if err := writeAuthorizedKeys(config.GamerHome, os.Getuid(), os.Getgid(), []string{k}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious\n" {
		t.Fatalf("victim overwritten via temp name: %q", b)
	}

	// Removal through a symlinked ~/.ssh does not touch the target.
	os.RemoveAll(filepath.Join(config.GamerHome, ".ssh"))
	os.WriteFile(filepath.Join(target, "authorized_keys"), []byte("keep\n"), 0o644)
	os.Symlink(target, filepath.Join(config.GamerHome, ".ssh"))
	if err := removeAuthorizedKeys(config.GamerHome); err == nil {
		t.Fatal("removal followed a symlinked ~/.ssh without complaint")
	}
	if _, err := os.Stat(filepath.Join(target, "authorized_keys")); err != nil {
		t.Fatal("removed a file through the symlink")
	}

	// ~/.ssh as a plain file is an error, not clobbered.
	os.Remove(filepath.Join(config.GamerHome, ".ssh"))
	os.WriteFile(filepath.Join(config.GamerHome, ".ssh"), []byte("x"), 0o600)
	if err := writeAuthorizedKeys(config.GamerHome, os.Getuid(), os.Getgid(), []string{k}); err == nil {
		t.Fatal("wrote into a non-directory ~/.ssh")
	}
	// Nothing to remove is fine.
	os.Remove(filepath.Join(config.GamerHome, ".ssh"))
	if err := removeAuthorizedKeys(config.GamerHome); err != nil {
		t.Fatalf("remove with no ~/.ssh: %v", err)
	}
}

func TestRunSyncsAuthorizedKeys(t *testing.T) {
	f := newFixture(t)
	k := edKey(t, "sync")
	_ = f.svc.cfg.Mutate(func(c *config.Config) { c.SSH = config.SSHConfig{Enabled: true, Keys: []string{k}} })
	f.svc.Run(context.Background())
	if b, _ := os.ReadFile(keysPath()); string(b) != k+"\n" {
		t.Fatalf("Run wrote %q", b)
	}
	_ = f.svc.cfg.Mutate(func(c *config.Config) { c.SSH = config.SSHConfig{Enabled: false, Keys: []string{k}} })
	f.svc.Run(context.Background())
	if _, err := os.Stat(keysPath()); !os.IsNotExist(err) {
		t.Fatal("Run left keys with SSH disabled")
	}
	if cmds := f.commands(); len(cmds) != 0 {
		t.Fatalf("Run touched units: %q", cmds)
	}
}
