package boot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The initramfs hook (rootfs/usr/lib/initcpio/hooks/vos) finds partitions
// on the vos.disk disk and marks a slot that cannot start as bad, in shell.
// These tests run its functions under dash against a fake sysfs, /dev and
// ESP: paths are rewritten into a temp tree, [ -b ] becomes [ -e ] (plain
// files stand in for block devices), and mount, blkid and reboot are stubs.

const hookPath = "../../rootfs/usr/lib/initcpio/hooks/vos"

type hookEnv struct {
	t    *testing.T
	root string
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	h := &hookEnv{t: t, root: t.TempDir()}
	for _, d := range []string{"sys/class/block", "dev/disk/by-partlabel", "run", "esp/loader/entries", "new_root"} {
		h.mkdir(d)
	}
	for _, disk := range []string{"sda", "sdb"} {
		dev := h.path("sys/devices", disk)
		h.mkdir("sys/devices/" + disk)
		os.Symlink(dev, h.path("sys/class/block", disk))
		h.write("dev/"+disk, "")
		for i, label := range []string{"vos_esp", "vos_a", "vos_b", "vos_data"} {
			part := fmt.Sprintf("%s%d", disk, i+1)
			h.write(filepath.Join("sys/devices", disk, part, "partition"), fmt.Sprint(i+1))
			h.write(filepath.Join("sys/devices", disk, part, "uevent"), "DEVTYPE=partition\nPARTNAME="+label+"\n")
			os.Symlink(filepath.Join(dev, part), h.path("sys/class/block", part))
			h.write("dev/"+part, "")
			// udev gave the labels to sdb, the other VaporOS disk.
			if disk == "sdb" {
				os.Symlink(h.path("dev", part), h.path("dev/disk/by-partlabel", label))
			}
		}
	}
	h.write("esp/loader/entries/vos-2.conf", entryText("2", "vos.slot=a quiet"))
	h.write("esp/loader/entries/vos-1.conf", entryText("1", "vos.slot=b quiet"))
	return h
}

func (h *hookEnv) path(p ...string) string { return filepath.Join(append([]string{h.root}, p...)...) }

