package update

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// fakeNet is a /sys/class/net of the test's own for probeLAN.
type fakeNet struct {
	t     *testing.T
	dir   string
	addrs map[string][]string // each interface's addresses, as CIDR
	macs  map[string]string   // each interface's hardware address
}

func newFakeNet(t *testing.T) *fakeNet {
	t.Helper()
	oldDir, oldAddrs := NetClassDir, interfaceAddrs
	t.Cleanup(func() { NetClassDir, interfaceAddrs = oldDir, oldAddrs })
	n := &fakeNet{t: t, dir: t.TempDir(), addrs: map[string][]string{}, macs: map[string]string{}}
	NetClassDir = n.dir
	interfaceAddrs = func(name string) ([]net.Addr, error) {
		var out []net.Addr
		for _, a := range n.addrs[name] {
			ip, ipn, err := net.ParseCIDR(a)
			if err != nil {
				return nil, err
			}
			ipn.IP = ip
			out = append(out, ipn)
		}
		return out, nil
	}
	return n
}

// nic adds (or replaces) an interface: wired Ethernet with a hardware
// address of its own. flags "" leaves that file out, and carrier "" too:
// the kernel refuses to read it while the interface is down.
func (n *fakeNet) nic(name string, device bool, flags, carrier string, addrs ...string) {
	n.t.Helper()
	os.RemoveAll(filepath.Join(n.dir, name))
	n.set(name, "type", "1")
	n.set(name, "addr_assign_type", "0")
	n.set(name, "address", n.mac(name))
	if device {
		n.set(name, "device/", "")
	}
	if flags != "" {
		n.set(name, "flags", flags)
	}
	if carrier != "" {
		n.set(name, "carrier", carrier)
	}
	n.addrs[name] = addrs
}

// set writes one attribute of an interface; a file ending in / is a directory.
func (n *fakeNet) set(name, file, value string) {
	n.t.Helper()
	p := filepath.Join(n.dir, name, file)
	err := os.MkdirAll(filepath.Dir(p), 0o755)
	if err == nil && strings.HasSuffix(file, "/") {
		err = os.MkdirAll(p, 0o755)
	} else if err == nil {
		err = os.WriteFile(p, []byte(value+"\n"), 0o644)
	}
	if err != nil {
		n.t.Fatal(err)
	}
}

// mac is the hardware address nic gives the interface name.
func (n *fakeNet) mac(name string) string {
	if _, ok := n.macs[name]; !ok {
		n.macs[name] = fmt.Sprintf("52:54:00:00:00:%02x", len(n.macs)+1)
	}
	return n.macs[name]
}

func TestProbeLAN(t *testing.T) {
	setup(t)
	n := newFakeNet(t)
	probe := func(lanMAC string, want lanState, what string) lanProbe {
		t.Helper()
		p := probeLAN(lanMAC)
		if p.state != want {
			t.Fatalf("%s: %+v, want state %v", what, p, want)
		}
		return p
	}

	if p := probe("", lanNoCarrier, "no interfaces"); len(p.macs) != 0 {
		t.Fatalf("devices %v", p.macs)
	}
	n.nic("lo", true, "0x9", "1", "127.0.0.1/8", "::1/128")
	n.nic("docker0", false, "0x1003", "1", "172.17.0.1/16") // virtual: no device
	n.nic("docker1", false, "0x1002", "")                   // virtual and down
	n.nic("eno1", true, "0x1003", "0", "192.168.1.5/24")    // up, nothing plugged in
	n.nic("wlan0", true, "", "0")                           // no flags: a readable carrier means up
	n.set("wlan0", "wireless/", "")
	n.nic("eth9", true, "0x1003", "0")
	n.set("eth9", "address", "00:00:00:00:00:00") // none of its own: not listed
	p := probe("", lanNoCarrier, "only lo, bridges and devices without a link")
	if !slices.Equal(p.macs, []string{n.mac("eno1"), n.mac("wlan0")}) {
		t.Fatalf("devices %v", p.macs)
	}

	// Radios and modems may be down for a switch or a missing SIM; only the
	// one that had the LAN counts.
	n.nic("wlan1", true, "0x1002", "")
	n.set("wlan1", "phy80211/", "")
	n.nic("wwan0", true, "0x1002", "")
	n.set("wwan0", "type", "65534") // ARPHRD_NONE: raw IP
	n.nic("wwan1", true, "0x1002", "")
	n.set("wwan1", "uevent", "DEVTYPE=wwan\nINTERFACE=wwan1")
	// A radio known only by its uevent (no wireless/ or phy80211).
	n.nic("wlan2", true, "0x1002", "")
	n.set("wlan2", "uevent", "DEVTYPE=wlan\nINTERFACE=wlan2")
	probe("", lanNoCarrier, "down radios and modems next to a device without a cable")
	probe(n.mac("eno1"), lanNoCarrier, "down radios and modems, another device had the LAN")
	for _, name := range []string{"wlan1", "wlan2", "wwan0", "wwan1"} {
		if p := probe(n.mac(name), lanNoAddress, name+" down, and it had the LAN"); p.detail != name+" is down" {
			t.Fatalf("detail %q", p.detail)
		}
	}

	n.nic("enp5s0", true, "0x1002", "") // down, so its carrier cannot be read
	if p := probe("", lanNoAddress, "a wired device that is down"); p.detail != "enp5s0 is down" || len(p.macs) != 7 {
		t.Fatalf("down: %+v", p)
	}
	n.nic("enp5s0", true, "", "") // neither flags nor carrier readable
	probe("", lanNoAddress, "a device whose carrier cannot be read")
	n.nic("enp5s0", true, "0x1002", "1", "10.0.0.7/8") // flags say down, whatever carrier says
	probe("", lanNoAddress, "a down device with a carrier file")

	n.nic("enp5s0", true, "0x1003", "1", "169.254.10.2/16", "fe80::1/64")
	if p := probe("", lanNoAddress, "link-local only"); p.detail != "enp5s0 has a link but no address" {
		t.Fatalf("detail %q", p.detail)
	}
	n.addrs["enp5s0"] = append(n.addrs["enp5s0"], "fd12:3456::2/64")
	if p := probe("", lanUp, "a ULA address"); p.mac != n.mac("enp5s0") || p.detail != "" || len(p.macs) != 7 {
		t.Fatalf("up: %+v", p)
	}
	n.addrs["enp5s0"] = []string{"10.0.0.7/8"}
	probe("", lanUp, "a private IPv4 address")

	// An address that is random or set by software (or of an unknown kind)
	// may be another one next boot: the device is listed, not recorded.
	for _, typ := range []string{"1", "3", ""} {
		n.set("enp5s0", "addr_assign_type", typ)
		if p := probe("", lanUp, "addr_assign_type "+typ); p.mac != "" || !slices.Contains(p.macs, n.mac("enp5s0")) {
			t.Fatalf("addr_assign_type %q: %+v", typ, p)
		}
	}
	n.set("enp5s0", "addr_assign_type", "0")

	// Another device down does not matter once one has the LAN, and every
	// device present is still listed.
	n.nic("enp6s0", true, "0x1002", "")
	if p := probe("", lanUp, "one up, one down"); len(p.macs) != 8 {
		t.Fatalf("devices %v", p.macs)
	}
}

