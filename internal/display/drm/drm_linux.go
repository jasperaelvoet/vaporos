//go:build linux

package drm

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Card is an open DRM primary node (/dev/dri/cardN).
type Card struct {
	Path string
	f    *os.File
}

// Open opens a DRM primary node read-write.
//
// The kernel makes the first opener of a card without a master its master.
// Pass keepMaster=false for read-only observers (vosd): the fd drops master
// right away so it can never block gamescope or the welcome screen.
func Open(path string, keepMaster bool) (*Card, error) {
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	c := &Card{Path: path, f: f}
	if !keepMaster {
		c.DropMaster() // EINVAL when we never were master; that is fine
	}
	return c, nil
}

// Close releases the fd (and with it master, if held).
func (c *Card) Close() error { return c.f.Close() }

// Fd returns the raw descriptor (for mmap).
func (c *Card) Fd() int { return int(c.f.Fd()) }

// ioctl issues one DRM ioctl, retrying on EINTR/EAGAIN like libdrm's drmIoctl.
func (c *Card) ioctl(req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, c.f.Fd(), req, uintptr(arg))
		if errno == 0 {
			return nil
		}
		if errno == unix.EINTR || errno == unix.EAGAIN {
			continue
		}
		return errno
	}
}

// SetMaster makes this fd the DRM master (needs root or a prior master).
func (c *Card) SetMaster() error {
	if err := c.ioctl(ioctlSetMaster, nil); err != nil {
		return fmt.Errorf("drm: set master on %s: %w", c.Path, err)
	}
	return nil
}

// DropMaster gives up DRM master.
func (c *Card) DropMaster() error { return c.ioctl(ioctlDropMaster, nil) }

// SetClientCap enables a DRM client capability.
func (c *Card) SetClientCap(capability, value uint64) error {
	arg := setClientCap{Capability: capability, Value: value}
	return c.ioctl(ioctlSetClientCap, unsafe.Pointer(&arg))
}

// alloc returns a heap-allocated slice for the kernel to fill. The uapi
// structs carry user pointers as plain u64s, which the garbage collector
// cannot see, so those buffers must never live on a goroutine stack (which
// may move): returning the slice from a function that is never inlined
// forces it onto the non-moving heap. Callers keep it alive past the ioctl.
//
//go:noinline
func alloc[T any](n int) []T { return make([]T, n) }

// ptr returns the address of a slice's backing array for a u64 pointer field.
func ptr[T any](s []T) uint64 {
	if len(s) == 0 {
		return 0
	}
	return uint64(uintptr(unsafe.Pointer(&s[0])))
}

// Resources returns the card's CRTCs, connectors, encoders and fbs.
func (c *Card) Resources() (*Resources, error) {
	// The counts can change between the sizing call and the fill call
	// (hotplug of MST connectors), so loop until they are stable.
	for range 10 {
		var res modeCardRes
		if err := c.ioctl(ioctlModeGetResources, unsafe.Pointer(&res)); err != nil {
			return nil, fmt.Errorf("drm: get resources: %w", err)
		}
		fbs := alloc[uint32](int(res.CountFBs))
		crtcs := alloc[uint32](int(res.CountCRTCs))
		conns := alloc[uint32](int(res.CountConns))
		encs := alloc[uint32](int(res.CountEncoders))
		want := res
		res.FBIDPtr, res.CRTCIDPtr = ptr(fbs), ptr(crtcs)
		res.ConnectorIDPtr, res.EncoderIDPtr = ptr(conns), ptr(encs)
		err := c.ioctl(ioctlModeGetResources, unsafe.Pointer(&res))
		runtime.KeepAlive(fbs)
		runtime.KeepAlive(crtcs)
		runtime.KeepAlive(conns)
		runtime.KeepAlive(encs)
		if err != nil {
			return nil, fmt.Errorf("drm: get resources: %w", err)
		}
		if res.CountFBs > want.CountFBs || res.CountCRTCs > want.CountCRTCs ||
			res.CountConns > want.CountConns || res.CountEncoders > want.CountEncoders {
			continue
		}
		return &Resources{
			FBs: fbs[:res.CountFBs], CRTCs: crtcs[:res.CountCRTCs],
			Connectors: conns[:res.CountConns], Encoders: encs[:res.CountEncoders],
			MinWidth: res.MinWidth, MaxWidth: res.MaxWidth,
			MinHeight: res.MinHeight, MaxHeight: res.MaxHeight,
		}, nil
	}
	return nil, errors.New("drm: get resources: counts kept changing")
}

