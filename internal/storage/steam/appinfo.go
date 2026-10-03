package steam

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// AppInfoPath is Steam's cache of what it knows about apps (their type,
// platforms, and Valve's tested runtime per device), which Steam fills from
// its servers and VaporOS only reads.
func AppInfoPath(root string) string { return filepath.Join(root, "appcache", "appinfo.vdf") }

// The appinfo.vdf versions read: 28 writes each key as a string, 29 (Steam
// since 2024) as an index into a table of the keys at the end of the file.
const (
	appInfoV28 = 0x07564428
	appInfoV29 = 0x07564429
)

const (
	// An entry is its app id and size, then this much before its KeyValues:
	// info state, last update, PICS token, SHA-1, change number, SHA-1.
	appInfoEntryHead = 4 + 4 + 8 + 20 + 4 + 20
	maxAppInfoEntry  = 16 << 20
	maxAppInfoKeys   = 64 << 20
	maxAppInfoDepth  = 32
)

// KV is a value in an appinfo.vdf entry: a map, a string or a number.
// Floats, colours and wide strings are kept as keys without a value.
type KV struct {
	Key      string
	Map      bool
	Children []*KV
	Str      string
	Num      int64
	IsNum    bool
}

// Get follows keys down maps as Steam looks them up (in any case, the
// first of repeated keys), and returns nil once one is missing.
func (k *KV) Get(keys ...string) *KV {
	for _, key := range keys {
		if k == nil || !k.Map {
			return nil
		}
		var next *KV
		for _, c := range k.Children {
			if strings.EqualFold(c.Key, key) {
				next = c
				break
			}
		}
		k = next
	}
	return k
}

// Text is a string value, "" for anything else.
func (k *KV) Text() string {
	if k == nil || k.Map || k.IsNum {
		return ""
	}
	return k.Str
}

// Uint32 is a number, or a string of decimal digits, that fits 32 bits.
func (k *KV) Uint32() (uint32, bool) {
	switch {
	case k == nil || k.Map:
		return 0, false
	case k.IsNum:
		return uint32(k.Num), k.Num >= 0 && k.Num <= 0xffffffff
	}
	n, err := strconv.ParseUint(k.Str, 10, 32)
	return uint32(n), err == nil
}

// ReadAppInfo returns the entries of the apps want picks from appinfo.vdf,
// each as the map that holds "appinfo". Other entries are skipped unread;
// one larger than 16 MiB, or whose KeyValues do not parse, is left out. A
// file of another version, or one cut short, is an error.
func ReadAppInfo(path string, want func(app uint32) bool) (map[uint32]*KV, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	size := fi.Size()
	var head [16]byte
	if _, err := io.ReadFull(f, head[:8]); err != nil {
		return nil, errAppInfoShort
	}
	start, end := int64(8), size
	var keys []string
	switch magic := binary.LittleEndian.Uint32(head[:]); magic {
	case appInfoV28:
	case appInfoV29:
		if _, err := io.ReadFull(f, head[8:16]); err != nil {
			return nil, errAppInfoShort
		}
		end = int64(binary.LittleEndian.Uint64(head[8:]))
		if end < 16 || end > size || size-end > maxAppInfoKeys {
			return nil, fmt.Errorf("appinfo.vdf: key table at %d of %d bytes", end, size)
		}
		if keys, err = readAppInfoKeys(f, end, size-end); err != nil {
			return nil, err
		}
		start = 16
	default:
		return nil, fmt.Errorf("appinfo.vdf: unknown version %#08x", magic)
	}

	r := bufio.NewReaderSize(io.NewSectionReader(f, start, end-start), 64<<10)
	out := map[uint32]*KV{}
	for {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:4]); err != nil {
			return nil, errAppInfoShort
		}
		app := binary.LittleEndian.Uint32(h[:4])
		if app == 0 {
			return out, nil
		}
		if _, err := io.ReadFull(r, h[4:]); err != nil {
			return nil, errAppInfoShort
		}
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		if n < appInfoEntryHead {
			return nil, fmt.Errorf("appinfo.vdf: app %d has an entry of %d bytes", app, n)
		}
		if !want(app) || n > maxAppInfoEntry {
			if _, err := r.Discard(int(n)); err != nil {
				return nil, errAppInfoShort
			}
			continue
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, errAppInfoShort
		}
		kr := &kvReader{b: buf[appInfoEntryHead:], keys: keys, indexed: keys != nil}
		if children, err := kr.entries(0); err == nil {
			out[app] = &KV{Map: true, Children: children}
		}
	}
}

