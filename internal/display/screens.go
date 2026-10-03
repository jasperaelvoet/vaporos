package display

// screens.json (docs/CONTRACTS.md, Paths and Display policy, Scaling): a
// screen per Moonlight device vosd can tell apart, with the user's
// choices, and the hints browsers gave about their own screens.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

const (
	maxScreens     = 64
	maxHints       = 64
	maxScreenModes = 4
	hintTTL        = 180 * 24 * time.Hour
	ipHintTTL      = 24 * time.Hour     // a hint keyed by IP: addresses move on
	ipScreenTTL    = 7 * 24 * time.Hour // a screen keyed by name@ip, since last seen
	// sharedMACWindow is how far back the addresses seen with a MAC count
	// when telling whether several devices share it (a range extender that
	// rewrites MACs).
	sharedMACWindow = 7 * 24 * time.Hour
)

// Screen is one device's screen.
type Screen struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	// Kind is the user's pick, "" for automatic.
	Kind Kind `json:"kind"`
	// Size is the user's factor on VaporOS's own scale (1.0), unrounded.
	Size float64 `json:"size"`
	// SteamAuto: the screen uses Steam's own automatic scale.
	SteamAuto bool `json:"steam_auto"`
	// Modes are the last modes the device asked for, newest first.
	Modes     []string  `json:"modes"`
	Guess     Kind      `json:"guess"`
	GuessFrom Source    `json:"guess_from"`
	LastSeen  time.Time `json:"last_seen"`
	// UIScale and GameDPI are the Steam scale and Xft.dpi last applied.
	UIScale float64 `json:"ui_scale"`
	GameDPI int     `json:"game_dpi"`
}

// Hint is what a browser said about its own screen (POST /display/hint),
// or the kind picked at pairing (You).
type Hint struct {
	Kind  Kind      `json:"kind"`
	W     int       `json:"w"`
	H     int       `json:"h"`
	DPR   float64   `json:"dpr"`
	Touch int       `json:"touch"`
	At    time.Time `json:"at"`
	You   bool      `json:"you,omitempty"`
	// IP is the IPv4 address the hint came from, so a MAC that several
	// devices share does not lend one device's hint to another.
	IP string `json:"ip,omitempty"`
}

// Screens is screens.json. It is not safe for concurrent use: vosd keeps
// one in memory under a lock of its own and writes it through.
type Screens struct {
	ByID map[string]Screen `json:"screens"`
	// Hints are keyed by MAC, else by IPv4 address.
	Hints map[string]Hint `json:"hints"`
	// HandbackPending: Steam still has to get its automatic scale back
	// after display.ui_scaling was turned off.
	HandbackPending bool `json:"handback_pending"`
}

// NewScreens returns an empty set.
func NewScreens() *Screens {
	return &Screens{ByID: map[string]Screen{}, Hints: map[string]Hint{}}
}

// LoadScreens reads screens.json and always returns a usable set: a
// missing file is an empty one, and a file that does not parse is renamed
// <path>.bad (replacing an older one) and started afresh, with an error
// that says so.
func LoadScreens(path string) (*Screens, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewScreens(), nil
	}
	if err != nil {
		return NewScreens(), err
	}
	s := NewScreens()
	if err := json.Unmarshal(b, s); err != nil {
		if rerr := os.Rename(path, path+".bad"); rerr != nil {
			return NewScreens(), fmt.Errorf("%s: %v; keeping it aside failed: %v", path, err, rerr)
		}
		return NewScreens(), fmt.Errorf("%s: %v; moved to %s.bad", path, err, path)
	}
	s.normalise()
	return s, nil
}

// normalise makes a hand-edited or older file safe to use.
func (s *Screens) normalise() {
	if s.ByID == nil {
		s.ByID = map[string]Screen{}
	}
	if s.Hints == nil {
		s.Hints = map[string]Hint{}
	}
	for id, sc := range s.ByID {
		if !sc.Kind.Valid() {
			sc.Kind = ""
		}
		if !sc.Guess.Valid() {
			sc.Guess, sc.GuessFrom = "", ""
		}
		sc.Size = normSize(sc.Size)
		if len(sc.Modes) > maxScreenModes {
			sc.Modes = sc.Modes[:maxScreenModes]
		}
		s.ByID[id] = sc
	}
	for k, h := range s.Hints {
		if !hintKeyOK(k) {
			delete(s.Hints, k)
			continue
		}
		if !h.Kind.Valid() {
			h.Kind = KindUnknown
		}
		s.Hints[k] = h
	}
}

