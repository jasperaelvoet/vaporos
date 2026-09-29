package drm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"
)

// The uapi structs must match the kernel's layout exactly (64-bit ABI).
func TestStructSizes(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("layout assertions are for 64-bit targets")
	}
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"drm_mode_modeinfo", unsafe.Sizeof(ModeInfo{}), 68},
		{"drm_mode_card_res", unsafe.Sizeof(modeCardRes{}), 64},
		{"drm_mode_crtc", unsafe.Sizeof(modeCRTC{}), 104},
		{"drm_mode_get_encoder", unsafe.Sizeof(modeGetEncoder{}), 20},
		{"drm_mode_get_connector", unsafe.Sizeof(modeGetConnector{}), 80},
		{"drm_mode_get_plane_res", unsafe.Sizeof(modeGetPlaneRes{}), 16},
		{"drm_mode_get_plane", unsafe.Sizeof(modeGetPlane{}), 32},
		{"drm_mode_fb_cmd", unsafe.Sizeof(modeFBCmd{}), 28},
		{"drm_mode_fb_cmd2", unsafe.Sizeof(modeFBCmd2{}), 104},
		{"drm_mode_create_dumb", unsafe.Sizeof(modeCreateDumb{}), 32},
		{"drm_mode_map_dumb", unsafe.Sizeof(modeMapDumb{}), 16},
		{"drm_mode_destroy_dumb", unsafe.Sizeof(modeDestroyDumb{}), 4},
		{"drm_set_client_cap", unsafe.Sizeof(setClientCap{}), 16},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
	// Spot-check field offsets the kernel reads.
	if off := unsafe.Offsetof(modeCRTC{}.Mode); off != 36 {
		t.Errorf("drm_mode_crtc.mode at %d, want 36", off)
	}
	if off := unsafe.Offsetof(modeFBCmd2{}.Modifier); off != 72 {
		t.Errorf("drm_mode_fb_cmd2.modifier at %d, want 72", off)
	}
	if off := unsafe.Offsetof(ModeInfo{}.Name); off != 36 {
		t.Errorf("drm_mode_modeinfo.name at %d, want 36", off)
	}
	if off := unsafe.Offsetof(modeGetConnector{}.ConnectorID); off != 48 {
		t.Errorf("drm_mode_get_connector.connector_id at %d, want 48", off)
	}
	// Further offsets cross-checked with gcc against linux-libc-dev's headers.
	for name, c := range map[string]struct{ got, want uintptr }{
		"get_connector.connection":  {unsafe.Offsetof(modeGetConnector{}.Connection), 60},
		"modeinfo.vrefresh":         {unsafe.Offsetof(ModeInfo{}.VRefresh), 24},
		"create_dumb.size":          {unsafe.Offsetof(modeCreateDumb{}.Size), 24},
		"get_plane.format_type_ptr": {unsafe.Offsetof(modeGetPlane{}.FormatTypePtr), 24},
	} {
		if c.got != c.want {
			t.Errorf("%s at %d, want %d", name, c.got, c.want)
		}
	}
}

// The request numbers libdrm uses on x86-64 (from xf86drm.h expansions).
func TestIoctlNumbers(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("numbers are for 64-bit targets")
	}
	for name, c := range map[string]struct{ got, want uintptr }{
		"SET_CLIENT_CAP":         {ioctlSetClientCap, 0x4010640d},
		"SET_MASTER":             {ioctlSetMaster, 0x641e},
		"DROP_MASTER":            {ioctlDropMaster, 0x641f},
		"MODE_GETRESOURCES":      {ioctlModeGetResources, 0xc04064a0},
		"MODE_GETCRTC":           {ioctlModeGetCRTC, 0xc06864a1},
		"MODE_SETCRTC":           {ioctlModeSetCRTC, 0xc06864a2},
		"MODE_GETENCODER":        {ioctlModeGetEncoder, 0xc01464a6},
		"MODE_GETCONNECTOR":      {ioctlModeGetConnector, 0xc05064a7},
		"MODE_ADDFB":             {ioctlModeAddFB, 0xc01c64ae},
		"MODE_RMFB":              {ioctlModeRmFB, 0xc00464af},
		"MODE_CREATE_DUMB":       {ioctlModeCreateDumb, 0xc02064b2},
		"MODE_MAP_DUMB":          {ioctlModeMapDumb, 0xc01064b3},
		"MODE_DESTROY_DUMB":      {ioctlModeDestroyDumb, 0xc00464b4},
		"MODE_GETPLANERESOURCES": {ioctlModeGetPlaneRes, 0xc01064b5},
		"MODE_GETPLANE":          {ioctlModeGetPlane, 0xc02064b6},
		"MODE_ADDFB2":            {ioctlModeAddFB2, 0xc06864b8},
		"MODE_DIRTYFB":           {ioctlModeDirtyFB, 0xc01864b1},
	} {
		if c.got != c.want {
			t.Errorf("DRM_IOCTL_%s = %#x, want %#x", name, c.got, c.want)
		}
	}
	if FormatXRGB8888 != 0x34325258 {
		t.Errorf("XRGB8888 fourcc = %#x", FormatXRGB8888)
	}
}

