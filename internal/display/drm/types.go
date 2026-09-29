// Package drm is a small pure-Go client for the Linux DRM/KMS uapi: just
// enough of it to light connectors with dumb buffers (`vos welcome`) and to
// read which mode a connector is scanning out (vosd waiting for gamescope).
// It talks to the kernel with raw ioctls from golang.org/x/sys/unix, so the
// binary stays static and needs no libdrm.
//
// The struct layouts below mirror include/uapi/drm/drm.h and drm_mode.h
// byte for byte; types_test.go pins their sizes and the resulting ioctl
// numbers against libdrm's well-known values.
package drm

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// ErrUnsupported is returned on platforms without DRM (the dev Mac).
var ErrUnsupported = errors.New("drm: not supported on this platform")

// Connection states (enum drm_connector_status).
const (
	Connected         = 1
	Disconnected      = 2
	UnknownConnection = 3
)

// Mode type and flag bits we care about (drm_mode.h).
const (
	ModeTypePreferred = 1 << 3
	ModeTypeDriver    = 1 << 6

	ModeFlagPHSync = 1 << 0
	ModeFlagNHSync = 1 << 1
	ModeFlagPVSync = 1 << 2
	ModeFlagNVSync = 1 << 3
)

// FormatXRGB8888 is DRM_FORMAT_XRGB8888 (fourcc 'XR24'): little-endian
// B, G, R, X bytes per pixel. Every KMS driver supports it for dumb buffers.
const FormatXRGB8888 = 'X' | 'R'<<8 | '2'<<16 | '4'<<24

// Client capabilities (DRM_CLIENT_CAP_*).
const CapUniversalPlanes = 2

// ModeInfo is struct drm_mode_modeinfo (68 bytes).
type ModeInfo struct {
	Clock      uint32 // kHz
	HDisplay   uint16
	HSyncStart uint16
	HSyncEnd   uint16
	HTotal     uint16
	HSkew      uint16
	VDisplay   uint16
	VSyncStart uint16
	VSyncEnd   uint16
	VTotal     uint16
	VScan      uint16
	VRefresh   uint32
	Flags      uint32
	Type       uint32
	Name       [32]byte
}

// Refresh returns the refresh rate in Hz, rounded the way the kernel's
// drm_mode_vrefresh() does, so it matches what gamescope compares against.
func (m *ModeInfo) Refresh() int {
	if m.HTotal == 0 || m.VTotal == 0 {
		return int(m.VRefresh)
	}
	den := uint64(m.HTotal) * uint64(m.VTotal)
	num := uint64(m.Clock) * 1000
	if m.Flags&(1<<4) != 0 { // DRM_MODE_FLAG_INTERLACE
		num *= 2
	}
	if m.Flags&(1<<5) != 0 { // DRM_MODE_FLAG_DBLSCAN
		den *= 2
	}
	if m.VScan > 1 {
		den *= uint64(m.VScan)
	}
	return int((num + den/2) / den)
}

// Preferred reports whether the driver marked this mode preferred.
func (m *ModeInfo) Preferred() bool { return m.Type&ModeTypePreferred != 0 }

// String formats the mode as "WxH@R".
func (m *ModeInfo) String() string {
	return fmt.Sprintf("%dx%d@%d", m.HDisplay, m.VDisplay, m.Refresh())
}

// NameString returns the kernel's mode name ("1920x1080").
func (m *ModeInfo) NameString() string {
	n := m.Name[:]
	if i := strings.IndexByte(string(n), 0); i >= 0 {
		n = n[:i]
	}
	return string(n)
}

// modeCardRes is struct drm_mode_card_res (64 bytes).
type modeCardRes struct {
	FBIDPtr        uint64
	CRTCIDPtr      uint64
	ConnectorIDPtr uint64
	EncoderIDPtr   uint64
	CountFBs       uint32
	CountCRTCs     uint32
	CountConns     uint32
	CountEncoders  uint32
	MinWidth       uint32
	MaxWidth       uint32
	MinHeight      uint32
	MaxHeight      uint32
}

// modeCRTC is struct drm_mode_crtc (104 bytes).
type modeCRTC struct {
	SetConnectorsPtr uint64
	CountConnectors  uint32
	CRTCID           uint32
	FBID             uint32
	X                uint32
	Y                uint32
	GammaSize        uint32
	ModeValid        uint32
	Mode             ModeInfo
}