func normSize(v float64) float64 {
	if v == 0 {
		return 1
	}
	return min(max(v, SizeMin), SizeMax)
}

// Save writes the file atomically, readable by root only: it holds the
// LAN's MAC and IP addresses.
func (s *Screens) Save(path string) error {
	return config.WriteJSONAtomic(path, s, 0o600)
}

// ScreenKey is what tells a device's screen apart: its name when that is
// not generic, else <name>@<mac>, else <name>@<ip>, else "" (the session
// is inference only). KeyFor refines it with what screens.json knows.
func ScreenKey(name, mac, ip string) string {
	name = strings.TrimSpace(name)
	if !GenericName(name) {
		return name
	}
	if m := normMAC(mac); m != "" {
		return name + "@" + m
	}
	if a, ok := normAddr(ip); ok {
		return name + "@" + a.String()
	}
	return ""
}

// KeyFor is the key of the screen of the device named name at mac/ip:
// ScreenKey's, except for a generic name at an address several devices
// share (see sharedAddr), which tells no device apart (""), and for a
// second device with another IPv4 address behind a MAC the first one's
// screen went with lately (a range extender that rewrites MACs), which
// gets <name>@<mac>@<ip>. The first device keeps its screen: only its own
// sessions update that screen's address.
func (s *Screens) KeyFor(name, mac, ip string, now time.Time) string {
	key := ScreenKey(name, mac, ip)
	if key == "" || !GenericName(name) {
		return key
	}
	if s.sharedAddr(mac, ip, now) {
		return ""
	}
	v4 := normIPv4(ip)
	if normMAC(mac) == "" || v4 == "" {
		return key
	}
	if sc, ok := s.ByID[ScreenID(key)]; ok {
		if old := normIPv4(sc.IP); old != "" && old != v4 && now.Sub(sc.LastSeen) <= sharedMACWindow {
			return key + "@" + v4
		}
	}
	return key
}

// ScreenID is a screen's id: the first 12 hex digits of its key's sha256.
func ScreenID(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

func normMAC(mac string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil || len(hw) != 6 || slices.Equal(hw, make(net.HardwareAddr, 6)) {
		return ""
	}
	return hw.String()
}

// normAddr parses a client address without zone and IPv4-mapping;
// loopback and unspecified addresses are no client's.
func normAddr(ip string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return netip.Addr{}, false
	}
	a = a.WithZone("").Unmap()
	return a, !a.IsLoopback() && !a.IsUnspecified()
}

// normIPv4 is ip as a hint may be keyed or matched by it ("" for IPv6).
func normIPv4(ip string) string {
	if a, ok := normAddr(ip); ok && a.Is4() {
		return a.String()
	}
	return ""
}

func hintKeyOK(k string) bool { return k != "" && (normMAC(k) == k || normIPv4(k) == k) }

// keyedByIP: a screen keyed by <name>@<ip>, which expires.
func (sc Screen) keyedByIP() bool { return GenericName(sc.Name) && sc.MAC == "" }

func screenExpired(sc Screen, now time.Time) bool {
	return sc.keyedByIP() && now.Sub(sc.LastSeen) > ipScreenTTL
}

func hintExpired(key string, h Hint, now time.Time) bool {
	ttl := hintTTL
	if normIPv4(key) == key {
		ttl = ipHintTTL
	}
	return now.Sub(h.At) > ttl
}

// Prune drops expired hints and screens, then the least recently seen
// beyond the caps.
func (s *Screens) Prune(now time.Time) {
	for id, sc := range s.ByID {
		if screenExpired(sc, now) {
			delete(s.ByID, id)
		}
	}
	for k, h := range s.Hints {
		if hintExpired(k, h, now) {
			delete(s.Hints, k)
		}
	}
	for len(s.ByID) > maxScreens {
		delete(s.ByID, oldestKey(s.ByID, func(sc Screen) time.Time { return sc.LastSeen }))
	}
	for len(s.Hints) > maxHints {
		delete(s.Hints, oldestKey(s.Hints, func(h Hint) time.Time { return h.At }))
	}
}

