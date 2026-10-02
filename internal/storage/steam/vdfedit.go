package steam

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Size caps for the edits below. localconfig.vdf keeps a block for every
// app an account has run and outgrows the 4 MiB the other files never reach.
const (
	VDFMax         = maxVDFSize
	LocalConfigMax = 64 << 20
)

// The edits work like Steam's own KeyValues lookups: keys match
// case-insensitively, and where a key repeats the first one counts. A
// conditional such as [$WIN32] stays with its entry. Every edit splices
// the parsed text, so all bytes outside the changed entry are kept, and
// it refuses a file that does not parse (or is larger than limit) instead
// of guessing. path names the blocks from the top level down and must
// not be empty.

// GetVDF returns the string value of key in the block at path, and false
// when either is missing or the key holds a block.
func GetVDF(data []byte, limit int, path []string, key string) (string, bool, error) {
	if len(path) == 0 {
		return "", false, errEmptyPath
	}
	root, err := parseVDF(data, limit)
	if err != nil {
		return "", false, err
	}
	n, matched := walkVDF(root, path)
	if matched < len(path) {
		return "", false, nil
	}
	c := n.Child(key)
	if c == nil || c.Block {
		return "", false, nil
	}
	return c.Value, true, nil
}

// SetVDF returns data with key set to value in the block at path, and
// whether that changed anything. A key that exists has only its value
// replaced; a missing one goes at the end of its block, and missing
// blocks below the top-level one are added, all indented with tabs as
// Steam writes them. The top-level block must exist.
func SetVDF(data []byte, limit int, path []string, key, value string) ([]byte, bool, error) {
	return setVDF(data, limit, path, [][2]string{{key, value}})
}

// DeleteVDF returns data without key (a value or a whole block) in the
// block at path, every copy of it, and whether there was one. A line left
// empty by the removal goes too.
func DeleteVDF(data []byte, limit int, path []string, key string) ([]byte, bool, error) {
	if len(path) == 0 {
		return nil, false, errEmptyPath
	}
	root, err := parseVDF(data, limit)
	if err != nil {
		return nil, false, err
	}
	n, matched := walkVDF(root, path)
	if matched < len(path) {
		return data, false, nil
	}
	var cut []splice
	for _, c := range n.Children {
		if strings.EqualFold(c.Key, key) {
			cut = append(cut, removal(data, c))
		}
	}
	if len(cut) == 0 {
		return data, false, nil
	}
	return applySplices(data, cut), true, nil
}

var errEmptyPath = errors.New("vdf: empty path")

// setVDF sets several keys of one block in a single pass, in order.
func setVDF(data []byte, limit int, path []string, pairs [][2]string) ([]byte, bool, error) {
	if len(path) == 0 {
		return nil, false, errEmptyPath
	}
	for _, kv := range pairs {
		if strings.IndexByte(kv[0], 0) >= 0 || strings.IndexByte(kv[1], 0) >= 0 {
			return nil, false, fmt.Errorf("vdf: %q holds a NUL byte", kv[0])
		}
	}
	root, err := parseVDF(data, limit)
	if err != nil {
		return nil, false, err
	}
	n, matched := walkVDF(root, path)
	if matched == 0 {
		return nil, false, fmt.Errorf("vdf: no %q block", path[0])
	}
	if matched < len(path) && n.Child(path[matched]) != nil {
		return nil, false, fmt.Errorf("vdf: %q is not a block", strings.Join(path[:matched+1], "/"))
	}

	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
	}
	var add strings.Builder
	line := func(depth int, s string) { add.WriteString(strings.Repeat("\t", depth) + s + nl) }
	var edits []splice
	for i := matched; i < len(path); i++ {
		line(i, vdfQuote(path[i]))
		line(i, "{")
	}
	for _, kv := range pairs {
		c := n.Child(kv[0])
		if matched < len(path) {
			c = nil // a block of our own, still empty
		}
		switch {
		case c == nil:
			line(len(path), vdfQuote(kv[0])+"\t\t"+vdfQuote(kv[1]))
		case c.Block:
			return nil, false, fmt.Errorf("vdf: %q is a block", strings.Join(path, "/")+"/"+kv[0])
		case c.Value != kv[1]:
			edits = append(edits, splice{c.valStart, c.valEnd, vdfQuote(kv[1])})
		}
	}
	for i := len(path) - 1; i >= matched; i-- {
		line(i, "}")
	}
	if add.Len() > 0 {
		edits = append(edits, appendTo(data, n, add.String(), nl))
	}
	if len(edits) == 0 {
		return data, false, nil
	}
	return applySplices(data, edits), true, nil
}

// walkVDF follows path from root as far as it goes through blocks: the
// deepest block reached and how many elements of path that took.
func walkVDF(root *Node, path []string) (*Node, int) {
	n := root
	for i, k := range path {
		c := n.Child(k)
		if c == nil || !c.Block {
			return n, i
		}
		n = c
	}
	return n, len(path)
}

// splice replaces data[at:end] with text.
type splice struct {
	at, end int
	text    string
}

// applySplices applies edits that do not overlap, except removals that
// may share their edge whitespace.
func applySplices(data []byte, edits []splice) []byte {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].at < edits[j].at })
	grow := 0
	for _, e := range edits {
		grow += len(e.text)
	}
	out := make([]byte, 0, len(data)+grow)
	pos := 0
	for _, e := range edits {
		at := max(e.at, pos)
		out = append(append(out, data[pos:at]...), e.text...)
		pos = max(e.end, at)
	}
	return append(out, data[pos:]...)
}

// appendTo inserts text, whole lines, as the last entries of block n.
func appendTo(data []byte, n *Node, text, nl string) splice {
	closeAt := n.valEnd
	ls := bytes.LastIndexByte(data[:closeAt], '\n') + 1
	if blank(data[ls:closeAt]) {
		// The closing brace is on a line of its own: the entries go on
		// the lines just before it.
		return splice{ls, ls, text}
	}
	return splice{closeAt, closeAt, nl + text}
}

// removal is the edit that takes entry c out of data, with its line when
// nothing else is on it.
func removal(data []byte, c *Node) splice {
	ls := bytes.LastIndexByte(data[:c.start], '\n') + 1
	le := c.end
	for le < len(data) && (data[le] == ' ' || data[le] == '\t' || data[le] == '\r') {
		le++
	}
	lineEnds := le == len(data) || data[le] == '\n'
	switch {
	case blank(data[ls:c.start]) && lineEnds:
		if le < len(data) {
			le++
		}
		return splice{ls, le, ""}
	case lineEnds:
		s := c.start
		for s > ls && (data[s-1] == ' ' || data[s-1] == '\t') {
			s--
		}
		return splice{s, c.end, ""}
	default:
		e := c.end
		for e < len(data) && (data[e] == ' ' || data[e] == '\t') {
			e++
		}
		return splice{c.start, e, ""}
	}
}

func blank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}