var errAppInfoShort = errors.New("appinfo.vdf: unexpected end of file")

// readAppInfoKeys reads version 29's table: a count, then that many
// NUL-terminated keys.
func readAppInfoKeys(f *os.File, off, n int64) ([]string, error) {
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, errAppInfoShort
	}
	if len(buf) < 4 {
		return nil, errAppInfoShort
	}
	count := binary.LittleEndian.Uint32(buf)
	buf = buf[4:]
	if int64(count) > int64(len(buf)) {
		return nil, fmt.Errorf("appinfo.vdf: %d keys in %d bytes", count, len(buf))
	}
	keys := make([]string, 0, count)
	for range count {
		i := bytes.IndexByte(buf, 0)
		if i < 0 {
			return nil, errAppInfoShort
		}
		keys = append(keys, string(buf[:i]))
		buf = buf[i+1:]
	}
	return keys, nil
}

// kvReader parses an entry's binary KeyValues: a type byte, the key
// (version 29: its index in the table), then the value.
type kvReader struct {
	b       []byte
	pos     int
	keys    []string
	indexed bool
}

func (r *kvReader) entries(depth int) ([]*KV, error) {
	if depth > maxAppInfoDepth {
		return nil, errors.New("appinfo.vdf: nesting too deep")
	}
	var out []*KV
	for {
		if r.pos >= len(r.b) {
			return nil, errAppInfoShort
		}
		typ := r.b[r.pos]
		r.pos++
		if typ == 0x08 || typ == 0x0b { // the end of a map, and its other spelling
			return out, nil
		}
		key, err := r.key()
		if err != nil {
			return nil, err
		}
		n := &KV{Key: key}
		switch typ {
		case 0x00:
			n.Map = true
			if n.Children, err = r.entries(depth + 1); err != nil {
				return nil, err
			}
		case 0x01:
			if n.Str, err = r.cstring(); err != nil {
				return nil, err
			}
		case 0x02, 0x04: // int32, pointer
			v, err := r.fixed(4)
			if err != nil {
				return nil, err
			}
			n.Num, n.IsNum = int64(int32(binary.LittleEndian.Uint32(v))), true
		case 0x03, 0x06: // float32, colour
			if _, err := r.fixed(4); err != nil {
				return nil, err
			}
		case 0x07, 0x0a: // uint64, int64
			v, err := r.fixed(8)
			if err != nil {
				return nil, err
			}
			n.Num, n.IsNum = int64(binary.LittleEndian.Uint64(v)), true
		default:
			return nil, fmt.Errorf("appinfo.vdf: %q has type 0x%02x", key, typ)
		}
		out = append(out, n)
	}
}

func (r *kvReader) key() (string, error) {
	if !r.indexed {
		return r.cstring()
	}
	v, err := r.fixed(4)
	if err != nil {
		return "", err
	}
	i := binary.LittleEndian.Uint32(v)
	if int64(i) >= int64(len(r.keys)) {
		return "", fmt.Errorf("appinfo.vdf: key %d of %d", i, len(r.keys))
	}
	return r.keys[i], nil
}

func (r *kvReader) fixed(n int) ([]byte, error) {
	if r.pos+n > len(r.b) {
		return nil, errAppInfoShort
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v, nil
}

func (r *kvReader) cstring() (string, error) {
	end := bytes.IndexByte(r.b[r.pos:], 0)
	if end < 0 {
		return "", errAppInfoShort
	}
	s := string(r.b[r.pos : r.pos+end])
	r.pos += end + 1
	return s, nil
}
