// Package display owns everything about screens: GPU detection and vendor
// profiles, DRM connector state (pure-Go ioctls in display/drm), the EDID
// generator (display/edid), the welcome renderer (`vos welcome`, in
// display/welcome), gamescope control, and vosd's display policy (welcome
// vs gamescope, following the Sunshine client's mode). See docs/CONTRACTS.md.
package display

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
)

// Paths are variables so tests can point them into a temp dir.
var (
	// PCIIDsPath names GPUs for the UI ("Navi 48 [Radeon RX 9070 XT]").
	PCIIDsPath = "/usr/share/hwdata/pci.ids"
	// SysBusPCI finds display controllers that have no DRM driver bound.
	SysBusPCI = "/sys/bus/pci/devices"
)

// GPUInfo describes the primary GPU.
type GPUInfo struct {
	Vendor    string `json:"vendor"` // "amd" | "intel" | "nvidia" | "virtual" | ""
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	Card      string `json:"card"` // /dev/dri/cardN
	Supported bool   `json:"supported"`

	cardName string // "card1", to find its connectors in sysfs
}

// Profile returns the vendor profile, or nil when there is none.
func (g GPUInfo) Profile() Profile { return ProfileFor(g) }

// CardName is the sysfs name of the card ("card1"), "" without a DRM card.
func (g GPUInfo) CardName() string { return g.cardName }

// Profile is what VaporOS knows about streaming from one GPU vendor.
type Profile interface {
	Name() string     // "amd", "intel", "nvidia"
	Supported() bool  // VaporOS streams with it today
	Encoder() string  // Sunshine encoder
	Capture() string  // Sunshine capture method
	VirtualHDR() bool // HDR works on the forced virtual connector
}

type amdProfile struct{}

func (amdProfile) Name() string     { return "amd" }
func (amdProfile) Supported() bool  { return true }
func (amdProfile) Encoder() string  { return "vulkan" }
func (amdProfile) Capture() string  { return "kms" }
func (amdProfile) VirtualHDR() bool { return true }

// Intel is detected so the UI can say what it found, but not supported yet:
// VA-API encode and the forced-connector path are untested on it.
type intelProfile struct{}

func (intelProfile) Name() string     { return "intel" }
func (intelProfile) Supported() bool  { return false }
func (intelProfile) Encoder() string  { return "vaapi" }
func (intelProfile) Capture() string  { return "kms" }
func (intelProfile) VirtualHDR() bool { return false }

// NVIDIA needs the proprietary driver and NVENC; it cannot do HDR on a
// forced virtual connector.
type nvidiaProfile struct{}

func (nvidiaProfile) Name() string     { return "nvidia" }
func (nvidiaProfile) Supported() bool  { return false }
func (nvidiaProfile) Encoder() string  { return "nvenc" }
func (nvidiaProfile) Capture() string  { return "kms" }
func (nvidiaProfile) VirtualHDR() bool { return false }

// ProfileFor maps a GPU to its vendor profile (nil for none/virtual).
func ProfileFor(g GPUInfo) Profile {
	switch g.Vendor {
	case "amd":
		if g.Driver == "amdgpu" {
			return amdProfile{}
		}
		return unsupportedAMD{}
	case "intel":
		return intelProfile{}
	case "nvidia":
		return nvidiaProfile{}
	}
	return nil
}

// unsupportedAMD is an AMD card driven by the old radeon driver (GCN 1/2
// and older): no Vulkan encode.
type unsupportedAMD struct{ amdProfile }

func (unsupportedAMD) Supported() bool { return false }

// PCI vendor ids.
const (
	vendorAMD    = 0x1002
	vendorIntel  = 0x8086
	vendorNVIDIA = 0x10de
)

// Emulated display adapters found in VMs: they light a screen but cannot
// stream.
var virtualVendors = map[uint16]bool{
	0x1af4: true, // virtio
	0x1234: true, // QEMU bochs/std VGA
	0x1b36: true, // Red Hat (QXL)
	0x15ad: true, // VMware
	0x80ee: true, // VirtualBox
	0x1013: true, // Cirrus
	0x1414: true, // Hyper-V
}

var driverNames = map[string]string{
	"virtio_gpu":  "Virtio GPU",
	"bochs-drm":   "QEMU standard VGA",
	"bochs":       "QEMU standard VGA",
	"qxl":         "QXL",
	"vmwgfx":      "VMware SVGA",
	"cirrus":      "Cirrus VGA",
	"cirrus-qemu": "Cirrus VGA",
	"hyperv_drm":  "Hyper-V video",
	"vboxvideo":   "VirtualBox video",
	"simpledrm":   "Firmware framebuffer",
	"efidrm":      "Firmware framebuffer",
	"vesadrm":     "Firmware framebuffer",
}

func vendorOf(id uint16, driver string) string {
	switch {
	case id == vendorAMD:
		return "amd"
	case id == vendorIntel:
		return "intel"
	case id == vendorNVIDIA:
		return "nvidia"
	case virtualVendors[id]:
		return "virtual"
	}
	switch driver {
	case "virtio_gpu", "bochs-drm", "bochs", "qxl", "vmwgfx", "cirrus", "cirrus-qemu", "hyperv_drm", "vboxvideo":
		return "virtual"
	}
	return ""
}

