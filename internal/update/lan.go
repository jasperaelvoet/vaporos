package update

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// lanState is what the LAN check of `vos health` sees.
type lanState int

const (
	lanNoCarrier lanState = iota // every network device is up without a link, or there is none
	lanNoAddress                 // one is down, or has a link but no usable address
	lanUp                        // one has a link and a usable address
)

// lanProbe is one look at the network hardware.
type lanProbe struct {
	state  lanState
	detail string   // lanNoAddress: which device, and what is wrong with it
	mac    string   // lanUp: the permanent address of the device with the usable address
	macs   []string // the addresses of every network device present
}

const (
	// iffUp is IFF_UP in /sys/class/net/<if>/flags: the interface was brought up.
	iffUp = 0x1
	// arphrdEther is ARPHRD_ETHER in /sys/class/net/<if>/type.
	arphrdEther = "1"
	// netAddrPerm is NET_ADDR_PERM in /sys/class/net/<if>/addr_assign_type:
	// the address is the hardware's own, not random or set by software.
	netAddrPerm = "0"
)

var (
	// NetClassDir lists the network interfaces.
	NetClassDir = "/sys/class/net"
	// interfaceAddrs returns an interface's addresses; tests fake it.
	interfaceAddrs = func(name string) ([]net.Addr, error) {
		ifc, err := net.InterfaceByName(name)
		if err != nil {
			return nil, err
		}
		return ifc.Addrs()
	}
)

// probeLAN looks at the network devices (interfaces with a device link,
// so not lo, bridges, veth or tunnels): up when one that is up with
// carrier has an IPv4 address that is neither loopback nor link-local, or a
// global or unique local IPv6 one. A wired device still down is not
// "nothing plugged in": NetworkManager brings up every one it manages, also
// without a cable, so a down one points at the image. Any other (Wi-Fi, a
// modem) may be down for a switch or a missing SIM, so it counts only when
// it had the LAN on the last good boot (its address is lanMAC).
func probeLAN(lanMAC string) lanProbe {
	ents, _ := os.ReadDir(NetClassDir)
	p := lanProbe{state: lanNoCarrier}
	problem := func(format string, a ...any) {
		if p.state == lanNoCarrier {
			p.state = lanNoAddress
			p.detail = fmt.Sprintf(format, a...)
		}
	}
	for _, e := range ents {
		name := e.Name()
		dir := filepath.Join(NetClassDir, name)
		if name == "lo" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "device")); err != nil {
			continue
		}
		mac := sysLine(dir, "address")
		if mac != "" && strings.Trim(mac, "0:") != "" {
			p.macs = append(p.macs, mac)
		} else {
			mac = ""
		}
		if p.state == lanUp {
			continue
		}
		// Reading carrier fails (EINVAL) on an interface that is down, so
		// without flags a readable carrier says it is up.
		carrier, cerr := os.ReadFile(filepath.Join(dir, "carrier"))
		up := cerr == nil
		if flags, err := strconv.ParseUint(sysLine(dir, "flags"), 0, 64); err == nil {
			up = flags&iffUp != 0
		}
		switch {
		case !up:
			if wired(dir) || (lanMAC != "" && mac == lanMAC) {
				problem("%s is down", name)
			}
			continue
		case strings.TrimSpace(string(carrier)) != "1":
			continue // up, with nothing plugged in
		}
		addrs, _ := interfaceAddrs(name)
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
				p.state, p.detail = lanUp, ""
				// Only the hardware's own address names the device on
				// the next boot too.
				if sysLine(dir, "addr_assign_type") == netAddrPerm {
					p.mac = mac
				}
				break
			}
		}
		if p.state != lanUp {
			problem("%s has a link but no address", name)
		}
	}
	return p
}

// wired reports whether the network device in dir is wired Ethernet: its
// link type is Ethernet, and it is neither Wi-Fi (which reports the same
// type) nor a WWAN modem.
func wired(dir string) bool {
	if sysLine(dir, "type") != arphrdEther {
		return false
	}
	for _, f := range []string{"wireless", "phy80211"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return false
		}
	}
	uevent := strings.Split(sysLine(dir, "uevent"), "\n")
	return !slices.Contains(uevent, "DEVTYPE=wlan") && !slices.Contains(uevent, "DEVTYPE=wwan")
}

// sysLine reads one sysfs attribute of dir, trimmed; "" when unreadable.
func sysLine(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
