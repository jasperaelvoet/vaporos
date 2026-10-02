package boot

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// Trial boots (docs/CONTRACTS.md "Extensions", Trial and promotion): a
// pending set uses up its try before anything of it mounts, and a trial
// arms a hardware watchdog, so a hang reboots and counts as a try.

func TestHookExtensionTriesFirst(t *testing.T) {
	h := newHookEnv(t)
	h.ext("a")
	h.ext("b", "a")
	h.ext("c")
	h.set("1", "", "", "a")
	h.link("enabled", "sets/1")
	h.prove("a")
	h.set("2", "2", "", "a", "b", "c")
	h.link("pending", "sets/2")
	h.run("", `vos_mount_extensions "$NEW"`)
	want := []string{"SYNC", "DECREMENT 1", "SYNC", "MOUNT erofs " + shaOf("a"), "MOUNT erofs " + shaOf("b"), "MOUNT erofs " + shaOf("c")}
	if got := strings.Split(strings.TrimSpace(h.read("trace")), "\n"); !slices.Equal(got, want) {
		t.Errorf("trace:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestHookTrialWatchdog(t *testing.T) {
	disk := "A_slot=a A_disk=aaaa-1 " + guids
	base := func(h *hookEnv) {
		h.ext("a")
		h.ext("b", "a")
		h.ext("c")
		h.set("1", "", "", "a")
		h.link("enabled", "sets/1")
		h.prove("a")
	}
	pending := func(tries string) func(h *hookEnv) {
		return func(h *hookEnv) {
			base(h)
			h.set("2", tries, "", "a", "b", "c")
			h.link("pending", "sets/2")
		}
	}
	counted := func(h *hookEnv) {
		base(h)
		h.write(bootCountVar, "")
	}
	const drivers = "MODPROBE -q sp5100_tco\nMODPROBE -q iTCO_wdt\nMODPROBE -q wdat_wdt"
	cases := []struct {
		name, vars, script string
		setup              func(h *hookEnv)
		armed              bool
	}{
		{name: "extension trial", setup: pending("2"), armed: true},
		{name: "os trial", setup: counted, armed: true},
		// Still an OS update on trial, extensions or not.
		{name: "os trial with vos.ext=0", vars: "A_ext=0", setup: counted, armed: true},
		{name: "enabled", setup: base},
		{name: "vos.ext=0", vars: "A_ext=0", setup: pending("2")},
		{name: "pending's tries used up", setup: pending("0")},
		{name: "pending's tries cannot be written", setup: pending("2"),
			script: `mv() { case "$2" in */tries.new) return 1 ;; esac; command mv "$@"; }; `},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHookEnv(t)
			c.setup(h)
			out := h.run(disk+" "+c.vars, c.script+`vos_mount_disk "$NEW"`)
			if strings.Contains(out, "REBOOT") {
				t.Fatalf("the boot stopped:\n%s", out)
			}
			conf, err := os.ReadFile(h.path("run/systemd/system.conf.d/50-vos-trial.conf"))
			tr := strings.TrimSpace(h.read("trace"))
			if !c.armed {
				if err == nil || strings.Contains(tr, "_wdt") || strings.Contains(tr, "sp5100") {
					t.Errorf("a watchdog on a boot that is no trial:\n%s\n%s", conf, tr)
				}
				return
			}
			if want := "# Written by the VaporOS initramfs: this boot is on trial.\n[Manager]\nRuntimeWatchdogSec=60s\n"; string(conf) != want {
				t.Errorf("50-vos-trial.conf (%v):\n%s\nwant:\n%s", err, conf, want)
			}
			if !strings.HasSuffix(tr, "\n"+drivers) {
				t.Errorf("trace:\n%s", tr)
			}
		})
	}

	// An extension trial in order: vos_data checked and given verity, the
	// try taken off and synced, the images mounted, the watchdog drivers
	// loaded.
	h := newHookEnv(t)
	pending("2")(h)
	h.run(disk, `vos_mount_disk "$NEW"`)
	dev := h.path("dev/sda4")
	want := "E2FSCK -p " + dev + "\nMODPROBE -q ext4\nTUNE2FS -O verity " + dev + "\nMOUNT ext4\nSYNC\nDECREMENT 1\nSYNC\n" +
		"MOUNT erofs " + shaOf("a") + "\nMOUNT erofs " + shaOf("b") + "\nMOUNT erofs " + shaOf("c") + "\n" + drivers
	if got := strings.TrimSpace(h.read("trace")); got != want {
		t.Errorf("trace:\n%s\nwant:\n%s", got, want)
	}
}
