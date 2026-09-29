//go:build !linux

package drm

// Card is an open DRM primary node. On non-Linux platforms (the dev Mac)
// nothing can be opened; the type exists so callers compile everywhere.
type Card struct{ Path string }

func Open(path string, keepMaster bool) (*Card, error) { return nil, ErrUnsupported }

func (c *Card) Close() error                                        { return ErrUnsupported }
func (c *Card) Fd() int                                             { return -1 }
func (c *Card) SetMaster() error                                    { return ErrUnsupported }
func (c *Card) DropMaster() error                                   { return ErrUnsupported }
func (c *Card) SetClientCap(capability, value uint64) error         { return ErrUnsupported }
func (c *Card) Resources() (*Resources, error)                      { return nil, ErrUnsupported }
func (c *Card) Connector(id uint32, probe bool) (*Connector, error) { return nil, ErrUnsupported }
func (c *Card) Encoder(id uint32) (*Encoder, error)                 { return nil, ErrUnsupported }
func (c *Card) CRTC(id uint32) (*CRTC, error)                       { return nil, ErrUnsupported }
func (c *Card) PlaneIDs() ([]uint32, error)                         { return nil, ErrUnsupported }
func (c *Card) Plane(id uint32) (*Plane, error)                     { return nil, ErrUnsupported }
func (c *Card) RmFB(id uint32) error                                { return ErrUnsupported }

func (c *Card) SetCRTC(crtc, fb uint32, connectors []uint32, mode *ModeInfo) error {
	return ErrUnsupported
}

// Framebuffer is a CPU-mapped dumb buffer (Linux only).
type Framebuffer struct {
	ID            uint32
	Handle        uint32
	Width, Height int
	Pitch         int
	Pixels        []byte
}

func (c *Card) NewFramebuffer(w, h int) (*Framebuffer, error) { return nil, ErrUnsupported }

func (fb *Framebuffer) Destroy() {}
