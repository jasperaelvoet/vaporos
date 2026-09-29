package power

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WoLIface is one wired interface and its Wake-on-LAN state. The image's
// 50-vos-wol.link sets WakeOnLan=magic on every wired NIC at boot; this is
// what the kernel reports it actually got.
type WoLIface struct {
	Iface     string `json:"iface"`
	MAC       string `json:"mac"`
	Enabled   bool   `json:"enabled"`   // magic-packet wake armed ("Wake-on: g")
	Supported bool   `json:"supported"` // the NIC can wake on a magic packet
	// The interface's IPv4 address and prefix length, and the subnet's
	// broadcast address, where a Wake-on-LAN app sends the magic packet.
	// All three are omitted without an IPv4 address, and Broadcast on a
	// /31 or /32, which has none.
	IPv4      string `json:"ipv4,omitempty"`
	Prefix    int    `json:"prefix,omitempty"`
	Broadcast string `json:"broadcast,omitempty"`
}

// arphrdEther is ARPHRD_ETHER, /sys/class/net/<if>/type for Ethernet.
const arphrdEther = "1"

// ethernetIfaces lists physical wired interfaces with their MAC address:
// Ethernet link type, backed by a device (not a bridge, veth or tun), and
// not wireless (which reports the same link type).
func ethernetIfaces(sysNet string) [][2]string {
	entries, err := os.ReadDir(sysNet)
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, e := range entries {
		dir := filepath.Join(sysNet, e.Name())
		if readTrim(filepath.Join(dir, "type")) != arphrdEther {
			continue
		}
		if !fileExists(filepath.Join(dir, "device")) ||
			fileExists(filepath.Join(dir, "wireless")) || fileExists(filepath.Join(dir, "phy80211")) {
			continue
		}
		out = append(out, [2]string{e.Name(), readTrim(filepath.Join(dir, "address"))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// parseEthtoolWoL extracts the "Supports Wake-on:" and "Wake-on:" mode
// letters from `ethtool <if>` output (e.g. "pumbg" and "g").
func parseEthtoolWoL(out string) (supports, current string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "Supports Wake-on:"); ok {
			supports = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "Wake-on:"); ok {
			current = strings.TrimSpace(v)
		}
	}
	return supports, current
}

// WakeOnLANCapable reports whether a wired NIC can wake this machine with
// a magic packet (ethtool "Supports Wake-on" includes g). The installer
// turns idle shutdown on only then: a machine that switches itself off
// must be able to be woken again.
func WakeOnLANCapable() bool {
	return wolCapable(context.Background(), "/sys/class/net", runEthtool)
}

func wolCapable(ctx context.Context, sysNet string, ethtool func(context.Context, string) (string, error)) bool {
	for _, w := range wolStatus(ctx, sysNet, ethtool) {
		if w.Supported {
			return true
		}
	}
	return false
}

func (s *Service) wolStatus(ctx context.Context) []WoLIface {
	out := wolStatus(ctx, s.sysNet, s.ethtool)
	for i := range out {
		if addrs, err := s.ifaceAddrs(out[i].Iface); err == nil {
			out[i].setIPv4(addrs)
		}
	}
	return out
}

// ifaceAddrs lists the addresses of the interface called name.
func ifaceAddrs(name string) ([]net.Addr, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return ifc.Addrs()
}

// setIPv4 takes the first IPv4 address that is neither loopback nor
// link-local: a link-local address says the LAN gave the machine none.
func (w *WoLIface) setIPv4(addrs []net.Addr) {
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := n.IP.To4()
		ones, bits := n.Mask.Size()
		if ip == nil || bits != 32 || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		w.IPv4, w.Prefix = ip.String(), ones
		if ones <= 30 {
			b := make(net.IP, net.IPv4len)
			for i := range b {
				b[i] = ip[i] | ^n.Mask[i]
			}
			w.Broadcast = b.String()
		}
		return
	}
}

// wolStatus asks ethtool about every wired interface. ethtool prints the
// link settings it could read even when it exits non-zero (for example
// when a virtual NIC has no Wake-on-LAN at all), so its output is parsed
// regardless of the exit status.
func wolStatus(ctx context.Context, sysNet string, ethtool func(context.Context, string) (string, error)) []WoLIface {
	out := []WoLIface{}
	for _, ifc := range ethernetIfaces(sysNet) {
		ectx, cancel := context.WithTimeout(ctx, 3*time.Second)
		text, _ := ethtool(ectx, ifc[0])
		cancel()
		supports, current := parseEthtoolWoL(text)
		out = append(out, WoLIface{
			Iface:     ifc[0],
			MAC:       ifc[1],
			Enabled:   strings.ContainsRune(current, 'g'),
			Supported: strings.ContainsRune(supports, 'g'),
		})
	}
	return out
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