// modeGetEncoder is struct drm_mode_get_encoder (20 bytes).
type modeGetEncoder struct {
	EncoderID      uint32
	EncoderType    uint32
	CRTCID         uint32
	PossibleCRTCs  uint32
	PossibleClones uint32
}

// modeGetConnector is struct drm_mode_get_connector (80 bytes).
type modeGetConnector struct {
	EncodersPtr     uint64
	ModesPtr        uint64
	PropsPtr        uint64
	PropValuesPtr   uint64
	CountModes      uint32
	CountProps      uint32
	CountEncoders   uint32
	EncoderID       uint32
	ConnectorID     uint32
	ConnectorType   uint32
	ConnectorTypeID uint32
	Connection      uint32
	MMWidth         uint32
	MMHeight        uint32
	Subpixel        uint32
	Pad             uint32
}

// modeGetPlaneRes is struct drm_mode_get_plane_res (16 bytes with tail padding).
type modeGetPlaneRes struct {
	PlaneIDPtr  uint64
	CountPlanes uint32
}

// modeGetPlane is struct drm_mode_get_plane (32 bytes).
type modeGetPlane struct {
	PlaneID          uint32
	CRTCID           uint32
	FBID             uint32
	PossibleCRTCs    uint32
	GammaSize        uint32
	CountFormatTypes uint32
	FormatTypePtr    uint64
}

// modeFBCmd is struct drm_mode_fb_cmd (28 bytes), the legacy ADDFB argument.
type modeFBCmd struct {
	FBID   uint32
	Width  uint32
	Height uint32
	Pitch  uint32
	BPP    uint32
	Depth  uint32
	Handle uint32
}

// modeFBDirtyCmd is struct drm_mode_fb_dirty_cmd (24 bytes). With no clips
// it marks the whole framebuffer dirty.
type modeFBDirtyCmd struct {
	FBID     uint32
	Flags    uint32
	Color    uint32
	NumClips uint32
	ClipsPtr uint64
}

// modeFBCmd2 is struct drm_mode_fb_cmd2 (104 bytes).
type modeFBCmd2 struct {
	FBID        uint32
	Width       uint32
	Height      uint32
	PixelFormat uint32
	Flags       uint32
	Handles     [4]uint32
	Pitches     [4]uint32
	Offsets     [4]uint32
	Modifier    [4]uint64
}

// modeCreateDumb is struct drm_mode_create_dumb (32 bytes).
type modeCreateDumb struct {
	Height uint32
	Width  uint32
	BPP    uint32
	Flags  uint32
	Handle uint32
	Pitch  uint32
	Size   uint64
}

// modeMapDumb is struct drm_mode_map_dumb (16 bytes).
type modeMapDumb struct {
	Handle uint32
	Pad    uint32
	Offset uint64
}

// modeDestroyDumb is struct drm_mode_destroy_dumb (4 bytes).
type modeDestroyDumb struct {
	Handle uint32
}

// setClientCap is struct drm_set_client_cap (16 bytes).
type setClientCap struct {
	Capability uint64
	Value      uint64
}

// ioctl number encoding (asm-generic/ioctl.h, used by x86 and arm64).
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2
	iocBase  = 'd' // DRM_IOCTL_BASE
)

func ioc(dir, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | iocBase<<8 | nr
}

func ioNone(nr uintptr) uintptr     { return ioc(iocNone, nr, 0) }
func iow(nr, size uintptr) uintptr  { return ioc(iocWrite, nr, size) }
func iowr(nr, size uintptr) uintptr { return ioc(iocRead|iocWrite, nr, size) }

// The ioctls, sized from the Go structs so the numbers can never drift
// from the layouts actually passed to the kernel.
var (
	ioctlSetClientCap     = iow(0x0d, unsafe.Sizeof(setClientCap{}))
	ioctlSetMaster        = ioNone(0x1e)
	ioctlDropMaster       = ioNone(0x1f)
	ioctlModeGetResources = iowr(0xA0, unsafe.Sizeof(modeCardRes{}))
	ioctlModeGetCRTC      = iowr(0xA1, unsafe.Sizeof(modeCRTC{}))
	ioctlModeSetCRTC      = iowr(0xA2, unsafe.Sizeof(modeCRTC{}))
	ioctlModeGetEncoder   = iowr(0xA6, unsafe.Sizeof(modeGetEncoder{}))
	ioctlModeGetConnector = iowr(0xA7, unsafe.Sizeof(modeGetConnector{}))
	ioctlModeAddFB        = iowr(0xAE, unsafe.Sizeof(modeFBCmd{}))
	ioctlModeRmFB         = iowr(0xAF, unsafe.Sizeof(uint32(0)))
	ioctlModeCreateDumb   = iowr(0xB2, unsafe.Sizeof(modeCreateDumb{}))
	ioctlModeMapDumb      = iowr(0xB3, unsafe.Sizeof(modeMapDumb{}))
	ioctlModeDestroyDumb  = iowr(0xB4, unsafe.Sizeof(modeDestroyDumb{}))
	ioctlModeGetPlaneRes  = iowr(0xB5, unsafe.Sizeof(modeGetPlaneRes{}))
	ioctlModeGetPlane     = iowr(0xB6, unsafe.Sizeof(modeGetPlane{}))
	ioctlModeAddFB2       = iowr(0xB8, unsafe.Sizeof(modeFBCmd2{}))
	ioctlModeDirtyFB      = iowr(0xB1, unsafe.Sizeof(modeFBDirtyCmd{}))
)