// Connector reads one connector. With probe=true (and DRM master) the
// kernel re-detects the sink and re-reads its EDID; with probe=false it
// returns the current state without touching the hardware, which is what
// libdrm's drmModeGetConnectorCurrent does.
func (c *Card) Connector(id uint32, probe bool) (*Connector, error) {
	for range 10 {
		var gc modeGetConnector
		gc.ConnectorID = id
		// A non-zero mode count suppresses the forced probe.
		var stub []ModeInfo
		if !probe {
			stub = alloc[ModeInfo](1)
			gc.CountModes, gc.ModesPtr = 1, ptr(stub)
		}
		if err := c.ioctl(ioctlModeGetConnector, unsafe.Pointer(&gc)); err != nil {
			return nil, fmt.Errorf("drm: get connector %d: %w", id, err)
		}
		runtime.KeepAlive(stub)

		nModes := max(gc.CountModes, 1)
		modes := alloc[ModeInfo](int(nModes))
		encs := alloc[uint32](int(gc.CountEncoders))
		want := gc
		full := modeGetConnector{
			ConnectorID:   id,
			ModesPtr:      ptr(modes),
			CountModes:    nModes,
			EncodersPtr:   ptr(encs),
			CountEncoders: gc.CountEncoders,
		}
		err := c.ioctl(ioctlModeGetConnector, unsafe.Pointer(&full))
		runtime.KeepAlive(modes)
		runtime.KeepAlive(encs)
		if err != nil {
			return nil, fmt.Errorf("drm: get connector %d: %w", id, err)
		}
		if full.CountModes > nModes || full.CountEncoders > want.CountEncoders {
			continue
		}
		return &Connector{
			ID:         id,
			Type:       full.ConnectorType,
			TypeID:     full.ConnectorTypeID,
			Name:       ConnectorName(full.ConnectorType, full.ConnectorTypeID),
			Connection: full.Connection,
			EncoderID:  full.EncoderID,
			Encoders:   encs[:full.CountEncoders],
			Modes:      modes[:full.CountModes],
			MMWidth:    full.MMWidth,
			MMHeight:   full.MMHeight,
		}, nil
	}
	return nil, fmt.Errorf("drm: get connector %d: counts kept changing", id)
}

// Encoder reads one encoder.
func (c *Card) Encoder(id uint32) (*Encoder, error) {
	e := modeGetEncoder{EncoderID: id}
	if err := c.ioctl(ioctlModeGetEncoder, unsafe.Pointer(&e)); err != nil {
		return nil, fmt.Errorf("drm: get encoder %d: %w", id, err)
	}
	return &Encoder{ID: e.EncoderID, Type: e.EncoderType, CRTCID: e.CRTCID, PossibleCRTCs: e.PossibleCRTCs}, nil
}

// CRTC reads one CRTC's current state.
func (c *Card) CRTC(id uint32) (*CRTC, error) {
	cr := modeCRTC{CRTCID: id}
	if err := c.ioctl(ioctlModeGetCRTC, unsafe.Pointer(&cr)); err != nil {
		return nil, fmt.Errorf("drm: get crtc %d: %w", id, err)
	}
	return &CRTC{ID: cr.CRTCID, FBID: cr.FBID, X: cr.X, Y: cr.Y, ModeValid: cr.ModeValid != 0, Mode: cr.Mode}, nil
}

// SetCRTC scans fb out on crtc to the given connectors in mode. fb == 0
// with no connectors and a nil mode switches the CRTC off.
func (c *Card) SetCRTC(crtc, fb uint32, connectors []uint32, mode *ModeInfo) error {
	ids := alloc[uint32](len(connectors))
	copy(ids, connectors)
	cr := modeCRTC{
		SetConnectorsPtr: ptr(ids),
		CountConnectors:  uint32(len(ids)),
		CRTCID:           crtc,
		FBID:             fb,
	}
	if mode != nil {
		cr.Mode, cr.ModeValid = *mode, 1
	}
	err := c.ioctl(ioctlModeSetCRTC, unsafe.Pointer(&cr))
	runtime.KeepAlive(ids)
	if err != nil {
		return fmt.Errorf("drm: set crtc %d: %w", crtc, err)
	}
	return nil
}

// PlaneIDs lists the card's planes (all of them once universal planes are enabled).
func (c *Card) PlaneIDs() ([]uint32, error) {
	for range 10 {
		var pr modeGetPlaneRes
		if err := c.ioctl(ioctlModeGetPlaneRes, unsafe.Pointer(&pr)); err != nil {
			return nil, fmt.Errorf("drm: get plane resources: %w", err)
		}
		ids := alloc[uint32](int(pr.CountPlanes))
		want := pr.CountPlanes
		pr.PlaneIDPtr = ptr(ids)
		err := c.ioctl(ioctlModeGetPlaneRes, unsafe.Pointer(&pr))
		runtime.KeepAlive(ids)
		if err != nil {
			return nil, fmt.Errorf("drm: get plane resources: %w", err)
		}
		if pr.CountPlanes > want {
			continue
		}
		return ids[:pr.CountPlanes], nil
	}
	return nil, errors.New("drm: get plane resources: count kept changing")
}

