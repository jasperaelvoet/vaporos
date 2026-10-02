// Package steam reads the few Steam client files VaporOS cares about:
// libraryfolders.vdf (where the game libraries are), appmanifest_*.acf
// (which games are installed) and the on-disk markers of a library.
// It also edits the ones `vos steam prepare` changes (config.vdf,
// localconfig.vdf, shortcuts.vdf, an app's BetaKey) without disturbing
// anything else in them; see "Steam" under Extensions in docs/CONTRACTS.md.
// It never talks to Steam itself, so it works while Steam is not running
// and on disks that are not mounted where Steam expects them.
package steam

import (
	"errors"
	"fmt"
	"strings"
)

// Node is one KeyValues ("VDF") entry: a string value, or a block of
// children. Valve's format is case-insensitive in its keys and allows
// repeated keys, so children are kept as an ordered list, not a map.
type Node struct {
	Key      string
	Value    string
	Children []*Node
	Block    bool

	// cond is the platform conditional after the entry ("$WIN32" for
	// [$WIN32]), "" when it has none.
	cond string
	// Byte offsets in the parsed text, for edits that splice it
	// (vdfedit.go): the key token, the value token (for a block its '{'
	// and '}'), and the end of the entry, a conditional included.
	start, valStart, valEnd, end int
}

// Child returns the first child whose key matches (case-insensitively)
// and that Steam on Linux reads: one whose conditional is false there,
// such as [$WIN32], is passed over.
func (n *Node) Child(key string) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.applies() && strings.EqualFold(c.Key, key) {
			return c
		}
	}
	return nil
}

// Str returns the string value of child key, or "" when it is missing or
// is a block.
func (n *Node) Str(key string) string {
	c := n.Child(key)
	if c == nil || c.Block {
		return ""
	}
	return c.Value
}

// applies reports whether Steam on Linux reads the entry.
func (n *Node) applies() bool { return linuxCond(n.cond) }

// linuxDefines are the platform names a conditional can test, as they
// are on Linux.
var linuxDefines = map[string]bool{
	"$WIN32": false, "$WINDOWS": false, "$OSX": false, "$X360": false, "$PS3": false,
	"$LINUX": true, "$POSIX": true,
}

// linuxCond evaluates a conditional such as $WIN32, !$LINUX or
// $WIN32||$OSX as Steam on Linux does. One that names something else is
// taken as true, as every conditional was before VaporOS read them.
func linuxCond(cond string) bool {
	if cond == "" {
		return true
	}
	result := false
	for _, alt := range strings.Split(cond, "||") {
		all := true
		for _, term := range strings.Split(alt, "&&") {
			term = strings.TrimSpace(term)
			not := strings.HasPrefix(term, "!")
			v, known := linuxDefines[strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(term, "!")))]
			if !known {
				return true
			}
			all = all && v != not
		}
		result = result || all
	}
	return result
}

// Limits that keep a corrupt or hostile file from costing more than a
// real one ever would. Real manifests are a few KiB and nest 3-4 deep.
const (
	maxVDFSize  = 4 << 20
	maxVDFDepth = 64
)

// ParseVDF parses Valve KeyValues text. It returns a synthetic root block
// whose children are the top-level entries.
func ParseVDF(data []byte) (*Node, error) {
	return parseVDF(data, maxVDFSize)
}

func parseVDF(data []byte, limit int) (*Node, error) {
	if len(data) > limit {
		return nil, fmt.Errorf("vdf: file too large (%d bytes)", len(data))
	}
	p := &vdfParser{s: string(data)}
	root := &Node{Block: true}
	if err := p.block(root, 0, false); err != nil {
		return nil, err
	}
	return root, nil
}

// checkVDF reports whether data parses, without building its tree: the
// check every edit makes of its own result.
func checkVDF(data []byte, limit int) error {
	if len(data) > limit {
		return fmt.Errorf("vdf: file too large (%d bytes)", len(data))
	}
	p := &vdfParser{s: string(data), scan: true}
	return p.block(nil, 0, false)
}

type vdfParser struct {
	s    string
	pos  int
	tok  int  // where the last token starts
	scan bool // only check the syntax
	slab []Node
}

// node allocates nodes a slab at a time: a large localconfig.vdf has
// millions, and one allocation each costs more than parsing them.
func (p *vdfParser) node() *Node {
	if len(p.slab) == 0 {
		p.slab = make([]Node, 512)
	}
	n := &p.slab[0]
	p.slab = p.slab[1:]
	return n
}

