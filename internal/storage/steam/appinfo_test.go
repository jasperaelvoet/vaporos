package steam

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// kvs is an entry's KeyValues for the tests: keys and values in turn,
// a value being a string, an int32, a uint64 or kvs.
type kvs []any

// appInfoFile writes apps (id, then its kvs, in turn) as appinfo.vdf of
// version 28 or 29.
func appInfoFile(t *testing.T, version int, apps ...any) string {
	t.Helper()
	var keys []string
	index := map[string]uint32{}
	var body bytes.Buffer
	le := binary.LittleEndian
	var writeKey func(b *bytes.Buffer, k string)
	writeKey = func(b *bytes.Buffer, k string) {
		if version == 28 {
			b.WriteString(k)
			b.WriteByte(0)
			return
		}
		i, ok := index[k]
		if !ok {
			i = uint32(len(keys))
			index[k] = i
			keys = append(keys, k)
		}
		b.Write(le.AppendUint32(nil, i))
	}
	var writeKV func(b *bytes.Buffer, m kvs)
	writeKV = func(b *bytes.Buffer, m kvs) {
		for i := 0; i < len(m); i += 2 {
			k := m[i].(string)
			switch v := m[i+1].(type) {
			case string:
				b.WriteByte(0x01)
				writeKey(b, k)
				b.WriteString(v)
				b.WriteByte(0)
			case int32:
				b.WriteByte(0x02)
				writeKey(b, k)
				b.Write(le.AppendUint32(nil, uint32(v)))
			case uint64:
				b.WriteByte(0x07)
				writeKey(b, k)
				b.Write(le.AppendUint64(nil, v))
			case kvs:
				b.WriteByte(0x00)
				writeKey(b, k)
				writeKV(b, v)
			}
		}
		b.WriteByte(0x08)
	}
	for i := 0; i < len(apps); i += 2 {
		var kv bytes.Buffer
		writeKV(&kv, apps[i+1].(kvs))
		body.Write(le.AppendUint32(nil, apps[i].(uint32)))
		body.Write(le.AppendUint32(nil, uint32(appInfoEntryHead+kv.Len())))
		body.Write(make([]byte, appInfoEntryHead))
		body.Write(kv.Bytes())
	}
	body.Write(make([]byte, 4))

	var out bytes.Buffer
	out.Write(le.AppendUint32(nil, uint32(0x07564400+version/10*16+version%10)))
	out.Write(le.AppendUint32(nil, 1))
	if version == 29 {
		out.Write(le.AppendUint64(nil, uint64(16+body.Len())))
	}
	out.Write(body.Bytes())
	if version == 29 {
		out.Write(le.AppendUint32(nil, uint32(len(keys))))
		for _, k := range keys {
			out.WriteString(k)
			out.WriteByte(0)
		}
	}
	path := filepath.Join(t.TempDir(), "appinfo.vdf")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func deckApp(name, runtime string) kvs {
	return kvs{"appinfo", kvs{
		"appid", int32(1),
		"common", kvs{"name", name, "type", "Game", "oslist", "windows",
			"steam_deck_compatibility", kvs{"configuration", kvs{"recommended_runtime", runtime}}},
		"extended", kvs{"gamedir", name},
	}}
}

func TestReadAppInfo(t *testing.T) {
	for _, version := range []int{28, 29} {
		path := appInfoFile(t, version,
			uint32(1091500), deckApp("Cyberpunk 2077", "proton-experimental"),
			uint32(570), kvs{"appinfo", kvs{"common", kvs{"name", "Dota 2", "type", "game"}}},
			uint32(891390), kvs{"appinfo", kvs{"extended", kvs{"app_mappings", kvs{
				"0", kvs{"appid", int32(570), "tool", "SteamLinuxRuntime_sniper"},
				"1", kvs{"appid", "39210", "tool", "proton-11.0-beta", "token", uint64(1) << 40},
			}}}},
		)
		got, err := ReadAppInfo(path, func(app uint32) bool { return app != 570 })
		if err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
		if len(got) != 2 || got[570] != nil {
			t.Fatalf("v%d: apps %v", version, got)
		}
		cp := got[1091500]
		if rr := cp.Get("AppInfo", "common", "steam_deck_compatibility", "configuration", "Recommended_Runtime").Text(); rr != "proton-experimental" {
			t.Errorf("v%d: recommended runtime %q", version, rr)
		}
		if cp.Get("appinfo", "common", "nope", "configuration") != nil || cp.Get("appinfo", "common").Text() != "" {
			t.Errorf("v%d: a missing key or a map read as something", version)
		}
		var ids []uint32
		for _, m := range got[891390].Get("appinfo", "extended", "app_mappings").Children {
			id, ok := m.Get("appid").Uint32()
			if !ok {
				t.Errorf("v%d: appid of %q", version, m.Key)
			}
			ids = append(ids, id)
		}
		if len(ids) != 2 || ids[0] != 570 || ids[1] != 39210 {
			t.Errorf("v%d: mapped apps %v", version, ids)
		}
		if n, ok := got[891390].Get("appinfo", "extended", "app_mappings", "1", "token").Uint32(); ok || n != 0 {
			t.Errorf("v%d: a 64-bit number read as 32 bits", version)
		}
	}
}

func TestReadAppInfoBroken(t *testing.T) {
	good := appInfoFile(t, 29, uint32(10), deckApp("A", "native"))
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	all := func(uint32) bool { return true }
	write := func(b []byte) string {
		p := filepath.Join(t.TempDir(), "appinfo.vdf")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, b := range map[string][]byte{
		"empty":       {},
		"version 27":  append([]byte{0x27, 0x44, 0x56, 0x07}, data[4:]...),
		"cut short":   data[:40],
		"no key list": append(append([]byte{}, data[:8]...), binary.LittleEndian.AppendUint64(nil, uint64(len(data)+5))...),
	} {
		if _, err := ReadAppInfo(write(b), all); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := ReadAppInfo(filepath.Join(t.TempDir(), "none"), all); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}

	// An entry whose KeyValues do not parse is left out, the rest read.
	bad := appInfoFile(t, 28, uint32(10), kvs{"appinfo", kvs{"common", kvs{"name", "A"}}}, uint32(20), deckApp("B", "native"))
	raw, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(raw, []byte("common"))
	raw[i-1] = 0x05 // a wide string, which nothing in appinfo uses
	got, err := ReadAppInfo(write(raw), all)
	if err != nil || len(got) != 1 || got[20] == nil {
		t.Errorf("bad entry: %v %v", got, err)
	}
}
