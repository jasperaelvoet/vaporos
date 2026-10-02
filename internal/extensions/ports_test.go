package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	const old = `"network":{"ports":[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"127.0.0.1:11986"}]}`
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
	if got := r.portsFile(); got != "tcp 11987\n" || r.reloads() != 1 {
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
	if got := r.portsFile(); got != "tcp 11987\n" || r.reloads() != 3 {
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
	if got := r.portsFile(); got != "tcp 11987\n" || r.reloads() != 0 {
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
	if b := portsFile(nil); len(b) != 0 {
		t.Fatalf("no ports: %q", b)
	}
}

func TestExposedPortsSkipsSunshineAdmin(t *testing.T) {
	r := newRig(t)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "coolercontrol.json"), withPorts(t, shipped["coolercontrol"],
		`[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"127.0.0.1:11986"},{"proto":"tcp","port":47990,"mode":"lan"},{"proto":"udp","port":5000,"mode":"lan"}]`))
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	r.report(bootWith(r, "proton", "coolercontrol"))
	ps, err := exposedPorts()
	must(t, err)
	var got []int
	for _, p := range ps {
		got = append(got, p.port)
	}
	if !slices.Equal(got, []int{11987, 5000}) {
		t.Fatalf("exposed ports %v", got)
	}
}
