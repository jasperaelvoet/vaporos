package steam

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Size caps for the edits below. localconfig.vdf keeps a block for every
// app an account has run and outgrows the 4 MiB the other files never reach.
const (
	VDFMax         = maxVDFSize
	LocalConfigMax = 64 << 20
)

// The edits work like Steam's own KeyValues lookups on Linux: keys match
// case-insensitively, where a key repeats the first one counts, and an
// entry whose conditional is false on Linux ([$WIN32]) is not there. A
// conditional stays with its entry. Every edit splices the parsed text,
// so all bytes outside the changed entries are kept. It refuses a file
// that does not parse (or is larger than limit) instead of guessing, and
// a result that would not parse with the same limit. path names the
// blocks from the top level down and must not be empty.

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
// block at path, every copy of it Steam on Linux reads, and whether there
// was one. A line left empty by the removal goes too.
func DeleteVDF(data []byte, limit int, path []string, key string) ([]byte, bool, error) {
	e, err := newEditor(data, limit)
	if err != nil {
		return nil, false, err
	}
	if err := e.del(path, key); err != nil {
		return nil, false, err
	}
	return e.result()
}

var errEmptyPath = errors.New("vdf: empty path")

// setVDF sets several keys of one block in a single pass, in order.
func setVDF(data []byte, limit int, path []string, pairs [][2]string) ([]byte, bool, error) {
	e, err := newEditor(data, limit)
	if err != nil {
		return nil, false, err
	}
	for _, kv := range pairs {
		if err := e.set(path, kv[0], kv[1]); err != nil {
			return nil, false, err
		}
	}
	return e.result()
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

// vdfEditor collects edits to one parsed file and makes them in one
// pass, so a file is parsed once however many entries change in it.
type vdfEditor struct {
	data  []byte
	limit int
	root  *Node
	vals  map[*Node]string // new values of entries that are there
	cuts  []*Node          // entries to remove
	gone  map[*Node]bool   // removed, and every entry in them
	adds  []*newBlock      // entries to add, by the block they go into
}

// newBlock is entries to add: to a block that is there (at), or to one
// that is added itself.
type newBlock struct {
	at      *Node
	depth   int // indentation of its entries
	entries []*newEntry
}

type newEntry struct {
	key, value string
	block      *newBlock // a block, not a value
}

func newEditor(data []byte, limit int) (*vdfEditor, error) {
	root, err := parseVDF(data, limit)
	if err != nil {
		return nil, err
	}
	return &vdfEditor{data: data, limit: limit, root: root, vals: map[*Node]string{}, gone: map[*Node]bool{}}, nil
}

// child is n's first child named key that Steam reads and that is still
// there.
func (e *vdfEditor) child(n *Node, key string) *Node {
	for _, c := range n.Children {
		if !e.gone[c] && c.applies() && strings.EqualFold(c.Key, key) {
			return c
		}
	}
	return nil
}

func (e *vdfEditor) walk(path []string) (*Node, int) {
	n := e.root
	for i, k := range path {
		c := e.child(n, k)
		if c == nil || !c.Block {
			return n, i
		}
		n = c
	}
	return n, len(path)
}

func checkPath(path []string, more ...string) error {
	if len(path) == 0 {
		return errEmptyPath
	}
	for _, s := range slices.Concat(path, more) {
		if strings.IndexByte(s, 0) >= 0 {
			return fmt.Errorf("vdf: %q holds a NUL byte", s)
		}
	}
	return nil
}

func (e *vdfEditor) set(path []string, key, value string) error {
	if err := checkPath(path, key, value); err != nil {
		return err
	}
	n, matched := e.walk(path)
	if matched == 0 {
		return fmt.Errorf("vdf: no %q block", path[0])
	}
	if matched < len(path) && e.child(n, path[matched]) != nil {
		return fmt.Errorf("vdf: %q is not a block", strings.Join(path[:matched+1], "/"))
	}
	if matched == len(path) {
		if c := e.child(n, key); c != nil {
			if c.Block {
				return fmt.Errorf("vdf: %q is a block", strings.Join(path, "/")+"/"+key)
			}
			if value == c.Value {
				delete(e.vals, c)
			} else {
				e.vals[c] = value
			}
			return nil
		}
	}
	b := e.addsTo(n, matched)
	for _, k := range path[matched:] {
		b = b.block(k)
	}
	b.put(key, value)
	return nil
}

func (e *vdfEditor) del(path []string, key string) error {
	if err := checkPath(path, key); err != nil {
		return err
	}
	n, matched := e.walk(path)
	if matched < len(path) {
		return nil
	}
	for _, c := range n.Children {
		if !e.gone[c] && c.applies() && strings.EqualFold(c.Key, key) {
			e.remove(c)
		}
	}
	for _, b := range e.adds {
		if b.at == n {
			b.entries = slices.DeleteFunc(b.entries, func(x *newEntry) bool { return strings.EqualFold(x.key, key) })
		}
	}
	return nil
}

// dropEmpty removes the block at path when nothing is left in it, an
// entry Steam does not read on Linux included.
func (e *vdfEditor) dropEmpty(path []string) {
	n, matched := e.walk(path)
	if len(path) == 0 || matched < len(path) {
		return
	}
	for _, c := range n.Children {
		if !e.gone[c] {
			return
		}
	}
	for _, b := range e.adds {
		if b.at == n && len(b.entries) > 0 {
			return
		}
	}
	e.remove(n)
}

// remove cuts entry c, which takes the place of the edits inside it.
func (e *vdfEditor) remove(c *Node) {
	inside := func(x *Node) bool { return x.start >= c.start && x.end <= c.end }
	e.gone[c] = true
	e.cuts = append(slices.DeleteFunc(e.cuts, inside), c)
	for x := range e.vals {
		if inside(x) {
			delete(e.vals, x)
		}
	}
	e.adds = slices.DeleteFunc(e.adds, func(b *newBlock) bool { return inside(b.at) })
}

func (e *vdfEditor) addsTo(n *Node, depth int) *newBlock {
	for _, b := range e.adds {
		if b.at == n {
			return b
		}
	}
	b := &newBlock{at: n, depth: depth}
	e.adds = append(e.adds, b)
	return b
}

func (b *newBlock) block(key string) *newBlock {
	for _, x := range b.entries {
		if x.block != nil && strings.EqualFold(x.key, key) {
			return x.block
		}
	}
	x := &newEntry{key: key, block: &newBlock{depth: b.depth + 1}}
	b.entries = append(b.entries, x)
	return x.block
}

func (b *newBlock) put(key, value string) {
	for _, x := range b.entries {
		if x.block == nil && strings.EqualFold(x.key, key) {
			x.value = value
			return
		}
	}
	b.entries = append(b.entries, &newEntry{key: key, value: value})
}

func (b *newBlock) render(w *strings.Builder, nl string) {
	ind := strings.Repeat("\t", b.depth)
	for _, x := range b.entries {
		if x.block == nil {
			w.WriteString(ind + vdfQuote(x.key) + "\t\t" + vdfQuote(x.value) + nl)
			continue
		}
		w.WriteString(ind + vdfQuote(x.key) + nl + ind + "{" + nl)
		x.block.render(w, nl)
		w.WriteString(ind + "}" + nl)
	}
}

// result makes the edits: the new file and whether it differs.
func (e *vdfEditor) result() ([]byte, bool, error) {
	nl := "\n"
	if bytes.Contains(e.data, []byte("\r\n")) {
		nl = "\r\n"
	}
	var edits []splice
	for c, v := range e.vals {
		edits = append(edits, splice{c.valStart, c.valEnd, vdfQuote(v)})
	}
	for _, c := range e.cuts {
		edits = append(edits, removal(e.data, c))
	}
	for _, b := range e.adds {
		if len(b.entries) > 0 {
			var text strings.Builder
			b.render(&text, nl)
			edits = append(edits, appendTo(e.data, b.at, text.String(), nl))
		}
	}
	if len(edits) == 0 {
		return e.data, false, nil
	}
	out := applySplices(e.data, edits)
	if err := checkVDF(out, e.limit); err != nil {
		return nil, false, fmt.Errorf("the edited file would not parse: %w", err)
	}
	return out, true, nil
}

// splice replaces data[at:end] with text.
type splice struct {
	at, end int
	text    string
}

// applySplices applies edits that do not overlap, except removals that
// may share their edge whitespace. A removal that would join the tokens
// on either side of it into one leaves a space between them.
func applySplices(data []byte, edits []splice) []byte {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].at < edits[j].at })
	grow := 0
	for _, e := range edits {
		grow += len(e.text) + 1
	}
	out := make([]byte, 0, len(data)+grow)
	pos := 0
	for _, e := range edits {
		at := max(e.at, pos)
		out = append(append(out, data[pos:at]...), e.text...)
		pos = max(e.end, at)
		if e.text == "" && len(out) > 0 && pos < len(data) && unquotedByte(out[len(out)-1]) && unquotedByte(data[pos]) {
			out = append(out, ' ')
		}
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
