package truckersmp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFlagLifecycle(t *testing.T) {
	newBox(t)
	ets2, ats := games[0], games[1]
	p := flagPath()
	if p != filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "vos", "truckersmp-mp.json") {
		t.Fatalf("flag at %s", p)
	}
	t0 := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)

	// None: single-player.
	if takeFlag(p, ets2, t0) {
		t.Fatal("took a flag that is not there")
	}

	// Taken once, by the game it is for.
	f, err := writeFlag(p, ets2, t0)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 || !nonceRe.MatchString(f.Nonce) {
		t.Fatalf("flag %v %v %+v", fi, err, f)
	}
	if takeFlag(p, ats, t0.Add(time.Minute)) {
		t.Fatal("ATS took ETS2's flag")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the other game's start removed the flag")
	}
	if !takeFlag(p, ets2, t0.Add(time.Minute)) {
		t.Fatal("ETS2 did not take its flag")
	}
	if takeFlag(p, ets2, t0.Add(2*time.Minute)) {
		t.Fatal("the flag was taken twice")
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 0 {
		t.Errorf("left over: %v", ents)
	}

	// Expired: deleted, not taken.
	writeFlag(p, ets2, t0)
	if takeFlag(p, ets2, t0.Add(flagTTL+time.Second)) {
		t.Fatal("an expired flag was taken")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("an expired flag stays")
	}

	// From the future (beyond a clock's skew): not valid.
	writeFlag(p, ets2, t0.Add(time.Hour))
	if takeFlag(p, ets2, t0) {
		t.Fatal("a flag from the future was taken")
	}

	// Malformed: deleted.
	write(t, p, `{"game":"ets2","created":"2026-10-02T20:00:00Z","nonce":"short"}`)
	if takeFlag(p, ets2, t0) {
		t.Fatal("a malformed flag was taken")
	}
	write(t, p, `not json`)
	if takeFlag(p, ets2, t0) {
		t.Fatal("garbage was taken")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("garbage stays")
	}

	// dropFlag removes only the flag it names.
	f, _ = writeFlag(p, ats, t0)
	dropFlag(p, "00000000000000000000000000000000")
	if _, err := os.Stat(p); err != nil {
		t.Fatal("dropped another flag")
	}
	dropFlag(p, f.Nonce)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("the flag stays")
	}
}