func (h *hookEnv) mkdir(rel string) {
	if err := os.MkdirAll(h.path(rel), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hookEnv) write(rel, content string) {
	h.mkdir(filepath.Dir(rel))
	if err := os.WriteFile(h.path(rel), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hookEnv) entries() string {
	des, _ := os.ReadDir(h.path("esp/loader/entries"))
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// run sources the rewritten hook with vars set, runs run_hook, then script.
func (h *hookEnv) run(vars, script string) string {
	h.t.Helper()
	shell, err := exec.LookPath("dash")
	if err != nil {
		h.t.Skip("no dash to run the initramfs hook with")
	}
	src, err := os.ReadFile(hookPath)
	if err != nil {
		h.t.Fatal(err)
	}
	hook := strings.NewReplacer("/sys/class/block", h.path("sys/class/block"), "/dev/", h.path("dev")+"/",
		"/run/", h.path("run")+"/", "[ -b ", "[ -e ").Replace(string(src))
	h.write("hook.sh", hook)
	stubs := `
getarg() { case "$1" in vos.slot) echo "${A_slot:-$2}" ;; vos.disk) echo "${A_disk:-$2}" ;; vos.mode) echo "${A_mode:-$2}" ;; *) echo "$2" ;; esac; }
err() { echo "ERR: $*"; }
msg() { :; }
poll_device() { [ -e "$1" ]; }
blkid() { eval "echo \${G_${6##*/}:-}"; }
udevadm() { :; }
sleep() { :; }
sync() { :; }
reboot() { echo REBOOT; exit 0; }
mount() {
    case "$2" in
        vfat) rmdir "$6" 2>/dev/null; ln -s "$ESP" "$6" ;;
        erofs) [ -n "${FAIL_EROFS:-}" ] && return 1; echo "MOUNT $*" ;;
        *) echo "MOUNT $*" ;;
    esac
}
umount() { rm -f "$1"; }
e2fsck() { return 0; }
`
	test := "ESP=" + h.path("esp") + "; NEW=" + h.path("new_root") + "; " + vars + "\n" + stubs +
		". " + h.path("hook.sh") + "\nrun_hook\n" + script + "\n"
	h.write("test.sh", test)
	out, _ := exec.Command(shell, h.path("test.sh")).CombinedOutput()
	return string(out)
}

const guids = "G_sda=aaaa-1 G_sdb=bbbb-2"

func TestHookMountsTheVosDisk(t *testing.T) {
	h := newHookEnv(t)
	out := h.run("A_slot=a A_disk=AAAA-1 "+guids, `vos_mount_disk "$NEW"`)
	for _, want := range []string{
		"MOUNT -t erofs -o ro " + h.path("dev/sda2") + " ",
		"MOUNT -t ext4 -o rw,noatime " + h.path("dev/sda4") + " ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	rules, _ := os.ReadFile(h.path("run/udev/rules.d/61-vos-boot-disk.rules"))
	if !strings.Contains(string(rules), `KERNEL=="sda1", OPTIONS+="link_priority=100"`) || strings.Contains(string(rules), "sdb") {
		t.Errorf("udev rule:\n%s", rules)
	}

	// Without vos.disk (installs from before it): by label, as before.
	h = newHookEnv(t)
	if out := h.run("A_slot=a "+guids, `vos_mount_disk "$NEW"`); !strings.Contains(out, "ro "+h.path("dev/disk/by-partlabel/vos_a")) {
		t.Errorf("by label:\n%s", out)
	}
	// A vos.disk no disk has: never another disk's partitions.
	h = newHookEnv(t)
	out = h.run("A_slot=a A_disk=cccc-3 "+guids, `vos_mount_disk "$NEW"`)
	if !strings.Contains(out, "ERR: no disk with GPT disk GUID cccc-3") || !strings.Contains(out, "REBOOT") || strings.Contains(out, "MOUNT -t erofs") {
		t.Errorf("unknown disk:\n%s", out)
	}
	// No disk GUID readable at all (a tool problem): by label.
	h = newHookEnv(t)
	if out := h.run("A_slot=a A_disk=aaaa-1", `vos_mount_disk "$NEW"`); !strings.Contains(out, "ro "+h.path("dev/disk/by-partlabel/vos_a")) {
		t.Errorf("unreadable GUIDs:\n%s", out)
	}
}

func TestHookMarksFailedSlotBad(t *testing.T) {
	cases := []struct {
		name, vars, script string
		setup              func(h *hookEnv)
		want               string
	}{
		{"blessed entry", "A_slot=a A_disk=aaaa-1 FAIL_EROFS=1 " + guids, `vos_mount_disk "$NEW"`, nil,
			"vos-1.conf vos-2+0-1.conf"},
		{"emergency hook", "A_slot=a A_disk=aaaa-1 " + guids, `vos_mount_disk "$NEW"; run_emergencyhook`, nil,
			"vos-1.conf vos-2+0-1.conf"},
		{"counted entry: systemd-boot counts", "A_slot=a A_disk=aaaa-1 FAIL_EROFS=1 " + guids, `vos_mount_disk "$NEW"`,
			func(h *hookEnv) {
				os.Rename(h.path("esp/loader/entries/vos-2.conf"), h.path("esp/loader/entries/vos-2+2-1.conf"))
			}, "vos-1.conf vos-2+2-1.conf"},
		{"nothing to fall back to", "A_slot=a A_disk=aaaa-1 FAIL_EROFS=1 " + guids, `vos_mount_disk "$NEW"`,
			func(h *hookEnv) {
				os.Rename(h.path("esp/loader/entries/vos-1.conf"), h.path("esp/loader/entries/vos-1+0-3.conf"))
			}, "vos-1+0-3.conf vos-2.conf"},
		{"before vos.disk: the labelled ESP of the slot's disk", "A_slot=a FAIL_EROFS=1", `vos_mount_disk "$NEW"`, nil,
			"vos-1.conf vos-2+0-1.conf"},
		{"before vos.disk: an ESP on another disk", "A_slot=a FAIL_EROFS=1", `vos_mount_disk "$NEW"`,
			func(h *hookEnv) {
				os.Remove(h.path("dev/disk/by-partlabel/vos_esp"))
				os.Symlink(h.path("dev/sda1"), h.path("dev/disk/by-partlabel/vos_esp"))
			}, "vos-1.conf vos-2.conf"},
		{"live", "A_mode=live A_slot=a", `vos_die test`, nil, "vos-1.conf vos-2.conf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHookEnv(t)
			if c.setup != nil {
				c.setup(h)
			}
			out := h.run(c.vars, c.script)
			if !strings.Contains(out, "REBOOT") {
				t.Errorf("no reboot:\n%s", out)
			}
			if got := h.entries(); got != c.want {
				t.Errorf("entries %q, want %q\n%s", got, c.want, out)
			}
		})
	}
}