// connectorTypeNames follows drm_connector_enum_list in the kernel, which is
// also what names the /sys/class/drm/cardN-<name> directories.
var connectorTypeNames = []string{
	0: "Unknown", 1: "VGA", 2: "DVI-I", 3: "DVI-D", 4: "DVI-A", 5: "Composite",
	6: "SVIDEO", 7: "LVDS", 8: "Component", 9: "DIN", 10: "DP", 11: "HDMI-A",
	12: "HDMI-B", 13: "TV", 14: "eDP", 15: "Virtual", 16: "DSI", 17: "DPI",
	18: "Writeback", 19: "SPI", 20: "USB",
}

// ConnectorName returns the kernel's name for a connector ("DP-1").
func ConnectorName(connType, typeID uint32) string {
	t := "Unknown"
	if int(connType) < len(connectorTypeNames) {
		t = connectorTypeNames[connType]
	}
	return fmt.Sprintf("%s-%d", t, typeID)
}

// Resources is the result of MODE_GETRESOURCES.
type Resources struct {
	FBs, CRTCs, Connectors, Encoders []uint32
	MinWidth, MaxWidth               uint32
	MinHeight, MaxHeight             uint32
}

// Connector is the result of MODE_GETCONNECTOR.
type Connector struct {
	ID         uint32
	Type       uint32
	TypeID     uint32
	Name       string // "DP-1"
	Connection uint32
	EncoderID  uint32 // currently bound encoder, 0 if none
	Encoders   []uint32
	Modes      []ModeInfo
	MMWidth    uint32
	MMHeight   uint32
}

// Connected reports whether a sink is attached (or the connector is forced on).
func (c *Connector) Connected() bool { return c.Connection == Connected }

// PreferredMode returns the driver's preferred mode, else the first one.
func (c *Connector) PreferredMode() (ModeInfo, bool) {
	for _, m := range c.Modes {
		if m.Preferred() {
			return m, true
		}
	}
	if len(c.Modes) > 0 {
		return c.Modes[0], true
	}
	return ModeInfo{}, false
}

// FindMode returns a mode of the given size. With refresh != 0 it prefers
// that exact rate, then the highest rate below it, then the lowest above;
// with refresh == 0 it takes the highest rate.
func (c *Connector) FindMode(w, h, refresh int) (ModeInfo, bool) {
	var below, above *ModeInfo
	for i := range c.Modes {
		m := &c.Modes[i]
		if int(m.HDisplay) != w || int(m.VDisplay) != h {
			continue
		}
		r := m.Refresh()
		switch {
		case refresh != 0 && r == refresh:
			return *m, true
		case refresh == 0 || r < refresh:
			if below == nil || r > below.Refresh() {
				below = m
			}
		default:
			if above == nil || r < above.Refresh() {
				above = m
			}
		}
	}
	if below != nil {
		return *below, true
	}
	if above != nil {
		return *above, true
	}
	return ModeInfo{}, false
}

// Encoder is the result of MODE_GETENCODER.
type Encoder struct {
	ID            uint32
	Type          uint32
	CRTCID        uint32
	PossibleCRTCs uint32
}

// CRTC is the result of MODE_GETCRTC.
type CRTC struct {
	ID        uint32
	FBID      uint32 // framebuffer on the primary plane, 0 if none
	X, Y      uint32
	ModeValid bool
	Mode      ModeInfo
}

// Plane is the result of MODE_GETPLANE.
type Plane struct {
	ID            uint32
	CRTCID        uint32
	FBID          uint32
	PossibleCRTCs uint32
}
