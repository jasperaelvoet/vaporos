package extensions

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// bootWith is a boot report of the rig's images ids, mounted.
func bootWith(r *rig, ids ...string) store.BootReport {
	var imgs []image
	for _, id := range ids {
		imgs = append(imgs, r.imgs[id])
	}
	return store.BootReport{Mode: store.ModeEnabled, Set: "1", Mounted: mountedAs(imgs...)}
}

// withPorts is descriptor desc with network.ports replaced by ports.
func withPorts(t *testing.T, desc, ports string) string {
	t.Helper()
	const old = `"network":{"ports":[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"127.0.0.1:11985"}]}`
	if !strings.Contains(desc, old) {
		t.Fatal("the fixture's ports changed")
	}
	return strings.Replace(desc, old, `"network":{"ports":`+ports+`}`, 1)
}

// reloads counts the firewall reloads the rig's systemctl saw.
func (r *rig) reloads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.units {
		if u == "reload "+firewallUnit {
			n++
		}
	}
	return n
}

func (r *rig) portsFile() string {
	r.t.Helper()
	b, err := os.ReadFile(config.ExtPortsPath())
	if errors.Is(err, os.ErrNotExist) {
		return "<missing>"
	}
	must(r.t, err)
	return string(b)
}

// The ports file lists the ports of what runs and is still wanted: adding
// CoolerControl opens 11987 once it is mounted, removing it closes it at
// once, adding it back before the restart opens it again. The firewall is
// reloaded only when the file changes.
func TestPortsFollowMountedAndWanted(t *testing.T) {
	r := newRig(t)
	r.s.syncPorts()
	if got := r.portsFile(); got != "<missing>" || r.reloads() != 0 {
		t.Fatalf("no ports: file %q, %d reloads", got, r.reloads())
	}
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("add: %d %s", code, body)
	}
	r.pass()
	if got := r.portsFile(); got != "<missing>" {
		t.Fatalf("ports %q before the restart mounts it", got)
	}

	r.boot()
	r.s.syncPorts()
	if got := r.portsFile(); got != "tcp 11987 upstream 11985\n" || r.reloads() != 1 {
		t.Fatalf("mounted: file %q, %d reloads", got, r.reloads())
	}
	r.s.syncPorts()
	if r.reloads() != 1 {
		t.Fatalf("an unchanged file reloaded the firewall (%d reloads)", r.reloads())
	}

	if code, body := r.do("DELETE", "/extensions/coolercontrol", ""); code != 200 {
		t.Fatalf("remove: %d %s", code, body)
	}
	if got := r.portsFile(); got != "" || r.reloads() != 2 {
		t.Fatalf("removed: file %q, %d reloads", got, r.reloads())
	}
	if code, body := r.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("add again: %d %s", code, body)
	}
	if got := r.portsFile(); got != "tcp 11987 upstream 11985\n" || r.reloads() != 3 {
		t.Fatalf("added back: file %q, %d reloads", got, r.reloads())
	}
}

// A reload that fails is tried again by the next sync, file unchanged.
func TestPortsReloadRetried(t *testing.T) {
	r := newRig(t)
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	r.report(bootWith(r, "proton", "coolercontrol"))
	fail := true
	r.s.cc.systemctl = func(_ context.Context, _ bool, args ...string) error {
		if fail {
			return errors.New("vos-firewall.service is not active")
		}
		r.mu.Lock()
		r.units = append(r.units, args[0]+" "+args[1])
		r.mu.Unlock()
		return nil
	}
	r.s.syncPorts()
	if got := r.portsFile(); got != "tcp 11987 upstream 11985\n" || r.reloads() != 0 {
		t.Fatalf("file %q, %d reloads", got, r.reloads())
	}
	fail = false
	r.s.syncPorts()
	if r.reloads() != 1 {
		t.Fatalf("the failed reload was not tried again (%d reloads)", r.reloads())
	}
}

func TestPortsFile(t *testing.T) {
	got := string(portsFile([]exposed{{proto: "udp", port: 27015}, {proto: "tcp", port: 11987}, {proto: "tcp", port: 11987}}))
	if got != "tcp 11987\nudp 27015\n" {
		t.Fatalf("ports file %q", got)
	}
	// A proxied port names its loopback upstream, which only root may reach.
	got = string(portsFile([]exposed{
		{proto: "tcp", port: 11987, mode: "proxied", upstream: "127.0.0.1:11985"},
		{proto: "tcp", port: 8080, mode: "lan"},
		{proto: "tcp", port: 9000, mode: "proxied", upstream: "localhost:9001"},
		{proto: "tcp", port: 9002, mode: "proxied", upstream: "127.0.0.1:80"},
	}))
	if got != "tcp 11987 upstream 11985\ntcp 8080\ntcp 9000\ntcp 9002\n" {
		t.Fatalf("ports file %q", got)
	}
	if b := portsFile(nil); len(b) != 0 {
		t.Fatalf("no ports: %q", b)
	}
}

func TestExposedPortsSkipsSunshineAdmin(t *testing.T) {
	r := newRig(t)
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	r.report(bootWith(r, "proton", "coolercontrol"))
	// Apart: a descriptor whose upstream is one of its own ports is invalid.
	for _, ports := range []string{
		`[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"127.0.0.1:11985"},{"proto":"tcp","port":47990,"mode":"lan"},` +
			`{"proto":"udp","port":5000,"mode":"lan"}]`,
		`[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"127.0.0.1:11985"},` +
			`{"proto":"tcp","port":12000,"mode":"proxied","upstream":"127.0.0.1:47990"},{"proto":"udp","port":5000,"mode":"lan"}]`,
	} {
		writeFile(t, filepath.Join(config.ExtDescriptorsDir, "coolercontrol.json"), withPorts(t, shipped["coolercontrol"], ports))
		ps, err := exposedPorts()
		must(t, err)
		var got []int
		for _, p := range ps {
			got = append(got, p.port)
		}
		if !slices.Equal(got, []int{11987, 5000}) {
			t.Fatalf("%s: exposed ports %v", ports, got)
		}
	}
}

// logs collects what the package logs during the test.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func captureLogs(t *testing.T) *logs {
	t.Helper()
	l := &logs{}
	saved := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(saved) })
	return l
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// count is how many lines contain s.
func (l *logs) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range strings.Split(l.buf.String(), "\n") {
		if strings.Contains(line, s) {
			n++
		}
	}
	return n
}

// A shipped descriptor vosd cannot read is logged once per error, not at
// every look at the ports.
func TestExposedPortsLogsADamagedDescriptorOnce(t *testing.T) {
	r := newRig(t)
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	r.report(bootWith(r, "proton", "coolercontrol"))
	p := filepath.Join(config.ExtDescriptorsDir, "coolercontrol.json")
	good, err := os.ReadFile(p)
	must(t, err)
	l := captureLogs(t)
	writeFile(t, p, "{")
	for i := 0; i < 3; i++ {
		exposedPorts()
	}
	if n := l.count("extensions: coolercontrol: "); n != 1 {
		t.Fatalf("%d log lines for one damaged descriptor:\n%s", n, l.buf.String())
	}
	writeFile(t, p, string(good))
	exposedPorts()
	writeFile(t, p, "{")
	exposedPorts()
	if n := l.count("extensions: coolercontrol: "); n != 2 {
		t.Fatalf("%d log lines after it broke again, want 2", n)
	}
}