func oldestKey[V any](m map[string]V, at func(V) time.Time) string {
	oldest := ""
	for k, v := range m {
		if oldest == "" || at(v).Before(at(m[oldest])) || (at(v).Equal(at(m[oldest])) && k < oldest) {
			oldest = k
		}
	}
	return oldest
}

// Get returns a copy of a screen.
func (s *Screens) Get(id string) (Screen, bool) {
	sc, ok := s.ByID[id]
	sc.Modes = slices.Clone(sc.Modes)
	return sc, ok
}

// Touch records that the device behind id began a session asking for
// mode: it creates the screen if needed, keeps its latest name and
// addresses and puts the mode first in its history. It returns a copy.
func (s *Screens) Touch(id, name, mac, ip string, mode edid.Mode, now time.Time) Screen {
	if id == "" {
		return Screen{}
	}
	sc, ok := s.ByID[id]
	if !ok {
		sc = Screen{Size: 1}
	}
	sc.Name = strings.TrimSpace(name)
	if m := normMAC(mac); m != "" {
		sc.MAC = m
	}
	if a, ok := normAddr(ip); ok {
		sc.IP = a.String()
	}
	md := mode.String()
	sc.Modes = append([]string{md}, slices.DeleteFunc(slices.Clone(sc.Modes), func(m string) bool { return m == md })...)
	if len(sc.Modes) > maxScreenModes {
		sc.Modes = sc.Modes[:maxScreenModes]
	}
	sc.LastSeen = now
	s.ByID[id] = sc
	s.Prune(now)
	sc.Modes = slices.Clone(sc.Modes)
	return sc
}

// Update changes a screen in place; false when there is none.
func (s *Screens) Update(id string, f func(*Screen)) bool {
	sc, ok := s.ByID[id]
	if !ok {
		return false
	}
	f(&sc)
	s.ByID[id] = sc
	return true
}

// Delete forgets a screen (its hints stay); false when there is none.
func (s *Screens) Delete(id string) bool {
	_, ok := s.ByID[id]
	delete(s.ByID, id)
	return ok
}

// History is the screen's modes, newest first, as modes.
func (sc Screen) History() []edid.Mode {
	var out []edid.Mode
	for _, m := range sc.Modes {
		if md, err := edid.ParseMode(m); err == nil {
			out = append(out, md)
		}
	}
	return out
}

// PUT /display/screens/{id}'s answers (docs/CONTRACTS.md "HTTP API").
var (
	ErrNoScreen = errors.New("no such screen")
	ErrBadKind  = errors.New("kind must be auto, phone, handheld, tablet, laptop, monitor or tv")
	ErrBadSize  = errors.New("size must be between 0.4 and 2.5")
)

// ScreenEdit is the body of PUT /display/screens/{id}; nil fields stay.
type ScreenEdit struct {
	Kind      *string  `json:"kind"`
	Size      *float64 `json:"size"`
	SteamAuto *bool    `json:"steam_auto"`
}

// Edit applies a PUT /display/screens/{id} (the body is checked first,
// then the id): kind "auto" makes the screen automatic again and another
// kind is the user's pick; a new kind resets size to 1.0 unless the body
// has one; a size alone pins inEffect (the kind the screen has now; the
// stored guess when it is not valid) as the user's, unless vetoed (inEffect
// is this session's veto, which is never stored: the kind stays automatic
// and the size goes with whatever kind a session infers); a kind or size
// without steam_auto turns steam_auto off. It returns the stored screen.
func (s *Screens) Edit(id string, e ScreenEdit, inEffect Kind, vetoed bool) (Screen, error) {
	var kind Kind
	if e.Kind != nil && *e.Kind != "auto" {
		k, ok := UserKind(*e.Kind)
		if !ok {
			return Screen{}, ErrBadKind
		}
		kind = k
	}
	if e.Size != nil && !(*e.Size >= SizeMin && *e.Size <= SizeMax) {
		return Screen{}, ErrBadSize
	}
	sc, ok := s.ByID[id]
	if !ok || id == "" {
		return Screen{}, ErrNoScreen
	}
	if e.Kind != nil {
		if kind != sc.Kind {
			sc.Size = 1
		}
		sc.Kind = kind
	}
	if e.Size != nil {
		sc.Size = *e.Size
		if e.Kind == nil && sc.Kind == "" && !vetoed {
			if !inEffect.Valid() {
				inEffect = sc.View(id).Kind
			}
			sc.Kind = inEffect
		}
	}
	switch {
	case e.SteamAuto != nil:
		sc.SteamAuto = *e.SteamAuto
	case e.Kind != nil || e.Size != nil:
		sc.SteamAuto = false
	}
	s.ByID[id] = sc
	sc.Modes = slices.Clone(sc.Modes)
	return sc, nil
}

