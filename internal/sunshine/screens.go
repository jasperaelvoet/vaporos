package sunshine

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/display"
)

// Screens is the display manager's side of interface scaling (see
// docs/CONTRACTS.md, Display policy, Scaling): what only Sunshine's side
// gets to see.
type Screens interface {
	// NoteResume: a Moonlight device resumed the running app. That runs no
	// prep command, so nothing tells the scaler which device is watching;
	// it stops holding and adopting Steam's scale until the next begin.
	NoteResume()
	// PairHint: the device waiting at remote has just paired. kind is the
	// user's pick at pairing ("" for none); userAgent is the browser's when
	// the PIN came from that device itself ("" otherwise).
	PairHint(remote net.IP, kind string, userAgent string)
}

// browserKinds is what Screens may also offer: the kind of the browser hint
// stored for remote ("" for none). DeviceLabel needs it to call an iPad
// that asks for desktop sites an iPad rather than a Mac.
type browserKinds interface {
	BrowserKind(remote net.IP) string
}

// sharedAddrs is what Screens may also offer: whether several devices
// were seen lately at remote's address, behind a router that NATs them,
// where its address and MAC are the router's for all of them.
type sharedAddrs interface {
	SharedAddr(remote net.IP) bool
}

// The display manager offers all of it; an optional method that drifted
// would otherwise just go unused.
var (
	_ Screens      = (*display.Manager)(nil)
	_ browserKinds = (*display.Manager)(nil)
	_ sharedAddrs  = (*display.Manager)(nil)
)

// SetScreens connects the display manager; until then (and with nil)
// pairing and resumes tell it nothing.
func (s *Service) SetScreens(x Screens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scr = x
}

func (s *Service) screens() Screens {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scr
}

// peerAddr is the request's source address. vosd is never behind a proxy,
// so forwarding headers mean nothing.
func peerAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return normAddr(ap.Addr())
	}
	a, _ := netip.ParseAddr(r.RemoteAddr)
	return normAddr(a)
}

// parseAddr reads an address as Sunshine reports a waiting device's.
func parseAddr(s string) netip.Addr {
	s = strings.Trim(strings.TrimSpace(s), "[]")
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return normAddr(a)
}

func normAddr(a netip.Addr) netip.Addr { return a.WithZone("").Unmap() }

// sameDevice reports whether the browser at from is the device waiting to
// pair at at: the same address, or the same MAC. A device has one IPv4
// address but several IPv6 ones, and Safari may reach vaporos.local over
// IPv6 while Moonlight pairs over IPv4. Two IPv4 addresses behind one MAC
// are two devices behind a bridge that rewrites MACs, so the MAC only
// counts when an IPv6 address is involved. An address the display manager
// saw several devices at is no one device's.
func (s *Service) sameDevice(from, at netip.Addr) bool {
	switch {
	case !from.IsValid() || !at.IsValid() || from.IsLoopback() || at.IsLoopback():
		return false
	case s.sharedAddr(at) || s.sharedAddr(from):
		return false
	case from == at:
		return true
	case from.Is4() && at.Is4():
		return false
	}
	mac := s.neighbourMAC(from)
	return mac != "" && mac == s.neighbourMAC(at)
}

// sharedAddr asks the display manager whether a is an address several
// devices share (false when it cannot tell).
func (s *Service) sharedAddr(a netip.Addr) bool {
	sa, ok := s.screens().(sharedAddrs)
	return ok && sa.SharedAddr(net.IP(a.AsSlice()))
}

// labelKind is what tells DeviceLabel an iPad on desktop sites from a Mac
// and a Steam Deck from another Linux PC: the browser's stored hint, else
// the user's pick.
func (s *Service) labelKind(from netip.Addr, picked string) display.Kind {
	if bk, ok := s.screens().(browserKinds); ok {
		if k, ok := display.UserKind(bk.BrowserKind(net.IP(from.AsSlice()))); ok {
			return k
		}
	}
	return display.Kind(picked)
}

// uniqueName is label, or "label 2", "label 3", ... when a paired device
// already has that name (case aside).
func uniqueName(label string, paired []PairedClient) string {
	taken := map[string]bool{}
	for _, c := range paired {
		taken[strings.ToLower(strings.TrimSpace(c.Name))] = true
	}
	name := label
	for n := 2; taken[strings.ToLower(name)]; n++ {
		name = fmt.Sprintf("%s %d", label, n)
	}
	return name
}