type tokKind int

const (
	tokEOF tokKind = iota
	tokString
	tokOpen
	tokClose
)

var errUnexpectedEOF = errors.New("vdf: unexpected end of file")

// block reads key/value pairs into parent until '}' (nested) or EOF (top).
// Scanning, parent is nil and nothing is kept.
func (p *vdfParser) block(parent *Node, depth int, nested bool) error {
	if depth > maxVDFDepth {
		return errors.New("vdf: nesting too deep")
	}
	for {
		kind, key, err := p.next()
		if err != nil {
			return err
		}
		start := p.tok
		switch kind {
		case tokEOF:
			if nested {
				return errUnexpectedEOF
			}
			return nil
		case tokClose:
			if !nested {
				return fmt.Errorf("vdf: unexpected '}' at offset %d", p.pos)
			}
			return nil
		case tokOpen:
			return fmt.Errorf("vdf: unexpected '{' at offset %d", p.pos)
		}

		kind, val, err := p.next()
		if err != nil {
			return err
		}
		var n *Node
		if !p.scan {
			n = p.node()
			n.Key, n.start, n.valStart = key, start, p.tok
		}
		switch kind {
		case tokEOF:
			return errUnexpectedEOF
		case tokClose:
			return fmt.Errorf("vdf: key %q has no value", key)
		case tokOpen:
			if err := p.block(n, depth+1, true); err != nil {
				return err
			}
			if n != nil {
				n.Block, n.valEnd = true, p.pos-1 // the '}'
			}
		default:
			if n != nil {
				n.Value, n.valEnd = val, p.pos
			}
		}
		cond := p.skipConditional()
		if n != nil {
			n.cond, n.end = cond, p.pos
			parent.Children = append(parent.Children, n)
		}
	}
}

// skipConditional moves past a platform conditional such as [$WIN32]
// that may follow a value or block, and returns what is in its brackets.
func (p *vdfParser) skipConditional() string {
	save := p.pos
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == '[' {
		if end := strings.IndexByte(p.s[p.pos:], ']'); end >= 0 {
			cond := p.s[p.pos+1 : p.pos+end]
			p.pos += end + 1
			return cond
		}
	}
	p.pos = save
	return ""
}

func (p *vdfParser) skipSpace() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v':
			p.pos++
		case c == '/' && p.pos+1 < len(p.s) && p.s[p.pos+1] == '/':
			if nl := strings.IndexByte(p.s[p.pos:], '\n'); nl >= 0 {
				p.pos += nl + 1
			} else {
				p.pos = len(p.s)
			}
		default:
			return
		}
	}
}

func (p *vdfParser) next() (tokKind, string, error) {
	p.skipSpace()
	p.tok = p.pos
	if p.pos >= len(p.s) {
		return tokEOF, "", nil
	}
	switch c := p.s[p.pos]; c {
	case '{':
		p.pos++
		return tokOpen, "", nil
	case '}':
		p.pos++
		return tokClose, "", nil
	case '"':
		s, err := p.quoted()
		return tokString, s, err
	default:
		start := p.pos
		for p.pos < len(p.s) && unquotedByte(p.s[p.pos]) {
			p.pos++
		}
		return tokString, p.s[start:p.pos], nil
	}
}

// unquotedByte reports whether c continues an unquoted token.
func unquotedByte(c byte) bool {
	return c != ' ' && c != '\t' && c != '\r' && c != '\n' && c != '{' && c != '}' && c != '"'
}

// quoted reads a "..." token. Steam writes \\ for a backslash and \" for a
// quote; \n and \t also occur in user-visible strings.
func (p *vdfParser) quoted() (string, error) {
	p.pos++ // opening quote
	rest := p.s[p.pos:]
	if end := strings.IndexByte(rest, '"'); end >= 0 && strings.IndexByte(rest[:end], '\\') < 0 {
		// No escapes, as in nearly every token: the text itself.
		p.pos += end + 1
		return rest[:end], nil
	}
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch c {
		case '"':
			p.pos++
			return b.String(), nil
		case '\\':
			if p.pos+1 >= len(p.s) {
				return "", errUnexpectedEOF
			}
			p.pos++
			switch e := p.s[p.pos]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
		p.pos++
	}
	return "", errUnexpectedEOF
}
