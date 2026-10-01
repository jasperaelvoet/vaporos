package display

import (
	"errors"
	"io/fs"
	"sort"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// ClientMode is one mode a Moonlight client asked for
// (/var/lib/vos/clients.json, docs/CONTRACTS.md).
type ClientMode struct {
	Name     string    `json:"name,omitempty"`
	W        int       `json:"w"`
	H        int       `json:"h"`
	FPS      int       `json:"fps"`
	HDR      bool      `json:"hdr"`
	LastSeen time.Time `json:"last_seen"`
}

// Mode returns the client's mode.
func (c ClientMode) Mode() edid.Mode { return edid.Mode{W: c.W, H: c.H, Refresh: c.FPS} }

// Clients holds one entry per client and mode, so a mode stays learned when
// the same client, or another one under the same name, asks for a different
// mode later; without a client name from Sunshine every device is
// "Moonlight". Entries from before names were stored are keyed by name alone
// and move to their mode's key on load.
type Clients map[string]ClientMode

// maxClients bounds clients.json; the oldest entries are forgotten first.
const maxClients = 64

// LoadClients reads a clients file; a missing file is an empty set.
func LoadClients(path string) (Clients, error) {
	c := Clients{}
	err := config.ReadJSON(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		return Clients{}, nil
	}
	if err != nil {
		return Clients{}, err
	}
	for k, cm := range c {
		if cm.Name == "" {
			cm.Name = k
			delete(c, k)
			c[clientKey(k, cm.Mode())] = cm
		}
	}
	return c, nil
}

func clientKey(name string, md edid.Mode) string { return name + " " + md.String() }

// Save writes the file atomically.
func (c Clients) Save(path string) error { return config.WriteJSONAtomic(path, c, 0o644) }

// Record stores a mode a client asked for, evicting the least recently seen
// entries beyond maxClients.
func (c Clients) Record(name string, cm ClientMode) {
	if name == "" {
		name = "unknown"
	}
	cm.Name = name
	c[clientKey(name, cm.Mode())] = cm
	for len(c) > maxClients {
		oldest := ""
		for n, v := range c {
			if oldest == "" || v.LastSeen.Before(c[oldest].LastSeen) {
				oldest = n
			}
		}
		delete(c, oldest)
	}
}

// newestFirst returns the keys, most recently seen first.
func (c Clients) newestFirst() []string {
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := c[names[i]], c[names[j]]
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		return names[i] < names[j]
	})
	return names
}

// Modes returns the distinct client modes, most recently seen first, so the
// EDID's extra-mode cap drops the stalest ones.
func (c Clients) Modes() []edid.Mode {
	seen := map[edid.Mode]bool{}
	var out []edid.Mode
	for _, n := range c.newestFirst() {
		m := c[n].Mode()
		if m.W > 0 && m.H > 0 && m.Refresh > 0 && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// deviceMode is one clients.json entry as GET /display lists it.
type deviceMode struct {
	Name     string    `json:"name"`
	Mode     string    `json:"mode"`
	HDR      bool      `json:"hdr"`
	LastSeen time.Time `json:"last_seen"`
}

// devices lists every mode each client asked for, most recently seen first,
// so a client's first entry is its last mode.
func (c Clients) devices() []deviceMode {
	out := []deviceMode{}
	for _, n := range c.newestFirst() {
		cm := c[n]
		out = append(out, deviceMode{Name: cm.Name, Mode: cm.Mode().String(), HDR: cm.HDR, LastSeen: cm.LastSeen})
	}
	return out
}

// forget deletes every entry that asked for md and reports whether any did.
func (c Clients) forget(md edid.Mode) bool {
	found := false
	for n, cm := range c {
		if cm.Mode() == md {
			delete(c, n)
			found = true
		}
	}
	return found
}