// hintKey is where a device's hint lives: its MAC, else its IPv4 address
// (never an IPv6 address alone).
func hintKey(mac, ip string) string {
	if m := normMAC(mac); m != "" {
		return m
	}
	return normIPv4(ip)
}

// PutHint stores a browser's hint for the device at mac/ip, keeping a kind
// picked at pairing; false when the device cannot be keyed, or shares its
// address with others (no hint from there is any one device's).
func (s *Screens) PutHint(mac, ip string, h Hint) bool {
	key := hintKey(mac, ip)
	if key == "" || s.sharedAddr(mac, ip, h.At) {
		return false
	}
	h.IP, h.You = normIPv4(ip), false
	if !h.Kind.Valid() {
		h.Kind = KindUnknown
	}
	if old, ok := s.Hints[key]; ok && old.You {
		h.Kind, h.You = old.Kind, true
	}
	s.Hints[key] = h
	s.Prune(h.At)
	return true
}

// PutPick stores the kind picked at pairing for the device at mac/ip as a
// hint the user chose, keeping what its browser said about its screen,
// until the device's first session with a key takes it (takePick); false
// when the device cannot be keyed or shares its address with others, or k
// is no kind a person picks.
func (s *Screens) PutPick(mac, ip string, k Kind, now time.Time) bool {
	key := hintKey(mac, ip)
	if _, ok := UserKind(string(k)); !ok || key == "" || s.sharedAddr(mac, ip, now) {
		return false
	}
	h := s.Hints[key]
	h.Kind, h.You, h.At, h.IP = k, true, now, normIPv4(ip)
	s.Hints[key] = h
	s.Prune(now)
	return true
}

// HintFor is the hint of the device at mac/ip, nil when there is none
// within its lifetime. A hint under the device's MAC needs its address to
// match too when another address went with that MAC lately; one under its
// IPv4 address is the fallback. An address several devices share has none.
func (s *Screens) HintFor(mac, ip string, now time.Time) *Hint {
	if _, h, ok := s.hintAt(mac, ip, now); ok {
		return &h
	}
	return nil
}

// hintAt is HintFor with the key the hint is kept under.
func (s *Screens) hintAt(mac, ip string, now time.Time) (string, Hint, bool) {
	if s.sharedAddr(mac, ip, now) {
		return "", Hint{}, false
	}
	v4 := normIPv4(ip)
	if m := normMAC(mac); m != "" {
		if h, ok := s.Hints[m]; ok && !hintExpired(m, h, now) && (h.IP == v4 || !s.macShared(m, h, v4, now)) {
			return m, h, true
		}
	}
	if v4 != "" {
		if h, ok := s.Hints[v4]; ok && !hintExpired(v4, h, now) {
			return v4, h, true
		}
	}
	return "", Hint{}, false
}

// takePick hands the kind picked at pairing for the device at mac/ip over
// to its screen: it returns the pick and clears it from the hint, whose
// kind is unknown again until the device's browser tells it (the pick had
// replaced it). The screen keeps the pick as the user's kind, which
// Automatic, Reset and forgetting the screen undo; a standing pick in the
// hint would outrank all three.
func (s *Screens) takePick(mac, ip string, now time.Time) (Kind, bool) {
	key, h, ok := s.hintAt(mac, ip, now)
	if !ok || !h.You {
		return "", false
	}
	k, isPick := UserKind(string(h.Kind))
	h.Kind, h.You = KindUnknown, false
	s.Hints[key] = h
	return k, isPick
}

