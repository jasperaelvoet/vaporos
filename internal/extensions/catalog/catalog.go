// Package catalog reads and writes /usr/lib/vos/extensions.list, the list of
// extension images an OS image was built with (docs/CONTRACTS.md
// "Extensions"). It lives inside the read-only root, so it is what the
// initramfs trusts: an image mounts only if its fs-verity digest is the one
// listed here.
//
// The format is line based so the initramfs (busybox ash) can read it:
//
//	# comment
//	dispatcher 1
//	ext <id> <sha256> <size> <fsverity> <core|-> [requires...]
//
// ext lines come in dependency order (requirements first). Unknown line
// kinds are ignored, so later images can add some.
package catalog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// Dispatcher is the `vos ext launch` level this vos understands. Steam launch
// options may only point at the dispatcher once every bootable slot has it.
const Dispatcher = 1

// MaxSize bounds the file; it holds one short line per extension.
const MaxSize = 1 << 20

// Entry is one extension image.
type Entry struct {
	ID       string
	SHA256   string
	Size     int64
	FSVerity string
	Core     bool
	Requires []string
}

// Catalog is a parsed extensions.list.
type Catalog struct {
	Dispatcher int
	Entries    []Entry // dependency order
}

// Get returns the entry for id.
func (c *Catalog) Get(id string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	for _, e := range c.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// IDs returns every id in catalog order.
func (c *Catalog) IDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.Entries))
	for i, e := range c.Entries {
		out[i] = e.ID
	}
	return out
}

// Core returns the ids of the core extensions in catalog order.
func (c *Catalog) Core() []string {
	var out []string
	if c == nil {
		return out
	}
	for _, e := range c.Entries {
		if e.Core {
			out = append(out, e.ID)
		}
	}
	return out
}

// Closure returns ids plus everything they require, in catalog order. Ids
// the catalog does not carry are left out.
func (c *Catalog) Closure(ids []string) []string {
	want := map[string]bool{}
	var add func(id string)
	add = func(id string) {
		if want[id] {
			return
		}
		e, ok := c.Get(id)
		if !ok {
			return
		}
		want[id] = true
		for _, r := range e.Requires {
			add(r)
		}
	}
	for _, id := range ids {
		add(id)
	}
	var out []string
	for _, id := range c.IDs() {
		if want[id] {
			out = append(out, id)
		}
	}
	return out
}

// Dependents returns the ids in the catalog that require id, directly or not.
func (c *Catalog) Dependents(id string) []string {
	var out []string
	for _, e := range c.Entries {
		if e.ID != id && slices.Contains(c.Closure([]string{e.ID}), id) {
			out = append(out, e.ID)
		}
	}
	return out
}

// Load reads a catalog file. A missing file is an empty catalog (an image
// built before extensions), reported with fs.ErrNotExist.
func Load(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return &Catalog{}, err
	}
	defer f.Close()
	return Parse(io.LimitReader(f, MaxSize))
}

// Parse reads a catalog.
func Parse(r io.Reader) (*Catalog, error) {
	c := &Catalog{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "dispatcher":
			if len(f) != 2 {
				return nil, fmt.Errorf("catalog line %d: dispatcher takes one number", n)
			}
			v, err := strconv.Atoi(f[1])
			if err != nil || v < 0 {
				return nil, fmt.Errorf("catalog line %d: bad dispatcher level %q", n, f[1])
			}
			c.Dispatcher = v
		case "ext":
			e, err := parseExt(f[1:])
			if err != nil {
				return nil, fmt.Errorf("catalog line %d: %w", n, err)
			}
			if seen[e.ID] {
				return nil, fmt.Errorf("catalog line %d: %s listed twice", n, e.ID)
			}
			for _, r := range e.Requires {
				if !seen[r] {
					return nil, fmt.Errorf("catalog line %d: %s requires %s, which is not listed before it", n, e.ID, r)
				}
			}
			seen[e.ID] = true
			c.Entries = append(c.Entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

func parseExt(f []string) (Entry, error) {
	if len(f) < 5 {
		return Entry{}, errors.New("ext needs id, sha256, size, fsverity and core")
	}
	e := Entry{ID: f[0], SHA256: f[1], FSVerity: f[3]}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || size <= 0 {
		return Entry{}, fmt.Errorf("%s: bad size %q", e.ID, f[2])
	}
	e.Size = size
	switch f[4] {
	case "core":
		e.Core = true
	case "-":
	default:
		return Entry{}, fmt.Errorf("%s: core must be 'core' or '-', not %q", e.ID, f[4])
	}
	if len(f) > 5 {
		e.Requires = f[5:]
	}
	return e, e.validate()
}

func (e Entry) validate() error {
	if !manifest.ValidExtensionID(e.ID) {
		return fmt.Errorf("invalid extension id %q", e.ID)
	}
	if !isHex64(e.SHA256) || !isHex64(e.FSVerity) {
		return fmt.Errorf("%s: sha256 and fsverity must be 64 lowercase hex digits", e.ID)
	}
	for _, r := range e.Requires {
		if !manifest.ValidExtensionID(r) || r == e.ID {
			return fmt.Errorf("%s: bad requirement %q", e.ID, r)
		}
	}
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Format writes c in the file format.
func (c *Catalog) Format() []byte {
	var b bytes.Buffer
	b.WriteString("# VaporOS extension catalog (docs/CONTRACTS.md \"Extensions\"). Written by the build.\n")
	fmt.Fprintf(&b, "dispatcher %d\n", c.Dispatcher)
	for _, e := range c.Entries {
		core := "-"
		if e.Core {
			core = "core"
		}
		fmt.Fprintf(&b, "ext %s %s %d %s %s", e.ID, e.SHA256, e.Size, e.FSVerity, core)
		for _, r := range e.Requires {
			b.WriteString(" " + r)
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// FromManifest builds the catalog of m's extensions, in dependency order
// (ties by id). m must already be validated.
func FromManifest(m *manifest.Manifest) *Catalog {
	c := &Catalog{Dispatcher: Dispatcher}
	if m == nil {
		return c
	}
	done := map[string]bool{}
	ids := make([]string, 0, len(m.Extensions))
	for id := range m.Extensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var visit func(id string)
	visit = func(id string) {
		if done[id] {
			return
		}
		done[id] = true
		x := m.Extensions[id]
		var reqs []string
		if len(x.Requires) > 0 {
			reqs = slices.Clone(x.Requires)
			sort.Strings(reqs)
		}
		for _, r := range reqs {
			visit(r)
		}
		c.Entries = append(c.Entries, Entry{
			ID: id, SHA256: x.SHA256, Size: x.Size, FSVerity: x.FSVerity, Core: x.Core, Requires: reqs,
		})
	}
	for _, id := range ids {
		visit(id)
	}
	return c
}

// Equal reports whether two catalogs list the same images.
func (c *Catalog) Equal(o *Catalog) bool {
	return bytes.Equal(c.Format(), o.Format())
}