// The LAN check of a boot that had the LAN, on network devices as sysfs
// shows them: which down devices fail it, and what health-ok keeps.
func TestHealthLANDevices(t *testing.T) {
	cases := []struct {
		name string
		nics func(n *fakeNet)
		had  string // the device that had the LAN (health-ok's lan_mac), or ""
		code int
		kept string // the device health-ok names afterwards, or ""
	}{
		{name: "a down radio next to a device without a cable", had: "eno1", kept: "eno1", nics: func(n *fakeNet) {
			n.nic("eno1", true, "0x1003", "0")
			n.nic("wlan0", true, "0x1002", "")
			n.set("wlan0", "wireless/", "")
		}},
		{name: "a down modem next to a device without a cable", had: "eno1", kept: "eno1", nics: func(n *fakeNet) {
			n.nic("eno1", true, "0x1003", "0")
			n.nic("wwan0", true, "0x1002", "")
			n.set("wwan0", "uevent", "DEVTYPE=wwan")
		}},
		{name: "a down wired device", had: "eth0", code: 1, nics: func(n *fakeNet) {
			n.nic("eno1", true, "0x1003", "0")
			n.nic("eth0", true, "0x1002", "")
		}},
		{name: "a down wired device, none known", code: 1, nics: func(n *fakeNet) {
			n.nic("eth0", true, "0x1002", "")
		}},
		{name: "the radio that had the LAN, down", had: "wlan0", code: 1, nics: func(n *fakeNet) {
			n.nic("eno1", true, "0x1003", "0")
			n.nic("wlan0", true, "0x1002", "")
			n.set("wlan0", "wireless/", "")
		}},
		{name: "up on a random address: none recorded", nics: func(n *fakeNet) {
			n.nic("eth0", true, "0x1003", "1", "10.0.0.7/8")
			n.set("eth0", "addr_assign_type", "1")
		}},
		{name: "up on the hardware's address: recorded", kept: "eth0", nics: func(n *fakeNet) {
			n.nic("eth0", true, "0x1003", "1", "10.0.0.7/8")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			n := newFakeNet(t)
			c.nics(n)
			mac := func(name string) string {
				if name == "" {
					return ""
				}
				return n.mac(name)
			}
			e.write(config.HealthOKPath(), fmt.Sprintf(`{"gpu":true,"stream":true,"lan":true,"lan_mac":%q}`, mac(c.had)))
			f := healthy()
			f.counting = true
			f.probe = probeLAN
			code, log := runFake(t, f)
			if code != c.code {
				t.Fatalf("exit %d, want %d\n%s", code, c.code, log)
			}
			if f.lanMAC != mac(c.had) {
				t.Fatalf("probed with lan_mac %q, want %q", f.lanMAC, mac(c.had))
			}
			if c.code == 0 {
				if h := healthOK(t); !h.LAN || h.LANMAC != mac(c.kept) {
					t.Fatalf("health-ok %+v, want lan_mac %q", h, mac(c.kept))
				}
			}
		})
	}
}
