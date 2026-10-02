package steam

import (
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// Shortcut is one non-Steam game in an account's shortcuts.vdf
// (userdata/<accountid>/config/shortcuts.vdf). Exe and StartDir keep the
// double quotes Steam writes around them. The flags are Steam's int32 0/1.
type Shortcut struct {
	AppID               uint32
	AppName             string
	Exe                 string
	StartDir            string
	Icon                string
	ShortcutPath        string
	LaunchOptions       string
	IsHidden            int32
	AllowDesktopConfig  int32
	AllowOverlay        int32
	OpenVR              int32
	Devkit              int32
	DevkitGameID        string
	DevkitOverrideAppID int32
	LastPlayTime        int32
	FlatpakAppID        string
	Tags                []string
	// Extra holds the keys this package does not know, in file order.
	Extra []*BinNode

	// keys is the entry's keys as the file spelled and ordered them, so
	// writing it back keeps its bytes. Nil for a new shortcut.
	keys []string
}

// shortcutFields is Steam's order of a shortcut's keys and their types.
var shortcutFields = []struct {
	key string
	typ byte
}{
	{"appid", BinInt32}, {"AppName", BinString}, {"Exe", BinString}, {"StartDir", BinString},
	{"icon", BinString}, {"ShortcutPath", BinString}, {"LaunchOptions", BinString},
	{"IsHidden", BinInt32}, {"AllowDesktopConfig", BinInt32}, {"AllowOverlay", BinInt32},
	{"OpenVR", BinInt32}, {"Devkit", BinInt32}, {"DevkitGameID", BinString},
	{"DevkitOverrideAppID", BinInt32}, {"LastPlayTime", BinInt32}, {"FlatpakAppID", BinString},
	{"tags", BinMap},
}

// shortcutField returns the index of key in shortcutFields, or -1. Older
// Steam wrote some keys in lower case ("appname", "exe").
func shortcutField(key string) int {
	for i, f := range shortcutFields {
		if strings.EqualFold(f.key, key) {
			return i
		}
	}
	return -1
}

// NewShortcut is a shortcut with Steam's defaults for one added by hand:
// the overlay and desktop controller configuration allowed, exe and
// startDir quoted the way Steam keeps them.
func NewShortcut(appid uint32, name, exe, startDir, launchOptions string) Shortcut {
	return Shortcut{
		AppID:              appid,
		AppName:            name,
		Exe:                `"` + exe + `"`,
		StartDir:           `"` + startDir + `"`,
		LaunchOptions:      launchOptions,
		AllowDesktopConfig: 1,
		AllowOverlay:       1,
	}
}

// ParseShortcuts reads shortcuts.vdf. Empty data (Steam never wrote the
// file) is no shortcuts.
func ParseShortcuts(data []byte) ([]Shortcut, error) {
	if len(data) == 0 {
		return nil, nil
	}
	nodes, err := ParseBinaryVDF(data)
	if err != nil {
		return nil, err
	}
	if len(nodes) != 1 || nodes[0].Type != BinMap || !strings.EqualFold(nodes[0].Key, "shortcuts") {
		return nil, errors.New("shortcuts.vdf: no shortcuts map")
	}
	out := make([]Shortcut, 0, len(nodes[0].Children))
	for _, e := range nodes[0].Children {
		if e.Type != BinMap {
			return nil, fmt.Errorf("shortcuts.vdf: entry %q is not a map", e.Key)
		}
		s, err := parseShortcut(e.Children)
		if err != nil {
			return nil, fmt.Errorf("shortcuts.vdf: entry %q: %w", e.Key, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func parseShortcut(kids []*BinNode) (Shortcut, error) {
	s := Shortcut{keys: make([]string, 0, len(kids))}
	seen := make([]bool, len(shortcutFields))
	for _, n := range kids {
		s.keys = append(s.keys, n.Key)
		i := shortcutField(n.Key)
		if i < 0 || seen[i] {
			s.Extra = append(s.Extra, n)
			continue
		}
		if n.Type != shortcutFields[i].typ {
			return s, fmt.Errorf("%q has type 0x%02x", n.Key, n.Type)
		}
		seen[i] = true
		if n.Type == BinMap {
			for _, t := range n.Children {
				if t.Type != BinString {
					return s, fmt.Errorf("tag %q is not a string", t.Key)
				}
				s.Tags = append(s.Tags, t.Str)
			}
			continue
		}
		if str, num := s.field(i); str != nil {
			*str = n.Str
		} else if num != nil {
			*num = n.Int
		} else {
			s.AppID = uint32(n.Int)
		}
	}
	return s, nil
}

// field points at the string or int32 field for shortcutFields[i]; both
// are nil for appid (a uint32) and tags.
func (s *Shortcut) field(i int) (*string, *int32) {
	switch shortcutFields[i].key {
	case "AppName":
		return &s.AppName, nil
	case "Exe":
		return &s.Exe, nil
	case "StartDir":
		return &s.StartDir, nil
	case "icon":
		return &s.Icon, nil
	case "ShortcutPath":
		return &s.ShortcutPath, nil
	case "LaunchOptions":
		return &s.LaunchOptions, nil
	case "DevkitGameID":
		return &s.DevkitGameID, nil
	case "FlatpakAppID":
		return &s.FlatpakAppID, nil
	case "IsHidden":
		return nil, &s.IsHidden
	case "AllowDesktopConfig":
		return nil, &s.AllowDesktopConfig
	case "AllowOverlay":
		return nil, &s.AllowOverlay
	case "OpenVR":
		return nil, &s.OpenVR
	case "Devkit":
		return nil, &s.Devkit
	case "DevkitOverrideAppID":
		return nil, &s.DevkitOverrideAppID
	case "LastPlayTime":
		return nil, &s.LastPlayTime
	}
	return nil, nil
}

// node is shortcutFields[i] as an entry spelled key, and whether it holds
// its zero value.
func (s *Shortcut) node(i int, key string) (*BinNode, bool) {
	n := &BinNode{Type: shortcutFields[i].typ, Key: key}
	switch str, num := s.field(i); {
	case str != nil:
		n.Str = *str
		return n, *str == ""
	case num != nil:
		n.Int = *num
		return n, *num == 0
	case n.Type == BinInt32:
		n.Int = int32(s.AppID)
		return n, s.AppID == 0
	}
	for j, t := range s.Tags {
		n.Children = append(n.Children, &BinNode{Type: BinString, Key: strconv.Itoa(j), Str: t})
	}
	return n, len(s.Tags) == 0
}

// entries is the shortcut as map entries: the keys it was read with in
// their order, then known keys it did not have (all of them, in Steam's
// order, for a new shortcut; otherwise only those set), then the rest of
// Extra.
func (s *Shortcut) entries() []*BinNode {
	var out []*BinNode
	done := make([]bool, len(shortcutFields))
	used := make([]bool, len(s.Extra))
	extra := func(key string) {
		for j, x := range s.Extra {
			if !used[j] && x.Key == key {
				used[j] = true
				out = append(out, x)
				return
			}
		}
	}
	for _, k := range s.keys {
		if i := shortcutField(k); i >= 0 && !done[i] {
			done[i] = true
			n, _ := s.node(i, k)
			out = append(out, n)
			continue
		}
		extra(k)
	}
	for i, f := range shortcutFields {
		if n, zero := s.node(i, f.key); !done[i] && (s.keys == nil || !zero) {
			out = append(out, n)
		}
	}
	for j, x := range s.Extra {
		if !used[j] {
			out = append(out, x)
		}
	}
	return out
}

// MarshalShortcuts writes shortcuts.vdf the way Steam does: one
// "shortcuts" map with the entries numbered from "0".
func MarshalShortcuts(list []Shortcut) ([]byte, error) {
	top := &BinNode{Type: BinMap, Key: "shortcuts"}
	for i := range list {
		top.Children = append(top.Children, &BinNode{Type: BinMap, Key: strconv.Itoa(i), Children: list[i].entries()})
	}
	return MarshalBinaryVDF([]*BinNode{top})
}

// ShortcutAppID is the app id VaporOS gives an extension's shortcut: the
// CRC-32 of "<owner>/<key>" with the high bit set, as Steam's own ids for
// shortcuts have it. Steam may keep another; ShortcutRef finds the entry.
func ShortcutAppID(owner, key string) uint32 {
	return crc32.ChecksumIEEE([]byte(owner+"/"+key)) | 0x80000000
}

// GameID is a shortcut's 64-bit game id, the one steam://rungameid/ takes.
func GameID(appid uint32) uint64 {
	return uint64(appid)<<32 | 0x02000000
}

// RunGameURL is the steam:// URL that starts a shortcut.
func RunGameURL(appid uint32) string {
	return "steam://rungameid/" + strconv.FormatUint(GameID(appid), 10)
}