// Probe inspects /sys/class/drm and returns the primary GPU: a supported
// one if present, else any real GPU (even without a DRM driver bound, from
// the PCI bus), else an emulated adapter or firmware framebuffer.
func Probe() GPUInfo {
	cards, _ := drm.Cards()
	var cand []GPUInfo
	for _, c := range cards {
		g := GPUInfo{
			Vendor:   vendorOf(c.VendorID, c.Driver),
			Driver:   c.Driver,
			Card:     c.Dev,
			cardName: c.Name,
		}
		g.Name = gpuName(c.VendorID, c.DeviceID, subsystem(c.PCISlot), c.Driver)
		if p := ProfileFor(g); p != nil {
			g.Supported = p.Supported()
		}
		cand = append(cand, g)
	}
	cand = append(cand, pciOnlyGPUs(cards)...)
	if len(cand) == 0 {
		return GPUInfo{}
	}
	rank := func(g GPUInfo) int {
		switch {
		case g.Supported:
			return 4
		case g.Vendor == "amd" || g.Vendor == "intel" || g.Vendor == "nvidia":
			if g.Card != "" {
				return 3
			}
			return 2
		case g.Card != "":
			return 1
		}
		return 0
	}
	sort.SliceStable(cand, func(i, j int) bool { return rank(cand[i]) > rank(cand[j]) })
	return cand[0]
}

// pciOnlyGPUs finds display controllers (class 0x03xxxx) that no DRM card
// represents, such as an NVIDIA card without a driver, so the UI can name it.
func pciOnlyGPUs(cards []drm.SysCard) []GPUInfo {
	have := map[string]bool{}
	for _, c := range cards {
		if c.PCISlot != "" {
			have[c.PCISlot] = true
		}
	}
	ents, err := os.ReadDir(SysBusPCI)
	if err != nil {
		return nil
	}
	var out []GPUInfo
	for _, e := range ents {
		slot := e.Name()
		dir := filepath.Join(SysBusPCI, slot)
		if have[slot] || !strings.HasPrefix(readTrim(filepath.Join(dir, "class")), "0x03") {
			continue
		}
		vid := uint16(readHex(filepath.Join(dir, "vendor")))
		did := uint16(readHex(filepath.Join(dir, "device")))
		driver := ""
		if l, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
			driver = filepath.Base(l)
		}
		v := vendorOf(vid, driver)
		if v == "virtual" || v == "" {
			continue
		}
		g := GPUInfo{Vendor: v, Driver: driver, Name: gpuName(vid, did, subsystem(slot), driver)}
		out = append(out, g)
	}
	return out
}

// subsystem returns the PCI subsystem vendor/device of a slot (for board
// names such as "Radeon RX 9070 XT 16GB").
func subsystem(slot string) [2]uint16 {
	if slot == "" {
		return [2]uint16{}
	}
	dir := filepath.Join(SysBusPCI, slot)
	return [2]uint16{uint16(readHex(filepath.Join(dir, "subsystem_vendor"))), uint16(readHex(filepath.Join(dir, "subsystem_device")))}
}

// gpuName looks the device up in pci.ids, preferring the board-specific
// subsystem name, and falls back to the driver's name or the raw ids.
func gpuName(vendor, device uint16, sub [2]uint16, driver string) string {
	if n, ok := driverNames[driver]; ok {
		return n
	}
	if vendor == 0 {
		if driver != "" {
			return driver
		}
		return "Unknown display adapter"
	}
	vname, dname, sname := lookupPCI(vendor, device, sub)
	switch {
	case sname != "":
		return sname
	case dname != "" && vname != "":
		return shortVendor(vname) + " " + dname
	case dname != "":
		return dname
	}
	return fmt.Sprintf("GPU [%04x:%04x]", vendor, device)
}

// shortVendor trims pci.ids' long vendor names ("Advanced Micro Devices,
// Inc. [AMD/ATI]" → "AMD").
func shortVendor(v string) string {
	switch {
	case strings.Contains(v, "AMD"):
		return "AMD"
	case strings.HasPrefix(v, "Intel"):
		return "Intel"
	case strings.HasPrefix(v, "NVIDIA"):
		return "NVIDIA"
	}
	return v
}

// pciNames caches lookupPCI: pci.ids is in the read-only image, and the
// display manager probes again every few seconds while no GPU is supported.
var pciNames sync.Map // pciKey -> [3]string

type pciKey struct {
	path           string
	vendor, device uint16
	sub            [2]uint16
}

