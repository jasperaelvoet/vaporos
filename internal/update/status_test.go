package update

import (
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestNextBoot(t *testing.T) {
	const newer = "20261001.000000"
	// setup: slot a runs bootedVersion, slot b holds the older oldIdleVersion,
	// neither counting.
	cases := []struct {
		name  string
		setup func(e *testEnv)
		want  *NextBoot
	}{
		{"running is newest", func(*testEnv) {}, nil},
		{"staged update", func(e *testEnv) { e.replaceEntry("b", newer, boot.DefaultTries) }, &NextBoot{"b", newer}},
		{"used-up newer loses to a bootable older", func(e *testEnv) {
			e.replaceEntry("b", newer, boot.DefaultTries)
			e.must(boot.MarkBad(config.ESP, "b"))
		}, nil},
		{"rollback to an older slot", func(e *testEnv) { e.must(boot.MarkBad(config.ESP, "a")) }, &NextBoot{"b", oldIdleVersion}},
		{"both used up: the newest", func(e *testEnv) {
			e.must(boot.MarkBad(config.ESP, "a"))
			e.must(boot.MarkBad(config.ESP, "b"))
		}, nil},
		{"both used up, the other newer", func(e *testEnv) {
			e.replaceEntry("b", newer, boot.DefaultTries)
			e.must(boot.MarkBad(config.ESP, "a"))
			e.must(boot.MarkBad(config.ESP, "b"))
		}, &NextBoot{"b", newer}},
		{"same version: the running slot stays", func(e *testEnv) { e.replaceEntry("b", bootedVersion, boot.DefaultTries) }, nil},
		{"no entries", func(e *testEnv) {
			e.must(boot.RemoveSlotEntries(config.ESP, "a"))
			e.must(boot.RemoveSlotEntries(config.ESP, "b"))
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			c.setup(e)
			got, err := nextBoot("a")
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
				t.Fatalf("nextBoot = %+v, want %+v", got, c.want)
			}
		})
	}
}

// replaceEntry gives slot a new entry for version with tries (0: uncounted).
func (e *testEnv) replaceEntry(slot, version string, tries int) {
	e.t.Helper()
	kdir := e.t.TempDir()
	e.write(filepath.Join(kdir, "vmlinuz"), "kernel "+version)
	e.write(filepath.Join(kdir, "initramfs.img"), "initrd "+version)
	e.must(boot.RemoveSlotEntries(config.ESP, slot))
	e.must(boot.InstallEntry(config.ESP, version, slot, kdir, boot.Cmdline(slot, imageCmdline, machineArgs), tries))
}