// Plane reads one plane's current binding.
func (c *Card) Plane(id uint32) (*Plane, error) {
	p := modeGetPlane{PlaneID: id}
	if err := c.ioctl(ioctlModeGetPlane, unsafe.Pointer(&p)); err != nil {
		return nil, fmt.Errorf("drm: get plane %d: %w", id, err)
	}
	return &Plane{ID: p.PlaneID, CRTCID: p.CRTCID, FBID: p.FBID, PossibleCRTCs: p.PossibleCRTCs}, nil
}

// RmFB removes a framebuffer. The kernel switches off any CRTC still
// scanning it out.
func (c *Card) RmFB(id uint32) error {
	v := id
	if err := c.ioctl(ioctlModeRmFB, unsafe.Pointer(&v)); err != nil {
		return fmt.Errorf("drm: rmfb %d: %w", id, err)
	}
	return nil
}

// DirtyFB tells the driver the whole framebuffer changed. Drivers that scan
// out of a shadow copy (bochs, virtio-gpu, udl, most simple KMS drivers)
// only show CPU writes after this; drivers that scan out the dumb buffer
// directly (amdgpu, i915) do not implement it and return ENOSYS, which
// callers can ignore.
func (c *Card) DirtyFB(fb uint32) error {
	cmd := modeFBDirtyCmd{FBID: fb}
	if err := c.ioctl(ioctlModeDirtyFB, unsafe.Pointer(&cmd)); err != nil {
		return fmt.Errorf("drm: dirtyfb %d: %w", fb, err)
	}
	return nil
}

// Framebuffer is a CPU-mapped XRGB8888 dumb buffer registered as a KMS fb.
type Framebuffer struct {
	card          *Card
	ID            uint32
	Handle        uint32
	Width, Height int
	Pitch         int
	Pixels        []byte // mmap'ed, Pitch*Height bytes (at least)
}

// NewFramebuffer allocates a dumb buffer of w x h, adds it as a framebuffer
// (ADDFB2 XRGB8888, falling back to legacy ADDFB 24/32) and maps it.
// Dumb buffers work on every KMS driver, including virtio-gpu and bochs.
func (c *Card) NewFramebuffer(w, h int) (*Framebuffer, error) {
	cd := modeCreateDumb{Width: uint32(w), Height: uint32(h), BPP: 32}
	if err := c.ioctl(ioctlModeCreateDumb, unsafe.Pointer(&cd)); err != nil {
		return nil, fmt.Errorf("drm: create dumb %dx%d: %w", w, h, err)
	}
	fb := &Framebuffer{card: c, Handle: cd.Handle, Width: w, Height: h, Pitch: int(cd.Pitch)}
	fail := func(err error) (*Framebuffer, error) {
		fb.Destroy()
		return nil, err
	}

	f2 := modeFBCmd2{Width: uint32(w), Height: uint32(h), PixelFormat: FormatXRGB8888}
	f2.Handles[0], f2.Pitches[0] = cd.Handle, cd.Pitch
	if err := c.ioctl(ioctlModeAddFB2, unsafe.Pointer(&f2)); err == nil {
		fb.ID = f2.FBID
	} else {
		f1 := modeFBCmd{Width: uint32(w), Height: uint32(h), Pitch: cd.Pitch, BPP: 32, Depth: 24, Handle: cd.Handle}
		if err := c.ioctl(ioctlModeAddFB, unsafe.Pointer(&f1)); err != nil {
			return fail(fmt.Errorf("drm: addfb %dx%d: %w", w, h, err))
		}
		fb.ID = f1.FBID
	}

	md := modeMapDumb{Handle: cd.Handle}
	if err := c.ioctl(ioctlModeMapDumb, unsafe.Pointer(&md)); err != nil {
		return fail(fmt.Errorf("drm: map dumb: %w", err))
	}
	size := int(cd.Size)
	if size < fb.Pitch*h {
		size = fb.Pitch * h
	}
	mem, err := unix.Mmap(c.Fd(), int64(md.Offset), size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fail(fmt.Errorf("drm: mmap dumb: %w", err))
	}
	fb.Pixels = mem
	return fb, nil
}

// Destroy unmaps the buffer, removes the fb and frees the dumb buffer.
func (fb *Framebuffer) Destroy() {
	if fb.Pixels != nil {
		unix.Munmap(fb.Pixels)
		fb.Pixels = nil
	}
	if fb.ID != 0 {
		fb.card.RmFB(fb.ID)
		fb.ID = 0
	}
	if fb.Handle != 0 {
		d := modeDestroyDumb{Handle: fb.Handle}
		fb.card.ioctl(ioctlModeDestroyDumb, unsafe.Pointer(&d))
		fb.Handle = 0
	}
}
