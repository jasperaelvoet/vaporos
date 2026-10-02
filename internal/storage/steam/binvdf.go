package steam

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Binary KeyValues entry types, the ones shortcuts.vdf holds. Each entry
// is its type byte, its key and a NUL, then its value: a NUL-terminated
// string, a little-endian int32, or a map's entries up to binEnd.
const (
	BinMap    byte = 0x00
	BinString byte = 0x01
	BinInt32  byte = 0x02
	binEnd    byte = 0x08
)

// BinNode is one entry of a binary KeyValues file.
type BinNode struct {
	Type     byte
	Key      string
	Str      string     // BinString
	Int      int32      // BinInt32
	Children []*BinNode // BinMap
}

// Limits for binary files. Steam's shortcuts.vdf nests three deep and
// stays far below a megabyte.
const (
	maxBinVDFSize  = 4 << 20
	maxBinVDFDepth = 16
)

// ParseBinaryVDF parses a binary KeyValues file: its top-level entries,
// which end with the end marker that closes the file. Any other type,
// a truncated entry or bytes after the end are an error.
func ParseBinaryVDF(data []byte) ([]*BinNode, error) {
	if len(data) > maxBinVDFSize {
		return nil, fmt.Errorf("binary vdf: file too large (%d bytes)", len(data))
	}
	r := &binReader{b: data}
	nodes, err := r.entries(0)
	if err != nil {
		return nil, err
	}
	if r.pos != len(data) {
		return nil, fmt.Errorf("binary vdf: %d bytes after the end", len(data)-r.pos)
	}
	return nodes, nil
}

// MarshalBinaryVDF writes nodes as a binary KeyValues file; for a file
// ParseBinaryVDF read, the same bytes.
func MarshalBinaryVDF(nodes []*BinNode) ([]byte, error) {
	var b bytes.Buffer
	if err := writeBin(&b, nodes, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

type binReader struct {
	b   []byte
	pos int
}

var errBinTruncated = errors.New("binary vdf: unexpected end of file")

func (r *binReader) entries(depth int) ([]*BinNode, error) {
	if depth > maxBinVDFDepth {
		return nil, errors.New("binary vdf: nesting too deep")
	}
	var out []*BinNode
	for {
		if r.pos >= len(r.b) {
			return nil, errBinTruncated
		}
		typ := r.b[r.pos]
		r.pos++
		if typ == binEnd {
			return out, nil
		}
		key, err := r.cstring()
		if err != nil {
			return nil, err
		}
		n := &BinNode{Type: typ, Key: key}
		switch typ {
		case BinMap:
			if n.Children, err = r.entries(depth + 1); err != nil {
				return nil, err
			}
		case BinString:
			if n.Str, err = r.cstring(); err != nil {
				return nil, err
			}
		case BinInt32:
			if r.pos+4 > len(r.b) {
				return nil, errBinTruncated
			}
			n.Int = int32(binary.LittleEndian.Uint32(r.b[r.pos:]))
			r.pos += 4
		default:
			return nil, fmt.Errorf("binary vdf: %q has type 0x%02x", key, typ)
		}
		out = append(out, n)
	}
}

func (r *binReader) cstring() (string, error) {
	end := bytes.IndexByte(r.b[r.pos:], 0)
	if end < 0 {
		return "", errBinTruncated
	}
	s := string(r.b[r.pos : r.pos+end])
	r.pos += end + 1
	return s, nil
}

func writeBin(b *bytes.Buffer, nodes []*BinNode, depth int) error {
	if depth > maxBinVDFDepth {
		return errors.New("binary vdf: nesting too deep")
	}
	for _, n := range nodes {
		if strings.IndexByte(n.Key, 0) >= 0 || strings.IndexByte(n.Str, 0) >= 0 {
			return fmt.Errorf("binary vdf: %q holds a NUL byte", n.Key)
		}
		b.WriteByte(n.Type)
		b.WriteString(n.Key)
		b.WriteByte(0)
		switch n.Type {
		case BinMap:
			if err := writeBin(b, n.Children, depth+1); err != nil {
				return err
			}
		case BinString:
			b.WriteString(n.Str)
			b.WriteByte(0)
		case BinInt32:
			b.Write(binary.LittleEndian.AppendUint32(nil, uint32(n.Int)))
		default:
			return fmt.Errorf("binary vdf: %q has type 0x%02x", n.Key, n.Type)
		}
	}
	b.WriteByte(binEnd)
	return nil
}
