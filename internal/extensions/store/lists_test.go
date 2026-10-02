package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestWanted(t *testing.T) {
	setup(t)
	w, err := Wanted()
	check(t, err)
	eq(t, "empty", len(w), 0)

	check(t, WriteWanted([]string{"truckersmp", "coolercontrol", "truckersmp"}))
	w, err = Wanted()
	check(t, err)
	eq(t, "wanted", strs(w), strs([]string{"coolercontrol", "truckersmp"}))
	b, err := os.ReadFile(config.ExtWantedPath())
	check(t, err)
	eq(t, "file", string(b), "coolercontrol\ntruckersmp\n")

	if err := WriteWanted([]string{"ok", "../x"}); err == nil {
		t.Error("WriteWanted took an invalid id")
	}
	writeFile(t, config.ExtWantedPath(), "proton\n\n../etc\n# c\nPROTON\nproton\n")
	w, err = Wanted()
	check(t, err)
	eq(t, "defensive", strs(w), strs([]string{"proton"}))
}

func TestProven(t *testing.T) {
	setup(t)
	a, b := Pair{"proton", hex64('a')}, Pair{"proton", hex64('b')}
	check(t, AddProven([]Pair{b, a, {"bad id", hex64('c')}, {"proton", "short"}}))
	p, err := Proven()
	check(t, err)
	if len(p) != 2 || !p[a] || !p[b] {
		t.Fatalf("proven = %v", p)
	}
	data, err := os.ReadFile(config.ExtProvenPath())
	check(t, err)
	eq(t, "file", string(data), "proton "+hex64('a')+"\nproton "+hex64('b')+"\n")

	// Nothing new: the file is not rewritten.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	check(t, os.Chtimes(config.ExtProvenPath(), old, old))
	check(t, AddProven([]Pair{a}))
	fi, err := os.Stat(config.ExtProvenPath())
	check(t, err)
	eq(t, "mtime", fi.ModTime().Equal(old), true)

	writeFile(t, config.ExtProvenPath(), "proton "+hex64('a')+"\ngarbage\nx y z\n")
	p, err = Proven()
	check(t, err)
	eq(t, "defensive", len(p), 1)
}

func TestFailed(t *testing.T) {
	setup(t)
	fp1, fp2 := hex64('1'), hex64('2')
	check(t, AddFailed(fp2))
	check(t, AddFailed(fp1))
	check(t, AddFailed(fp1))
	f, err := Failed()
	check(t, err)
	if len(f) != 2 || !f[fp1] || !f[fp2] {
		t.Fatalf("failed = %v", f)
	}
	check(t, RemoveFailed(fp2))
	check(t, RemoveFailed(fp2))
	f, err = Failed()
	check(t, err)
	if len(f) != 1 || !f[fp1] {
		t.Fatalf("failed = %v", f)
	}
	if err := AddFailed("nope"); err == nil {
		t.Error("AddFailed took a bad fingerprint")
	}
}

func TestListFilesAreBounded(t *testing.T) {
	setup(t)
	long := strings.Repeat("a", maxLine+10)
	writeFile(t, config.ExtWantedPath(), "proton\n"+long+"\ncoolercontrol\n"+long)
	w, err := Wanted()
	check(t, err)
	eq(t, "overlong lines skipped", strs(w), strs([]string{"coolercontrol", "proton"}))

	// At the limit the file is read whole, the last line included.
	line := hex64('1') + "\n"
	body := strings.Repeat("#"+strings.Repeat(" ", len(line)-2)+"\n", maxListFile/len(line)-1) + line
	body = strings.Repeat(" ", maxListFile-len(body)) + body
	writeFile(t, config.ExtFailedPath(), body)
	f, err := Failed()
	check(t, err)
	eq(t, "last line at the limit", f[hex64('1')], true)

	writeFile(t, config.ExtFailedPath(), body+"\n")
	if _, err := Failed(); !errors.Is(err, ErrListTooBig) {
		t.Errorf("Failed on a file over the limit = %v, want ErrListTooBig", err)
	}
	if err := AddFailed(hex64('2')); !errors.Is(err, ErrListTooBig) {
		t.Errorf("AddFailed rewrote a file it could not read whole: %v", err)
	}

	check(t, os.MkdirAll(config.ExtProvenPath(), 0o755))
	if _, err := Proven(); err == nil {
		t.Error("Proven read a directory")
	}
}

func TestFingerprint(t *testing.T) {
	a, b := Pair{"proton", hex64('a')}, Pair{"coolercontrol", hex64('b')}
	o1, o2 := "options amdgpu ppfeaturemask=0x4000", "options it87 ignore_resource_conflict=1"
	base := Fingerprint([]Pair{a, b}, []string{o1, o2})

	same := [][2]any{
		{[]Pair{b, a}, []string{o2, o1}},
		{[]Pair{a, b, a}, []string{o1, o2, o1}},
		{[]Pair{a, b}, []string{"options  amdgpu   ppfeaturemask=0x4000 ", o2, "garbage"}},
		{[]Pair{a, b, {"Bad", hex64('c')}}, []string{o1, o2}},
	}
	for i, c := range same {
		eq(t, "same set "+string(rune('0'+i)), Fingerprint(c[0].([]Pair), c[1].([]string)), base)
	}
	differ := [][2]any{
		{[]Pair{a}, []string{o1, o2}},
		{[]Pair{a, {"coolercontrol", hex64('c')}}, []string{o1, o2}},
		{[]Pair{a, b}, []string{o1}},
		{[]Pair{a, b}, []string{o1, "options it87 ignore_resource_conflict=0"}},
	}
	for i, c := range differ {
		if Fingerprint(c[0].([]Pair), c[1].([]string)) == base {
			t.Errorf("different set %d has the same fingerprint", i)
		}
	}

	// The documented format: sorted pair lines, then sorted option lines.
	sum := sha256.Sum256([]byte("coolercontrol " + hex64('b') + "\nproton " + hex64('a') + "\n" + o1 + "\n" + o2 + "\n"))
	eq(t, "format", base, hex.EncodeToString(sum[:]))
	empty := sha256.Sum256(nil)
	eq(t, "empty", Fingerprint(nil, nil), hex.EncodeToString(empty[:]))
}
