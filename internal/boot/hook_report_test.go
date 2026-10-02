package boot

import (
	"os"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// /run/vos/extensions.json is the initramfs's word to everything after it
// (docs/CONTRACTS.md "Extensions", Boot). The hook writes it in shell and the
// store reads it in Go; these tests hold the two to one vocabulary.

// The report's words, as the contract spells them; the store's constants
// must say the same.
var (
	reportModes   = []string{store.ModePending, store.ModeEnabled, store.ModeOSTrial, store.ModeOff}
	reportReasons = []string{store.ReasonCmdline, store.ReasonSkipOnce, store.ReasonNoSet, store.ReasonTriesUsed,
		store.ReasonTriesWrite, store.ReasonNoVerity}
	reportSkips = []string{store.SkipRequires, store.SkipNotInCatalog, store.SkipMissing, store.SkipSize,
		store.SkipFSVerity, store.SkipUnproven, store.SkipMount, store.SkipNoUsr, store.SkipOverlay}
)

func TestReportWords(t *testing.T) {
	for _, c := range []struct {
		got  []string
		want string
	}{
		{reportModes, "pending enabled os-trial off"},
		{reportReasons, "cmdline skip-once no-set tries-used tries-write no-verity"},
		{reportSkips, "requires not-in-catalog missing size fsverity unproven mount no-usr overlay"},
	} {
		if got := strings.Join(c.got, " "); got != c.want {
			t.Errorf("the store says %q, the contract %q", got, c.want)
		}
	}
}

// Every word the report may hold comes out of the hook somewhere, and
// bootReport checks each one it sees against the store's.
func TestHookReportVocabulary(t *testing.T) {
	base := func(h *hookEnv) {
		h.ext("a")
		h.ext("b", "a")
		h.ext("c")
	}
	enabled := func(h *hookEnv) {
		base(h)
		h.set("1", "", "", "a", "b", "c")
		h.link("enabled", "sets/1")
		h.prove("a", "b", "c")
	}
	pending := func(tries string) func(h *hookEnv) {
		return func(h *hookEnv) {
			enabled(h)
			h.set("2", tries, "", "a", "b", "c")
			h.link("pending", "sets/2")
		}
	}
	scenarios := []struct {
		vars, script string
		setup        func(h *hookEnv)
	}{
		// One image per skip reason of the mount loop: a missing, so b
		// requires; c of another size, d unproven, e of another digest, f
		// failing to mount, g without usr/, h fine, ghost not listed.
		{vars: "FAIL_IMAGE=" + shaOf("f"), setup: func(h *hookEnv) {
			base(h)
			for _, id := range []string{"d", "e", "f", "g", "h"} {
				h.ext(id)
			}
			h.set("1", "", "", "a", "b", "c", "d", "e", "f", "g", "h", "ghost")
			h.link("enabled", "sets/1")
			h.prove("a", "b", "c", "e", "f", "g", "h")
			os.Remove(h.path(storeRel, "images", shaOf("a")+".raw"))
			h.write(storeRel+"/images/"+shaOf("c")+".raw", "a longer erofs image of c")
			h.write("fsv/"+shaOf("e"), fsvOf("tampered e"))
			os.RemoveAll(h.path("trees", shaOf("g"), "usr"))
			h.write("trees/"+shaOf("g")+"/etc/g.conf", "")
		}},
		{vars: "FAIL_OVERLAY=1", setup: enabled},
		{vars: "A_ext=0", script: "vos_ext_noverity=1; ", setup: func(h *hookEnv) {
			enabled(h)
			h.write(storeRel+"/skip-once", "")
		}},
		{setup: func(h *hookEnv) {
			base(h)
			h.write(bootCountVar, "")
		}},
		{setup: pending("0")},
		{setup: pending("2"), script: `mv() { case "$2" in */tries.new) return 1 ;; esac; command mv "$@"; }; `},
		{setup: pending("2")},
	}
	seen := map[string]bool{}
	for _, s := range scenarios {
		h := newHookEnv(t)
		s.setup(h)
		h.run(s.vars, s.script+`vos_mount_extensions "$NEW"`)
		r := h.bootReport()
		seen["mode "+r.Mode] = true
		for _, w := range strings.Fields(r.Reason) {
			seen["reason "+w] = true
		}
		for _, sk := range r.Skipped {
			seen["skip "+sk.Reason] = true
		}
	}
	for kind, words := range map[string][]string{"mode": reportModes, "reason": reportReasons, "skip": reportSkips} {
		for _, w := range words {
			if !seen[kind+" "+w] {
				t.Errorf("no boot gave %s %q", kind, w)
			}
		}
	}
}

// The serial line has one word per field, so the reasons are joined with
// commas there.
func TestHookExtensionSerialLine(t *testing.T) {
	serial := `vos_serial() { echo "SERIAL $*"; }; `
	for _, c := range []struct {
		name, vars, script, want string
		skipOnce                 bool
	}{
		{name: "mounted and skipped",
			want: "extensions mode=enabled set=1 mounted=a,b skipped=c:unproven,ghost:not-in-catalog reason=-"},
		{name: "reasons", vars: "A_ext=0", script: "vos_ext_noverity=1; ", skipOnce: true,
			want: "extensions mode=off set=- mounted=- skipped=- reason=cmdline,skip-once,no-verity"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHookEnv(t)
			h.ext("a")
			h.ext("b", "a")
			h.ext("c")
			h.set("1", "", "", "a", "b", "c", "ghost")
			h.link("enabled", "sets/1")
			h.prove("a", "b")
			if c.skipOnce {
				h.write(storeRel+"/skip-once", "")
			}
			out := h.run(c.vars, serial+c.script+`vos_mount_extensions "$NEW"`)
			if !strings.Contains(out, "SERIAL "+c.want+"\n") {
				t.Errorf("want %q in:\n%s", c.want, out)
			}
		})
	}
}