// sharedAddr: the device at mac/ip shares its IPv4 address with other
// devices, as behind a router that NATs them (a VPN's subnet router, a
// second router): screens of different names were seen at that address
// (with that MAC, when both are known) within sharedMACWindow. Its MAC is
// then the router's too, so neither tells one device there from another.
func (s *Screens) sharedAddr(mac, ip string, now time.Time) bool {
	v4 := normIPv4(ip)
	if v4 == "" {
		return false
	}
	m := normMAC(mac)
	names := map[string]bool{}
	for _, sc := range s.ByID {
		if normIPv4(sc.IP) != v4 || now.Sub(sc.LastSeen) > sharedMACWindow || (m != "" && sc.MAC != "" && sc.MAC != m) {
			continue
		}
		names[strings.ToLower(strings.TrimSpace(sc.Name))] = true
		if len(names) > 1 {
			return true
		}
	}
	return false
}

// macShared: an address other than v4 went with mac lately, in its hint or
// in a screen.
func (s *Screens) macShared(mac string, h Hint, v4 string, now time.Time) bool {
	other := func(ip string, at time.Time) bool { return ip != "" && ip != v4 && now.Sub(at) <= sharedMACWindow }
	if other(h.IP, h.At) {
		return true
	}
	for _, sc := range s.ByID {
		if sc.MAC == mac && other(normIPv4(sc.IP), sc.LastSeen) {
			return true
		}
	}
	return false
}

// ScreenView is one screen as GET /display lists it.
type ScreenView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      Kind      `json:"kind"`
	KindFrom  Source    `json:"kind_from"`
	Guess     Kind      `json:"guess"`
	Size      float64   `json:"size"`
	SteamAuto bool      `json:"steam_auto"`
	Mode      string    `json:"mode"`
	LastSeen  time.Time `json:"last_seen"`
	UIScale   float64   `json:"ui_scale"`
	GameDPI   int       `json:"game_dpi"`
	Savable   bool      `json:"savable"`
	// SizeMin and SizeMax, on the screen streaming now only: the sizes
	// beyond which Steam's slider bounds hold its scale on the session's
	// mode (SizeRange).
	SizeMin float64 `json:"size_min,omitempty"`
	SizeMax float64 `json:"size_max,omitempty"`
}

// ScreenRef is a session's screen as session.begin carries it.
type ScreenRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     Kind    `json:"kind"`
	KindFrom Source  `json:"kind_from"`
	UIScale  float64 `json:"ui_scale"`
	GameDPI  int     `json:"game_dpi"`
}

// Ref is the view's session.begin form.
func (v ScreenView) Ref() ScreenRef {
	return ScreenRef{ID: v.ID, Name: v.Name, Kind: v.Kind, KindFrom: v.KindFrom, UIScale: v.UIScale, GameDPI: v.GameDPI}
}

// View is a stored screen as GET /display lists it while it does not
// stream: the user's pick, else its last guess.
func (sc Screen) View(id string) ScreenView {
	v := ScreenView{
		ID: id, Name: sc.Name, Kind: sc.Kind, KindFrom: FromYou, Guess: sc.Guess,
		Size: normSize(sc.Size), SteamAuto: sc.SteamAuto, LastSeen: sc.LastSeen,
		UIScale: sc.UIScale, GameDPI: sc.GameDPI, Savable: id != "",
	}
	if !v.Guess.Valid() {
		v.Guess = KindUnknown
	}
	if !v.Kind.Valid() {
		v.Kind, v.KindFrom = sc.Guess, sc.GuessFrom
		if !sc.Guess.Valid() || sc.GuessFrom == "" {
			v.Kind, v.KindFrom = KindUnknown, FromDefault
		}
	}
	if len(sc.Modes) > 0 {
		v.Mode = sc.Modes[0]
	}
	return v
}

// Views lists the screens for GET /display, most recently seen first and
// never nil, without expired ones. live, when set, is the screen of the
// session streaming now: it replaces its stored view, or comes first when
// the device cannot be told apart (an empty ID).
func (s *Screens) Views(live *ScreenView, now time.Time) []ScreenView {
	ids := make([]string, 0, len(s.ByID))
	for id, sc := range s.ByID {
		if !screenExpired(sc, now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.ByID[ids[i]].LastSeen, s.ByID[ids[j]].LastSeen
		if !a.Equal(b) {
			return a.After(b)
		}
		return ids[i] < ids[j]
	})
	out := make([]ScreenView, 0, len(ids)+1)
	if live != nil && live.ID == "" {
		v := *live
		v.Savable = false
		out = append(out, v)
	}
	for _, id := range ids {
		if live != nil && live.ID == id {
			out = append(out, *live)
			continue
		}
		out = append(out, s.ByID[id].View(id))
	}
	return out
}