func TestModeRefresh(t *testing.T) {
	// CVT-RB2 3840x2160@60 as the kernel sees it from a 10 kHz DTD clock.
	m := ModeInfo{Clock: 522610, HDisplay: 3840, HTotal: 3920, VDisplay: 2160, VTotal: 2222}
	if r := m.Refresh(); r != 60 {
		t.Errorf("refresh = %d", r)
	}
	if s := m.String(); s != "3840x2160@60" {
		t.Errorf("String = %q", s)
	}
	m = ModeInfo{VRefresh: 75}
	if r := m.Refresh(); r != 75 {
		t.Errorf("fallback refresh = %d", r)
	}
	copy(m.Name[:], "1920x1080")
	if m.NameString() != "1920x1080" {
		t.Errorf("name = %q", m.NameString())
	}
}

func mode(w, h, r int, preferred bool) ModeInfo {
	m := ModeInfo{HDisplay: uint16(w), VDisplay: uint16(h), HTotal: uint16(w + 80), VTotal: 1000}
	m.Clock = uint32(r * int(m.HTotal) * int(m.VTotal) / 1000)
	if preferred {
		m.Type = ModeTypePreferred
	}
	return m
}

func TestConnectorModes(t *testing.T) {
	c := &Connector{Modes: []ModeInfo{
		mode(2560, 1440, 144, false),
		mode(1920, 1080, 60, false),
		mode(1920, 1080, 120, true),
		mode(1920, 1080, 144, false),
	}}
	if m, _ := c.PreferredMode(); m.String() != "1920x1080@120" {
		t.Errorf("preferred = %s", m.String())
	}
	for _, tc := range []struct {
		w, h, r int
		want    string
	}{
		{1920, 1080, 60, "1920x1080@60"},
		{1920, 1080, 100, "1920x1080@60"},
		{1920, 1080, 30, "1920x1080@60"},
		{1920, 1080, 0, "1920x1080@144"},
		{2560, 1440, 60, "2560x1440@144"},
	} {
		m, ok := c.FindMode(tc.w, tc.h, tc.r)
		if !ok || m.String() != tc.want {
			t.Errorf("FindMode(%d,%d,%d) = %s %v, want %s", tc.w, tc.h, tc.r, m.String(), ok, tc.want)
		}
	}
	if _, ok := c.FindMode(3840, 2160, 60); ok {
		t.Error("found a mode the connector does not have")
	}
	if n := ConnectorName(11, 2); n != "HDMI-A-2" {
		t.Errorf("ConnectorName = %q", n)
	}
	if n := ConnectorName(99, 1); n != "Unknown-1" {
		t.Errorf("ConnectorName = %q", n)
	}
}

