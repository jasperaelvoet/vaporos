package drm

import (
	"fmt"
)

// FindConnector returns the connector called name ("DP-1") without probing.
func (c *Card) FindConnector(name string) (*Connector, error) {
	res, err := c.Resources()
	if err != nil {
		return nil, err
	}
	for _, id := range res.Connectors {
		conn, err := c.Connector(id, false)
		if err != nil {
			continue
		}
		if conn.Name == name {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("drm: %s: no connector %s", c.Path, name)
}

// Scanout describes what a connector currently shows.
type Scanout struct {
	CRTC   uint32
	FB     uint32 // framebuffer on the primary plane (0 = nothing scanned out)
	Mode   ModeInfo
	Active bool // CRTC has a valid mode and a framebuffer
}

// ScanoutOf reads, without DRM master and without probing, which CRTC drives
// connector name and in which mode. vosd uses it to wait until gamescope has
// really applied the mode a Moonlight client asked for.
func (c *Card) ScanoutOf(name string) (Scanout, error) {
	conn, err := c.FindConnector(name)
	if err != nil {
		return Scanout{}, err
	}
	if conn.EncoderID == 0 {
		return Scanout{}, nil
	}
	enc, err := c.Encoder(conn.EncoderID)
	if err != nil {
		return Scanout{}, err
	}
	if enc.CRTCID == 0 {
		return Scanout{}, nil
	}
	crtc, err := c.CRTC(enc.CRTCID)
	if err != nil {
		return Scanout{}, err
	}
	return Scanout{
		CRTC:   crtc.ID,
		FB:     crtc.FBID,
		Mode:   crtc.Mode,
		Active: crtc.ModeValid && crtc.FBID != 0,
	}, nil
}

// PlanesOn counts the planes that currently scan a framebuffer out on crtc.
// It needs the universal-planes client cap to see primary and cursor planes.
func (c *Card) PlanesOn(crtc uint32) (int, error) {
	ids, err := c.PlaneIDs()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		p, err := c.Plane(id)
		if err != nil {
			continue
		}
		if p.CRTCID == crtc && p.FBID != 0 {
			n++
		}
	}
	return n, nil
}
