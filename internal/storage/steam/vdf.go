// Package steam reads the few Steam client files VaporOS cares about:
// libraryfolders.vdf (where the game libraries are), appmanifest_*.acf
// (which games are installed) and the on-disk markers of a library.
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
}

// Child returns the first child whose key matches (case-insensitively).
func (n *Node) Child(key string) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if strings.EqualFold(c.Key, key) {
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

// Limits that keep a corrupt or hostile file from costing more than a
// real one ever would. Real manifests are a few KiB and nest 3-4 deep.
const (
	maxVDFSize  = 4 << 20
	maxVDFDepth = 64
)

// ParseVDF parses Valve KeyValues text. It returns a synthetic root block
// whose children are the top-level entries.
func ParseVDF(data []byte) (*Node, error) {
	if len(data) > maxVDFSize {
		return nil, fmt.Errorf("vdf: file too large (%d bytes)", len(data))
	}
	p := &vdfParser{s: string(data)}
	root := &Node{Block: true}
	if err := p.block(root, 0, false); err != nil {
		return nil, err
	}
	return root, nil
}

type vdfParser struct {
	s   string
	pos int
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
func (p *vdfParser) block(parent *Node, depth int, nested bool) error {
	if depth > maxVDFDepth {
		return errors.New("vdf: nesting too deep")
	}
	for {
		kind, key, err := p.next()
		if err != nil {
			return err
		}
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
		switch kind {
		case tokEOF:
			return errUnexpectedEOF
		case tokClose:
			return fmt.Errorf("vdf: key %q has no value", key)
		case tokOpen:
			child := &Node{Key: key, Block: true}
			if err := p.block(child, depth+1, true); err != nil {
				return err
			}
			parent.Children = append(parent.Children, child)
		default:
			parent.Children = append(parent.Children, &Node{Key: key, Value: val})
		}
		p.skipConditional()
	}
}

// skipConditional drops a platform conditional such as [$WIN32] that may
// follow a value or block. VaporOS is one platform, so they carry nothing.
func (p *vdfParser) skipConditional() {
	save := p.pos
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == '[' {
		if end := strings.IndexByte(p.s[p.pos:], ']'); end >= 0 {
			p.pos += end + 1
			return
		}
	}
	p.pos = save
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
		for p.pos < len(p.s) {
			c := p.s[p.pos]
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '{' || c == '}' || c == '"' {
				break
			}
			p.pos++
		}
		return tokString, p.s[start:p.pos], nil
	}
}

// quoted reads a "..." token. Steam writes \\ for a backslash and \" for a
// quote; \n and \t also occur in user-visible strings.
func (p *vdfParser) quoted() (string, error) {
	p.pos++ // opening quote
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
