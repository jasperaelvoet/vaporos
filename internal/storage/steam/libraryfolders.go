package steam

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// AddLibraryFolder returns libraryfolders.vdf data with dir added as one
// more library folder, and whether anything was added (false when dir is
// listed already). The entry goes at the end of the "libraryfolders"
// block, written the way Steam writes its own: the next free index, tab
// indentation and the file's own line endings. Every other byte of the
// file is kept. label and contentID are the library's own, from the
// libraryfolder.vdf in it (see ParseLibraryFolder).
//
// Steam keeps the list in memory while it runs and writes it back when it
// exits, so the caller must only do this while Steam is not running.
func AddLibraryFolder(data []byte, dir, label, contentID string) ([]byte, bool, error) {
	if !filepath.IsAbs(dir) {
		return nil, false, fmt.Errorf("library folder %q is not an absolute path", dir)
	}
	dir = filepath.Clean(dir)
	if _, err := ParseVDF(data); err != nil {
		return nil, false, err // never edit a file that does not parse
	}
	block, closeAt, err := libraryFoldersBlock(data)
	if err != nil {
		return nil, false, err
	}
	next := 0
	for _, c := range block.Children {
		n, err := strconv.Atoi(c.Key)
		if err != nil {
			continue // TimeNextStatsReport, ContentStatsID, …
		}
		p := c.Value
		if c.Block {
			p = c.Str("path")
		}
		if p = strings.TrimSpace(p); filepath.IsAbs(p) && filepath.Clean(p) == dir {
			return data, false, nil
		}
		if n >= next {
			next = n + 1
		}
	}
	if !isDigits(contentID) {
		contentID = "0"
	}

	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
	}
	var b strings.Builder
	line := func(depth int, s string) { b.WriteString(strings.Repeat("\t", depth) + s + nl) }
	pair := func(k, v string) { line(2, vdfQuote(k)+"\t\t"+vdfQuote(v)) }
	line(1, vdfQuote(strconv.Itoa(next)))
	line(1, "{")
	pair("path", dir)
	pair("label", label)
	pair("contentid", contentID)
	pair("totalsize", "0")
	pair("update_clean_bytes_tally", "0")
	pair("time_last_update_verified", "0")
	line(2, vdfQuote("apps"))
	line(2, "{")
	line(2, "}")
	line(1, "}")
	entry := b.String()

	out := make([]byte, 0, len(data)+len(entry)+len(nl))
	start := bytes.LastIndexByte(data[:closeAt], '\n') + 1
	if len(bytes.TrimSpace(data[start:closeAt])) == 0 {
		// The closing brace is on a line of its own: the entry goes on
		// the lines just before it.
		out = append(append(append(out, data[:start]...), entry...), data[start:]...)
	} else {
		out = append(append(append(append(out, data[:closeAt]...), nl...), entry...), data[closeAt:]...)
	}
	return out, true, nil
}

// ParseLibraryFolder reads the libraryfolder.vdf marker Steam keeps in
// every library: its label and content id ("" and "0" when unknown).
func ParseLibraryFolder(data []byte) (label, contentID string) {
	contentID = "0"
	root, err := ParseVDF(data)
	if err != nil {
		return "", contentID
	}
	lf := root.Child("libraryfolder")
	if lf == nil || !lf.Block {
		return "", contentID
	}
	if id := lf.Str("contentid"); isDigits(id) {
		contentID = id
	}
	return lf.Str("label"), contentID
}

// libraryFoldersBlock finds the top-level "libraryfolders" block and the
// offset of its closing brace.
func libraryFoldersBlock(data []byte) (*Node, int, error) {
	p := &vdfParser{s: string(data)}
	for {
		kind, key, err := p.next()
		if err != nil {
			return nil, 0, err
		}
		switch kind {
		case tokEOF:
			return nil, 0, errors.New("libraryfolders.vdf: no libraryfolders block")
		case tokString:
		default:
			return nil, 0, fmt.Errorf("vdf: unexpected token at offset %d", p.pos)
		}
		kind, _, err = p.next()
		if err != nil {
			return nil, 0, err
		}
		switch kind {
		case tokOpen:
			n := &Node{Key: key, Block: true}
			if err := p.block(n, 1, true); err != nil {
				return nil, 0, err
			}
			if strings.EqualFold(key, "libraryfolders") {
				return n, p.pos - 1, nil // block stops just past the '}'
			}
		case tokString:
		default:
			return nil, 0, fmt.Errorf("vdf: key %q has no value", key)
		}
		p.skipConditional()
	}
}

// vdfQuote writes s as a KeyValues string the way Steam's writer does:
// only a backslash and a quote are escaped.
func vdfQuote(s string) string {
	return `"` + vdfEscaper.Replace(s) + `"`
}

var vdfEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func isDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
