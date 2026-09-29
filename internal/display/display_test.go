package display

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
)

// fakeSysfs builds /sys/class/drm and /sys/bus/pci/devices trees.
func fakeSysfs(t *testing.T, cards map[string]map[string]string) {
	t.Helper()
	root := t.TempDir()
	saveDRM, savePCI, saveIDs := drm.SysClassDRM, SysBusPCI, PCIIDsPath
	t.Cleanup(func() { drm.SysClassDRM, SysBusPCI, PCIIDsPath = saveDRM, savePCI, saveIDs })
	drm.SysClassDRM = filepath.Join(root, "class/drm")
	SysBusPCI = filepath.Join(root, "bus/pci/devices")
	PCIIDsPath = filepath.Join(root, "pci.ids")
	mustWrite(t, PCIIDsPath, strings.Join([]string{
		"# pci.ids excerpt",
		"1002  Advanced Micro Devices, Inc. [AMD/ATI]",
		"\t7480  Navi 33 [Radeon RX 7600/7600 XT/7600M XT/7600S/7700S / PRO W7600]",
		"\t7550  Navi 48 [Radeon RX 9070/9070 XT/9070 GRE]",
		"\t\t148c 2435  Radeon RX 9070 XT 16GB",
		"\t\t1458 2437  Navi 48 XTX [Radeon RX 9070 XT Gaming OC ICE 16G]",
		"10de  NVIDIA Corporation",
		"\t2704  AD103 [GeForce RTX 4080]",
		"1af4  Red Hat, Inc.",
		"\t1050  Virtio 1.0 GPU",
		"",
	}, "\n"))
	for path, files := range cards {
		for name, content := range files {
			if strings.HasPrefix(name, "->") {
				p := filepath.Join(root, path, strings.TrimPrefix(name, "->"))
				os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.Symlink(content, p); err != nil {
					t.Fatal(err)
				}
				continue
			}
			mustWrite(t, filepath.Join(root, path, name), content)
		}
	}
}

func TestProbeAMD(t *testing.T) {
	fakeSysfs(t, map[string]map[string]string{
		"class/drm/card0": {"device/uevent": "DRIVER=simpledrm\n"},
		"class/drm/card1": {
			"device/vendor": "0x1002", "device/device": "0x7550", "device/boot_vga": "1",
			"device/uevent": "DRIVER=amdgpu\nPCI_SLOT_NAME=0000:0b:00.0\n",
		},
		"class/drm/card1-DP-1":     {"status": "disconnected"},
		"class/drm/card1-HDMI-A-1": {"status": "connected"},
		"bus/pci/devices/0000:0b:00.0": {
			"class": "0x030000", "vendor": "0x1002", "device": "0x7550",
			"subsystem_vendor": "0x148c", "subsystem_device": "0x2435",
		},
	})
	g := Probe()
	if g.Vendor != "amd" || !g.Supported || g.Driver != "amdgpu" || g.Card != "/dev/dri/card1" || g.CardName() != "card1" {
		t.Fatalf("Probe = %+v", g)
	}
	if g.Name != "Radeon RX 9070 XT 16GB" {
		t.Errorf("name = %q", g.Name)
	}
	p := g.Profile()
	if p == nil || p.Name() != "amd" || p.Encoder() != "vulkan" || p.Capture() != "kms" || !p.VirtualHDR() {
		t.Errorf("profile = %#v", p)
	}
}

func TestProbeUnsupported(t *testing.T) {
	// NVIDIA card without a DRM driver, plus the firmware framebuffer.
	fakeSysfs(t, map[string]map[string]string{
		"class/drm/card0":                   {"device/uevent": "DRIVER=simpledrm\n"},
		"class/drm/card0-Unknown-1":         {"status": "connected"},
		"bus/pci/devices/0000:01:00.0":      {"class": "0x030000", "vendor": "0x10de", "device": "0x2704"},
		"bus/pci/devices/0000:00:1f.3":      {"class": "0x040300", "vendor": "0x8086", "device": "0x7a50"},
		"bus/pci/devices/0000:01:00.0/null": {},
	})
	g := Probe()
	if g.Vendor != "nvidia" || g.Supported || g.Name != "NVIDIA AD103 [GeForce RTX 4080]" || g.Card != "" {
		t.Fatalf("Probe = %+v", g)
	}
	if p := g.Profile(); p == nil || p.Supported() || p.Encoder() != "nvenc" || p.VirtualHDR() {
		t.Errorf("nvidia profile = %#v", p)
	}
}

