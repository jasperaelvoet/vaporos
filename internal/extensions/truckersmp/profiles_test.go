package truckersmp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestCopyProfiles(t *testing.T) {
	b := newBox(t)
	ets2, ats := games[0], games[1]
	native := func(g game, name, file string) string {
		return filepath.Join(config.GamerHome, nativeDocsRel(g), "profiles", name, file)
	}
	write(t, native(ets2, "4A6F", "profile.sii"), "local")
	write(t, native(ets2, "4A6F", "save/autosave/game.sii"), "save")
	write(t, native(ets2, "426F62", "profile.sii"), "already there")
	write(t, native(ats, "416C", "profile.sii"), "ats")
	os.Symlink("/etc/shadow", native(ets2, "4A6F", "link.sii"))

	// No game installed yet.
	r := copyProfiles(config.GamerHome, libraries())
	if err := r.err(); err == nil || !strings.Contains(err.Error(), "isn't installed") || len(r.copied) != 0 {
		t.Fatalf("%+v %v", r, err)
	}

	// ETS2 on the disk, run once with Proton; ATS installed but never run.
	b.install(b.disk, ets2)
	b.install(b.steam, ats)
	mkdir(t, filepath.Join(b.disk, prefixUserRel(ets2)))
	prefix := filepath.Join(b.disk, prefixDocsRel(ets2), "profiles")
	write(t, filepath.Join(prefix, "426F62", "profile.sii"), "the prefix's own")
	r = copyProfiles(config.GamerHome, libraries())
	if !slices.Equal(r.copied, []string{"ETS2: 4A6F"}) {
		t.Errorf("copied %q", r.copied)
	}
	if err := r.err(); err == nil || !strings.Contains(err.Error(), "ATS hasn't started with Proton yet") {
		t.Errorf("err %v", err)
	}
	if read(t, filepath.Join(prefix, "4A6F", "save", "autosave", "game.sii")) != "save" ||
		read(t, filepath.Join(prefix, "426F62", "profile.sii")) != "the prefix's own" {
		t.Error("profiles copied wrongly")
	}
	if _, err := os.Lstat(filepath.Join(prefix, "4A6F", "link.sii")); err == nil {
		t.Error("a symbolic link was copied")
	}
	if ents, _ := os.ReadDir(prefix); len(ents) != 2 {
		t.Errorf("prefix holds %v", ents)
	}

	// Once ATS ran: its profile follows; ETS2's are there already.
	mkdir(t, filepath.Join(b.steam, prefixUserRel(ats)))
	r = copyProfiles(config.GamerHome, libraries())
	if !slices.Equal(r.copied, []string{"ATS: 416C"}) || r.err() != nil {
		t.Errorf("%+v %v", r, r.err())
	}

	// Nothing to copy at all.
	for _, g := range games {
		os.RemoveAll(filepath.Join(config.GamerHome, nativeDocsRel(g)))
	}
	if err := copyProfiles(config.GamerHome, libraries()).err(); err == nil || !strings.Contains(err.Error(), "no Linux profiles") {
		t.Errorf("err %v", err)
	}
}
