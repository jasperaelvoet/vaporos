package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

func TestLoadBootReport(t *testing.T) {
	setup(t)
	rep, err := LoadBootReport()
	check(t, err)
	eq(t, "mode", rep.Mode, ModeOff)
	eq(t, "reason", rep.Reason, ReasonNoReport)
	eq(t, "trial", rep.IsTrial(), false)

	writeReport(t, `{"mode":"pending","set":"3","tries_left":1,"reason":"",
		"mounted":[{"id":"proton","sha256":"`+hex64('1')+`","fsverity":"`+hex64('2')+`"},
		           {"id":"bad","sha256":"x","fsverity":"y"}],
		"skipped":[{"id":"coolercontrol","reason":"not-in-catalog"}],
		"future":true}`)
	rep, err = LoadBootReport()
	check(t, err)
	eq(t, "trial", rep.IsTrial(), true)
	eq(t, "set", rep.Set, "3")
	eq(t, "tries", rep.TriesLeft, 1)
	pairs := rep.MountedPairs()
	if len(pairs) != 1 || pairs[0] != (Pair{"proton", hex64('2')}) {
		t.Errorf("pairs = %v", pairs)
	}
	eq(t, "mounted", rep.IsMounted("proton"), true)
	eq(t, "skip reason", rep.SkipReason("coolercontrol"), SkipNotInCatalog)
	eq(t, "no skip reason", rep.SkipReason("proton"), "")

	writeReport(t, `{"mode":`)
	if _, err := LoadBootReport(); err == nil {
		t.Error("a broken report loaded")
	}
	var nilRep *BootReport
	eq(t, "nil trial", nilRep.IsTrial(), false)
	eq(t, "nil pairs", len(nilRep.MountedPairs()), 0)
	eq(t, "nil reason", nilRep.HasReason(ReasonCmdline), false)
}

func TestHasReason(t *testing.T) {
	rep := &BootReport{Mode: ModeOff, Reason: "cmdline  no-verity"}
	for _, tok := range []string{ReasonCmdline, ReasonNoVerity} {
		eq(t, tok, rep.HasReason(tok), true)
	}
	for _, tok := range []string{ReasonSkipOnce, "cmd", "no", "", "cmdline no-verity"} {
		eq(t, "not "+tok, rep.HasReason(tok), false)
	}
	setup(t)
	writeReport(t, `{"mode":"enabled","set":"","reason":"tries-used no-set"}`)
	rep, err := LoadBootReport()
	check(t, err)
	eq(t, "tries-used", rep.HasReason(ReasonTriesUsed), true)
	eq(t, "no-set", rep.HasReason(ReasonNoSet), true)
	eq(t, "tries-write", rep.HasReason(ReasonTriesWrite), false)
}

func TestSlots(t *testing.T) {
	setup(t)
	s, err := ReadSlot("a")
	check(t, err)
	if s != nil {
		t.Fatalf("missing slot = %+v", s)
	}
	exts := map[string]manifest.Extension{
		"truckersmp": {Name: "ext-truckersmp.raw", Size: 2, SHA256: hex64('3'), FSVerity: hex64('4'), Requires: []string{"proton"}},
		"proton":     {Name: "ext-proton.raw", Size: 1, SHA256: hex64('1'), FSVerity: hex64('2'), Core: true},
	}
	check(t, WriteSlot("b", "20261002.120000", exts))
	s, err = ReadSlot("b")
	check(t, err)
	eq(t, "version", s.Version, "20261002.120000")
	cat := s.Catalog()
	eq(t, "catalog order", strs(cat.IDs()), strs([]string{"proton", "truckersmp"}))
	eq(t, "core", strs(cat.Core()), strs([]string{"proton"}))

	check(t, WriteSlot("a", "20260901.000000", nil))
	s, err = ReadSlot("a")
	check(t, err)
	eq(t, "empty", len(s.Extensions), 0)

	if err := WriteSlot("c", "20261002.120000", nil); err == nil {
		t.Error("wrote slot c")
	}
	if err := WriteSlot("a", "../x", nil); err == nil {
		t.Error("wrote a bad version")
	}
	bad := map[string]manifest.Extension{"truckersmp": exts["truckersmp"]}
	if err := WriteSlot("a", "20261002.120000", bad); err == nil {
		t.Error("wrote a requirement that is not listed")
	}

	check(t, RemoveSlot("b"))
	check(t, RemoveSlot("b"))
	if _, err := os.Stat(filepath.Join(config.ExtSlotsDir(), "b.json")); !os.IsNotExist(err) {
		t.Errorf("slot b still there: %v", err)
	}
	writeFile(t, filepath.Join(config.ExtSlotsDir(), "b.json"), `{"version":"x","extensions":{"../x":{}}}`)
	if _, err := ReadSlot("b"); err == nil {
		t.Error("read a bad slot file")
	}
}