func TestProbeVirtual(t *testing.T) {
	fakeSysfs(t, map[string]map[string]string{
		"class/drm/card0": {"device/vendor": "0x1af4", "device/device": "0x0010", "->device/driver": "../../bus/virtio/drivers/virtio_gpu"},
	})
	g := Probe()
	if g.Vendor != "virtual" || g.Supported || g.Name != "Virtio GPU" || g.Driver != "virtio_gpu" || g.Profile() != nil {
		t.Fatalf("Probe = %+v", g)
	}
	fakeSysfs(t, nil)
	if g := Probe(); g != (GPUInfo{}) {
		t.Errorf("empty sysfs = %+v", g)
	}
}

func TestOldRadeonUnsupported(t *testing.T) {
	g := GPUInfo{Vendor: "amd", Driver: "radeon"}
	if p := g.Profile(); p == nil || p.Supported() || p.Name() != "amd" {
		t.Errorf("radeon profile = %#v", p)
	}
	if p := (GPUInfo{Vendor: "intel", Driver: "i915"}).Profile(); p == nil || p.Supported() || p.Encoder() != "vaapi" {
		t.Errorf("intel profile = %#v", p)
	}
}

func TestChooseVirtual(t *testing.T) {
	conn := func(name, status string) drm.SysConnector {
		return drm.SysConnector{Name: name, Type: drm.ConnectorType(name), Status: status}
	}
	for _, c := range []struct {
		conns  []drm.SysConnector
		forced string
		want   string
	}{
		{[]drm.SysConnector{conn("DP-1", "disconnected"), conn("HDMI-A-1", "connected")}, "", "DP-1"},
		{[]drm.SysConnector{conn("DP-1", "connected"), conn("DP-2", "disconnected")}, "", "DP-2"},
		{[]drm.SysConnector{conn("DP-1", "connected"), conn("HDMI-A-1", "disconnected")}, "", "HDMI-A-1"},
		{[]drm.SysConnector{conn("eDP-1", "disconnected"), conn("DP-1", "connected")}, "", ""},
		{[]drm.SysConnector{conn("DP-1", "connected"), conn("DP-2", "disconnected")}, "DP-1", "DP-1"},
		{[]drm.SysConnector{conn("eDP-1", "connected")}, "eDP-1", ""},
		{nil, "", ""},
	} {
		if got := chooseVirtual(c.conns, c.forced); got != c.want {
			t.Errorf("chooseVirtual(%v, %q) = %q, want %q", c.conns, c.forced, got, c.want)
		}
	}
}

func TestForcedConnector(t *testing.T) {
	for args, want := range map[string]string{
		"quiet video=DP-1:e drm.edid_firmware=DP-1:edid/x.bin": "DP-1",
		"video=HDMI-A-1:1920x1080@60e":                         "HDMI-A-1",
		"video=DP-2:D":                                         "DP-2",
		"video=efifb:off quiet":                                "",
		"video=DP-1:d":                                         "",
		"":                                                     "",
	} {
		if got := forcedConnector(strings.Fields(args)); got != want {
			t.Errorf("forcedConnector(%q) = %q, want %q", args, got, want)
		}
	}
}

func TestMachineCmdline(t *testing.T) {
	if got := MachineCmdlineFor("DP-1"); got != "video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin firmware_class.path="+config.FirmwareDir() {
		t.Errorf("MachineCmdlineFor = %q", got)
	}
	if MachineCmdlineFor("") != "" {
		t.Error("empty connector should give an empty cmdline")
	}
}
