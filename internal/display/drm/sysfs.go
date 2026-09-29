package drm

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Paths are variables so tests can point them at a fake sysfs tree.
var (
	SysClassDRM = "/sys/class/drm"
	DevDRI      = "/dev/dri"
)

var (
	cardRe      = regexp.MustCompile(`^card(\d+)$`)
	connectorRe = regexp.MustCompile(`^(card\d+)-(.+)$`)
)

// SysCard is a DRM primary node as sysfs describes it. Reading sysfs never
// touches the hardware or DRM master, so anyone may call it at any time.
type SysCard struct {
	Name     string // "card0"
	Index    int
	Dev      string // "/dev/dri/card0"
	VendorID uint16 // PCI vendor (0 for platform devices such as simpledrm)
	DeviceID uint16
	Driver   string // kernel driver: "amdgpu", "i915", "virtio_gpu", "simpledrm", …
	PCISlot  string // "0000:0b:00.0"
	BootVGA  bool   // firmware's boot display device
}

// Cards lists the DRM primary nodes, ordered by index.
func Cards() ([]SysCard, error) {
	ents, err := os.ReadDir(SysClassDRM)
	if err != nil {
		return nil, err
	}
	var cards []SysCard
	for _, e := range ents {
		m := cardRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		dir := filepath.Join(SysClassDRM, e.Name(), "device")
		c := SysCard{Name: e.Name(), Index: idx, Dev: filepath.Join(DevDRI, e.Name())}
		c.VendorID = uint16(readHex(filepath.Join(dir, "vendor")))
		c.DeviceID = uint16(readHex(filepath.Join(dir, "device")))
		c.BootVGA = readTrim(filepath.Join(dir, "boot_vga")) == "1"
		if l, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
			c.Driver = filepath.Base(l)
		}
		for _, line := range strings.Split(readTrim(filepath.Join(dir, "uevent")), "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "DRIVER":
				if c.Driver == "" {
					c.Driver = v
				}
			case "PCI_SLOT_NAME":
				c.PCISlot = v
			case "PCI_ID":
				// "1002:7550" — only used when the vendor/device files are absent.
				vs, ds, _ := strings.Cut(v, ":")
				if c.VendorID == 0 {
					n, _ := strconv.ParseUint(vs, 16, 16)
					c.VendorID = uint16(n)
				}
				if c.DeviceID == 0 {
					n, _ := strconv.ParseUint(ds, 16, 16)
					c.DeviceID = uint16(n)
				}
			}
		}
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].Index < cards[j].Index })
	return cards, nil
}

// SysConnector is one /sys/class/drm/cardN-<name> directory.
type SysConnector struct {
	Card    string // "card0"
	Name    string // "DP-1"
	Type    string // "DP", "HDMI-A", "eDP", "Virtual", …
	Status  string // "connected" | "disconnected" | "unknown"
	Enabled bool   // a CRTC currently drives it
	Modes   []string
	Dir     string
}

// Connected reports whether a sink is attached or the connector is forced on.
func (c SysConnector) Connected() bool { return c.Status == "connected" }

// EDID returns the connector's current EDID blob (nil if none).
func (c SysConnector) EDID() []byte {
	b, err := os.ReadFile(filepath.Join(c.Dir, "edid"))
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

// Connectors lists the connectors of card ("card0"), or of every card when
// card is "". Status comes from the kernel's cached state: reading it never
// forces a probe, so polling it is cheap and invisible on screen.
func Connectors(card string) ([]SysConnector, error) {
	ents, err := os.ReadDir(SysClassDRM)
	if err != nil {
		return nil, err
	}
	var out []SysConnector
	for _, e := range ents {
		m := connectorRe.FindStringSubmatch(e.Name())
		if m == nil || (card != "" && m[1] != card) {
			continue
		}
		dir := filepath.Join(SysClassDRM, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "status")); err != nil {
			continue
		}
		c := SysConnector{
			Card:    m[1],
			Name:    m[2],
			Type:    ConnectorType(m[2]),
			Status:  readTrim(filepath.Join(dir, "status")),
			Enabled: readTrim(filepath.Join(dir, "enabled")) == "enabled",
			Dir:     dir,
		}
		for _, l := range strings.Split(readTrim(filepath.Join(dir, "modes")), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				c.Modes = append(c.Modes, l)
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Card != out[j].Card {
			return cardIndex(out[i].Card) < cardIndex(out[j].Card)
		}
		return connectorLess(out[i].Name, out[j].Name)
	})
	return out, nil
}

// ConnectorType strips the instance number: "HDMI-A-1" → "HDMI-A".
func ConnectorType(name string) string {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 {
		return name
	}
	if _, err := strconv.Atoi(name[i+1:]); err != nil {
		return name
	}
	return name[:i]
}

// connectorLess orders by type, then numerically by instance ("DP-2" < "DP-10").
func connectorLess(a, b string) bool {
	ta, tb := ConnectorType(a), ConnectorType(b)
	if ta != tb {
		return ta < tb
	}
	na, _ := strconv.Atoi(strings.TrimPrefix(a, ta+"-"))
	nb, _ := strconv.Atoi(strings.TrimPrefix(b, tb+"-"))
	return na < nb
}

func cardIndex(card string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(card, "card"))
	return n
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(b))
}

func readHex(path string) uint64 {
	s := strings.TrimPrefix(readTrim(path), "0x")
	n, _ := strconv.ParseUint(s, 16, 32)
	return n
}