func TestParseUevent(t *testing.T) {
	msg := []byte("change@/devices/pci0000:00/0000:00:01.0/drm/card0\x00ACTION=change\x00DEVPATH=/devices/pci0000:00/0000:00:01.0/drm/card0\x00SUBSYSTEM=drm\x00HOTPLUG=1\x00SEQNUM=4242\x00")
	u, ok := ParseUevent(msg)
	if !ok || !u.IsDRM() || u.Action != "change" || u.Env["HOTPLUG"] != "1" {
		t.Fatalf("parsed %+v %v", u, ok)
	}
	if _, ok := ParseUevent([]byte("libudev\x00\xfe\xed")); ok {
		t.Error("accepted a udevd message")
	}
	u, ok = ParseUevent([]byte("add@/devices/virtual/net/veth0\x00SUBSYSTEM=net\x00"))
	if !ok || u.IsDRM() {
		t.Errorf("net event: %+v %v", u, ok)
	}
}

// writeTree creates files under root from a path → content map.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSysfs(t *testing.T) {
	root := t.TempDir()
	SysClassDRM = root
	t.Cleanup(func() { SysClassDRM = "/sys/class/drm" })
	writeTree(t, root, map[string]string{
		"card1/device/vendor":          "0x1002\n",
		"card1/device/device":          "0x7550\n",
		"card1/device/boot_vga":        "1\n",
		"card1/device/uevent":          "DRIVER=amdgpu\nPCI_ID=1002:7550\nPCI_SLOT_NAME=0000:0b:00.0\n",
		"card1-DP-10/status":           "disconnected\n",
		"card1-DP-2/status":            "connected\n",
		"card1-DP-2/modes":             "3840x2160\n1920x1080\n",
		"card1-DP-2/enabled":           "enabled\n",
		"card1-HDMI-A-1/status":        "disconnected\n",
		"card0/device/uevent":          "DRIVER=simpledrm\n",
		"card0-Unknown-1/status":       "connected\n",
		"renderD128/dev":               "226:128\n",
		"card1-Writeback-1/status":     "unknown\n",
		"card1-DP-2/edid":              "\x00\xff\xff\xff\xff\xff\xff\x00",
		"version":                      "drm 1.1.0\n",
		"card1-HDMI-A-1/modes":         "",
		"card1-DP-10/modes":            "",
		"card1-Writeback-1/modes":      "",
		"card0-Unknown-1/modes":        "1024x768\n",
		"card1/device/driver-override": "",
	})
	if err := os.Symlink("../../bus/pci/drivers/amdgpu", filepath.Join(root, "card1/device/driver")); err != nil {
		t.Fatal(err)
	}

	cards, err := Cards()
	if err != nil || len(cards) != 2 {
		t.Fatalf("Cards = %+v, %v", cards, err)
	}
	amd := cards[1]
	if amd.Name != "card1" || amd.VendorID != 0x1002 || amd.DeviceID != 0x7550 || amd.Driver != "amdgpu" ||
		!amd.BootVGA || amd.PCISlot != "0000:0b:00.0" || amd.Dev != "/dev/dri/card1" {
		t.Errorf("card1 = %+v", amd)
	}
	if cards[0].Driver != "simpledrm" || cards[0].VendorID != 0 {
		t.Errorf("card0 = %+v", cards[0])
	}

	conns, err := Connectors("card1")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range conns {
		names = append(names, c.Name)
	}
	if want := []string{"DP-2", "DP-10", "HDMI-A-1", "Writeback-1"}; !reflect.DeepEqual(names, want) {
		t.Errorf("connectors = %v, want %v", names, want)
	}
	dp := conns[0]
	if !dp.Connected() || !dp.Enabled || dp.Type != "DP" || len(dp.Modes) != 2 || len(dp.EDID()) != 8 {
		t.Errorf("DP-2 = %+v", dp)
	}
	if conns[2].Type != "HDMI-A" || conns[2].EDID() != nil {
		t.Errorf("HDMI = %+v", conns[2])
	}
	all, _ := Connectors("")
	if len(all) != 5 || all[0].Card != "card0" {
		t.Errorf("all connectors = %d, first %s", len(all), all[0].Card)
	}
	for in, want := range map[string]string{"HDMI-A-1": "HDMI-A", "eDP-1": "eDP", "DP": "DP", "Virtual-12": "Virtual"} {
		if got := ConnectorType(in); got != want {
			t.Errorf("ConnectorType(%q) = %q", in, got)
		}
	}
}