// lookupPCI scans pci.ids: "vvvv  Vendor", "\tdddd  Device",
// "\t\tssss ssss  Subsystem".
func lookupPCI(vendor, device uint16, sub [2]uint16) (vname, dname, sname string) {
	k := pciKey{PCIIDsPath, vendor, device, sub}
	if v, ok := pciNames.Load(k); ok {
		n := v.([3]string)
		return n[0], n[1], n[2]
	}
	vname, dname, sname, ok := scanPCIIDs(vendor, device, sub)
	if ok {
		pciNames.Store(k, [3]string{vname, dname, sname})
	}
	return vname, dname, sname
}

// scanPCIIDs does lookupPCI's work; ok is false when pci.ids is missing.
func scanPCIIDs(vendor, device uint16, sub [2]uint16) (vname, dname, sname string, ok bool) {
	f, err := os.Open(PCIIDsPath)
	if err != nil {
		return
	}
	defer f.Close()
	ok = true
	vkey := fmt.Sprintf("%04x", vendor)
	dkey := fmt.Sprintf("%04x", device)
	skey := fmt.Sprintf("%04x %04x", sub[0], sub[1])
	inVendor, inDevice := false, false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		switch {
		case !strings.HasPrefix(line, "\t"):
			if inVendor {
				return // left our vendor's section
			}
			if strings.HasPrefix(line, vkey+"  ") {
				inVendor, vname = true, strings.TrimSpace(line[6:])
			}
		case !inVendor:
		case strings.HasPrefix(line, "\t\t"):
			if inDevice && strings.HasPrefix(line[2:], skey+"  ") {
				sname = strings.TrimSpace(line[2+len(skey)+2:])
				return
			}
		default:
			if inDevice {
				return // next device: no subsystem match
			}
			if strings.HasPrefix(line[1:], dkey+"  ") {
				inDevice, dname = true, strings.TrimSpace(line[1+len(dkey)+2:])
			}
		}
	}
	return
}

// virtualCandidateTypes are the connector types a virtual display may use,
// best first. eDP/LVDS/DSI are internal panels and never qualify.
var virtualCandidateTypes = []string{"DP", "HDMI-A"}

// ChooseVirtualConnector picks the connector the virtual display uses on
// this machine: a disconnected DP (else HDMI) on a supported GPU; "" if none.
func ChooseVirtualConnector() (string, error) {
	g := Probe()
	if !g.Supported || g.cardName == "" {
		return "", nil
	}
	conns, err := drm.Connectors(g.cardName)
	if err != nil {
		return "", err
	}
	return chooseVirtual(conns, forcedConnector(config.KernelArgs())), nil
}

// chooseVirtual is the policy behind ChooseVirtualConnector. A connector the
// kernel already forces on (video=<C>:e) wins, because it reads "connected"
// only because we forced it; otherwise the first disconnected DP, then HDMI.
func chooseVirtual(conns []drm.SysConnector, forced string) string {
	if forced != "" {
		for _, c := range conns {
			if c.Name == forced && isCandidateType(c.Type) {
				return c.Name
			}
		}
	}
	for _, t := range virtualCandidateTypes {
		for _, c := range conns {
			if c.Type == t && c.Status == "disconnected" {
				return c.Name
			}
		}
	}
	return ""
}

func isCandidateType(t string) bool {
	for _, v := range virtualCandidateTypes {
		if v == t {
			return true
		}
	}
	return false
}

// forcedConnector returns the connector a `video=<C>:...e` kernel argument
// forces on, or "".
func forcedConnector(args []string) string {
	for _, a := range args {
		v, ok := strings.CutPrefix(a, "video=")
		if !ok {
			continue
		}
		name, opts, ok := strings.Cut(v, ":")
		if ok && name != "" && (strings.HasSuffix(opts, "e") || strings.HasSuffix(opts, "D")) {
			return name
		}
	}
	return ""
}

// MachineCmdlineFor returns the machine kernel args for a virtual connector:
// force it on and give it the VaporOS EDID, preferring the learned copy
// under /var/lib/vos/firmware over the image's /usr/lib/firmware one.
func MachineCmdlineFor(connector string) string {
	if connector == "" {
		return ""
	}
	return fmt.Sprintf("video=%s:e drm.edid_firmware=%s:edid/vaporos.bin firmware_class.path=%s",
		connector, connector, config.FirmwareDir())
}

// machineArgPrefixes are the kernel args MachineCmdlineFor owns; used to
// swap them out of existing boot entries.
var machineArgPrefixes = []string{"video=", "drm.edid_firmware=", "firmware_class.path="}

// replaceMachineArgs removes old machine args (and any arg we own) from an
// entry's options and appends the new machine cmdline.
func replaceMachineArgs(options, oldMachine, newMachine string) string {
	old := map[string]bool{}
	for _, a := range strings.Fields(oldMachine) {
		old[a] = true
	}
	var keep []string
	for _, a := range strings.Fields(options) {
		owned := old[a]
		for _, p := range machineArgPrefixes {
			owned = owned || strings.HasPrefix(a, p)
		}
		if !owned {
			keep = append(keep, a)
		}
	}
	keep = append(keep, strings.Fields(newMachine)...)
	return strings.Join(keep, " ")
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readHex(path string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimPrefix(readTrim(path), "0x"), 16, 32)
	return n
}
