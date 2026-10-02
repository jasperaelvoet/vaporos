package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

func readSet(t *testing.T, name string) *Set {
	t.Helper()
	s, err := ReadSet(name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeSet(t *testing.T, ids, options []string, tries int) *Set {
	t.Helper()
	s, err := WriteSet(ids, options, tries)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readLink(t *testing.T, path string) string {
	t.Helper()
	l, err := os.Readlink(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestWriteSetRoundTrip(t *testing.T) {
	setup(t)
	s := writeSet(t, []string{"proton", "coolercontrol", "proton"},
		[]string{"options it87  ignore_resource_conflict=1", "options amdgpu ppfeaturemask=0x4000", "options amdgpu ppfeaturemask=0x4000"}, 2)
	eq(t, "name", s.Name, "1")
	got := readSet(t, "1")
	eq(t, "ids", strs(got.IDs), strs([]string{"proton", "coolercontrol"}))
	eq(t, "options", strs(got.Options), strs([]string{"options amdgpu ppfeaturemask=0x4000", "options it87 ignore_resource_conflict=1"}))
	eq(t, "tries", got.Tries, 2)

	b, err := os.ReadFile(filepath.Join(config.ExtSetsDir(), "1", "ids"))
	check(t, err)
	eq(t, "ids file", string(b), "proton\ncoolercontrol\n")
	b, err = os.ReadFile(filepath.Join(config.ExtSetsDir(), "1", "tries"))
	check(t, err)
	eq(t, "tries file", string(b), "2\n")

	eq(t, "next name", writeSet(t, nil, nil, 0).Name, "2")
	empty := readSet(t, "2")
	eq(t, "empty ids", len(empty.IDs), 0)
}

func TestWriteSetDropsBadOptionsAndClampsTries(t *testing.T) {
	setup(t)
	s := writeSet(t, []string{"proton"}, []string{
		"options amdgpu ppfeaturemask=0xffff;reboot",
		"options amdgpu",
		"install amdgpu /bin/sh",
		"options amdgpu ppfeaturemask=$(id)",
		"options amd/gpu x=1",
		"options amdgpu x=1\noptions evil y=2",
		"options amdgpu dc=1",
	}, 15)
	eq(t, "options", strs(s.Options), strs([]string{"options amdgpu dc=1"}))
	eq(t, "tries", readSet(t, s.Name).Tries, 9)
	eq(t, "negative tries", writeSet(t, nil, nil, -3).Tries, 0)
	if _, err := WriteSet([]string{"Proton"}, nil, 2); err == nil {
		t.Error("WriteSet took an invalid id")
	}
	eq(t, "sets", strs(entries(t, config.ExtSetsDir())), strs([]string{nextSetFile, "1", "2"}))
}

func TestWriteSetIgnoresAndCleansTempDirs(t *testing.T) {
	setup(t)
	sets := config.ExtSetsDir()
	writeFile(t, filepath.Join(sets, tempPrefix+"5", "ids"), "proton\n")
	if _, err := ReadSet(tempPrefix + "5"); err == nil {
		t.Error("ReadSet read a temp dir")
	}
	s := writeSet(t, []string{"proton"}, nil, 2)
	eq(t, "name", s.Name, "1")
	eq(t, "sets", strs(entries(t, sets)), strs([]string{nextSetFile, "1"}))
}

func TestSetNamesNeverReuseLinkedOrBooted(t *testing.T) {
	setup(t)
	check(t, os.MkdirAll(config.ExtDir(), 0o755))
	check(t, os.Symlink("sets/7", config.ExtEnabledLink())) // dangling
	eq(t, "after enabled", writeSet(t, nil, nil, 0).Name, "8")
	writeReport(t, `{"mode":"enabled","set":"12"}`)
	eq(t, "after report", writeSet(t, nil, nil, 0).Name, "13")
	check(t, os.Symlink("sets/20", config.ExtPendingLink()))
	eq(t, "after pending", writeSet(t, nil, nil, 0).Name, "21")
}

func TestSetNamesNeverReuseCollected(t *testing.T) {
	setup(t)
	for range 3 {
		writeSet(t, []string{"proton"}, nil, 2)
	}
	check(t, setLink(config.ExtEnabledLink(), "1"))
	_, err := GC(nil)
	check(t, err)
	eq(t, "sets", strs(entries(t, config.ExtSetsDir())), strs([]string{nextSetFile, "1"}))
	eq(t, "after GC", writeSet(t, nil, nil, 0).Name, "4")
	b, err := os.ReadFile(filepath.Join(config.ExtSetsDir(), nextSetFile))
	check(t, err)
	eq(t, "high-water mark", string(b), "5\n")

	// A mark that is not a set number counts for nothing; the sets in
	// sets/ still do.
	writeFile(t, filepath.Join(config.ExtSetsDir(), nextSetFile), "x\n")
	eq(t, "bad mark", writeSet(t, nil, nil, 0).Name, "5")
	writeFile(t, filepath.Join(config.ExtSetsDir(), nextSetFile), "40\n")
	eq(t, "mark above every set", writeSet(t, nil, nil, 0).Name, "40")
	noTemps(t, config.ExtSetsDir())
}

func TestParseTries(t *testing.T) {
	for in, want := range map[string]int{
		"0": 0, "2": 2, "2\n": 2, " 9 \n": 9, "": 0, "x": 0, "10": 0, "-1": 0, "1 1": 0, "²": 0,
	} {
		eq(t, "parseTries("+in+")", parseTries(in), want)
	}
}

func TestReadSetDefensive(t *testing.T) {
	setup(t)
	dir := filepath.Join(config.ExtSetsDir(), "4")
	writeFile(t, filepath.Join(dir, "ids"), "proton\n../etc\n\n# note\nproton\ncoolercontrol\n")
	writeFile(t, filepath.Join(dir, "modprobe.conf"), "options amdgpu dc=1\nblacklist amdgpu\n")
	writeFile(t, filepath.Join(dir, "tries"), "two\n")
	s := readSet(t, "4")
	eq(t, "ids", strs(s.IDs), strs([]string{"proton", "coolercontrol"}))
	eq(t, "options", strs(s.Options), strs([]string{"options amdgpu dc=1"}))
	eq(t, "tries", s.Tries, 0)
	check(t, os.Remove(filepath.Join(dir, "tries")))
	eq(t, "missing tries", readSet(t, "4").Tries, 0)

	for _, bad := range []string{"", "0", "01", "../4", "a"} {
		if _, err := ReadSet(bad); err == nil {
			t.Errorf("ReadSet(%q) succeeded", bad)
		}
	}
	if _, err := ReadSet("99"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadSet(missing) = %v, want ErrNotExist", err)
	}
}

func TestProposePromote(t *testing.T) {
	setup(t)
	if p, err := Pending(); p != nil || err != nil {
		t.Fatalf("Pending = %v, %v on an empty store", p, err)
	}
	p, err := Propose([]string{"proton"}, nil)
	check(t, err)
	eq(t, "tries", p.Tries, ProposeTries)
	eq(t, "pending link", readLink(t, config.ExtPendingLink()), "sets/1")
	eq(t, "enabled link", readLink(t, config.ExtEnabledLink()), "")

	// A newer proposal swaps the link; no temp link is left behind.
	p2, err := Propose([]string{"proton", "coolercontrol"}, nil)
	check(t, err)
	eq(t, "pending link", readLink(t, config.ExtPendingLink()), "sets/2")
	noTemps(t, config.ExtDir())

	ok, err := Promote("1")
	check(t, err)
	eq(t, "promoted a set pending no longer names", ok, false)
	eq(t, "pending link", readLink(t, config.ExtPendingLink()), "sets/2")

	ok, err = Promote(p2.Name)
	check(t, err)
	eq(t, "promoted", ok, true)
	eq(t, "enabled link", readLink(t, config.ExtEnabledLink()), "sets/2")
	eq(t, "pending link", readLink(t, config.ExtPendingLink()), "")
	en, err := Enabled()
	check(t, err)
	eq(t, "enabled ids", strs(en.IDs), strs([]string{"proton", "coolercontrol"}))

	ok, err = Promote(p2.Name)
	check(t, err)
	eq(t, "promoted twice", ok, false)
	noTemps(t, config.ExtDir())
}

func TestClearPending(t *testing.T) {
	setup(t)
	check(t, ClearPending())
	_, err := Propose([]string{"proton"}, nil)
	check(t, err)
	check(t, ClearPending())
	eq(t, "pending link", readLink(t, config.ExtPendingLink()), "")
	eq(t, "set kept for GC", strs(entries(t, config.ExtSetsDir())), strs([]string{nextSetFile, "1"}))
}

func TestFailPending(t *testing.T) {
	setup(t)
	cat := &catalog.Catalog{Entries: []catalog.Entry{
		{ID: "proton", SHA256: hex64('1'), Size: 1, FSVerity: hex64('2'), Core: true},
		{ID: "coolercontrol", SHA256: hex64('3'), Size: 1, FSVerity: hex64('4')},
	}}
	check(t, FailPending(cat)) // nothing pending
	failed, err := Failed()
	check(t, err)
	eq(t, "failed", len(failed), 0)

	opts := []string{"options amdgpu dc=1"}
	_, err = Propose([]string{"proton", "coolercontrol"}, opts)
	check(t, err)
	check(t, FailPending(cat))
	eq(t, "pending link", readLink(t, config.ExtPendingLink()), "")
	failed, err = Failed()
	check(t, err)
	want := Fingerprint([]Pair{{"proton", hex64('2')}, {"coolercontrol", hex64('4')}}, opts)
	if len(failed) != 1 || !failed[want] {
		t.Errorf("failed = %v, want only %s", failed, want)
	}
}

func TestWriteEnabled(t *testing.T) {
	setup(t)
	s, err := WriteEnabled([]string{"proton"}, nil)
	check(t, err)
	eq(t, "tries", s.Tries, 0)
	eq(t, "enabled link", readLink(t, config.ExtEnabledLink()), "sets/1")
	en, err := Enabled()
	check(t, err)
	eq(t, "enabled", strs(en.IDs), strs([]string{"proton"}))
}

func TestLinksThatNameNoSet(t *testing.T) {
	setup(t)
	check(t, os.MkdirAll(config.ExtDir(), 0o755))
	for _, target := range []string{"sets/3", "../../etc", "/var/lib/vos/ext/sets/1", "sets/x"} {
		os.Remove(config.ExtEnabledLink())
		check(t, os.Symlink(target, config.ExtEnabledLink()))
		if s, err := Enabled(); s != nil || err != nil {
			t.Errorf("Enabled with link %q = %v, %v", target, s, err)
		}
	}
	os.Remove(config.ExtEnabledLink())
	writeFile(t, config.ExtEnabledLink(), "sets/1")
	if s, err := Enabled(); s != nil || err != nil {
		t.Errorf("Enabled with a regular file = %v, %v", s, err)
	}
}
