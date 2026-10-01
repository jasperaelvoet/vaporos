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
getarg() { case "$1" in vos.slot) echo "${A_slot:-$2}" ;; vos.disk) echo "${A_disk:-$2}" ;; vos.mode) echo "${A_mode:-$2}" ;; vos.version) echo "${A_version:-$2}" ;; *) echo "$2" ;; esac; }
err() { echo "ERR: $*"; }
msg() { :; }
poll_device() { [ -e "$1" ]; }
blkid() {
    case "$3" in
        TYPE) eval "echo \${T_${6##*/}:-}" ;;
        LABEL) head -n 1 "$6" ;;
        *) eval "echo \${G_${6##*/}:-}" ;;
    esac
}
# A fake .iso file is its label, then its version. losetup keeps the
# backing file next to the device, in files: the hook runs it in subshells.
losetup() {
    case "$1" in
        -d) echo "DETACH $(cat "$2.backing")"; rm -f "$2" "$2.backing" ;;
        *) n=$(($(cat "$DEV/loops" 2>/dev/null || echo 0) + 1)); echo "$n" >"$DEV/loops"
           echo "$4" >"$DEV/loop$n.backing"; : >"$DEV/loop$n"; echo "$DEV/loop$n" ;;
    esac
}
udevadm() { :; }
sleep() { :; }
sync() { :; }
reboot() { echo REBOOT; exit 0; }
mount() {
    case "$2" in
        vfat) rmdir "$6" 2>/dev/null; ln -s "$ESP" "$6" ;;
        erofs) [ -n "${FAIL_EROFS:-}" ] && return 1; echo "MOUNT $*" ;;
        iso9660)
            if [ -f "$5.backing" ]; then v="$(sed -n 2p "$(cat "$5.backing")")"; else v="${LABEL_VERSION:-}"; fi
            mkdir -p "$6/vos"; echo "{\"version\": \"$v\"}" >"$6/vos/manifest.json"
            echo "MOUNT iso9660 $(cat "$5.backing" 2>/dev/null || echo "$5")" ;;
        exfat | vfat | ntfs3) rm -rf "$6"; mkdir -p "$6"; cp -R "$HOST/${5##*/}/." "$6/"; echo "MOUNT $2 ${5##*/}" ;;
        *) echo "MOUNT $*" ;;
    esac
}
umount() { rm -rf "$1"; }
e2fsck() { return 0; }
`
	test := "ESP=" + h.path("esp") + "; NEW=" + h.path("new_root") + "; DEV=" + h.path("dev") + "; HOST=" + h.path("host") +
		"; " + vars + "\n" + stubs +
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

func TestHookFindsTheLiveISO(t *testing.T) {
	// A fake .iso file holds its label and version (see the losetup stub).
	iso := func(label, version string) string { return label + "\n" + version + "\n" }
	ventoy := func(h *hookEnv) {
		h.write("host/sdb4/old/vaporos-1.iso", iso("VOS_LIVE", "20260101.000000"))
		h.write("host/sdb4/other.iso", iso("ARCH_202609", ""))
		h.write("host/sdb4/isos/VaporOS-2.ISO", iso("VOS_LIVE", "20261001.120000"))
		h.write("host/sdb4/notes.txt", "")
	}
	live := "A_mode=live A_version=20261001.120000 T_sdb4=exfat "
	cases := []struct {
		name, vars string
		setup      func(h *hookEnv)
		want, not  []string
	}{
		{"labelled device", live + "LABEL_VERSION=20261001.120000", func(h *hookEnv) {
			ventoy(h)
			h.write("dev/disk/by-label/VOS_LIVE", "")
		}, []string{"MOUNT iso9660 " + "@dev/disk/by-label/VOS_LIVE", "MOUNT -t erofs"}, []string{"MOUNT exfat", "REBOOT"}},
		{"file on ventoy's exfat", live, ventoy,
			[]string{"MOUNT exfat sdb4", "MOUNT iso9660 @host/isos/VaporOS-2.ISO", "MOUNT -t erofs"},
			[]string{"other.iso", "REBOOT"}},
		{"another version only", "A_mode=live A_version=20261002.000000 T_sdb4=exfat ", ventoy,
			[]string{"DETACH @host/old/vaporos-1.iso", "DETACH @host/isos/VaporOS-2.ISO", "ERR: live medium not found", "REBOOT"},
			[]string{"MOUNT -t erofs"}},
		// The subtest name ends up in the temp path: it must not say "usb".
		{"stick before internal drive", live + "T_sdb4=exfat T_sdc1=exfat", func(h *hookEnv) {
			ventoy(h)
			h.write("host/sdc1/VaporOS.iso", iso("VOS_LIVE", "20261001.120000"))
			h.write("sys/devices/pci0/usb2/sdc/sdc1/partition", "1")
			os.Symlink(h.path("sys/devices/pci0/usb2/sdc/sdc1"), h.path("sys/class/block/sdc1"))
			h.write("dev/sdc1", "")
		}, []string{"MOUNT exfat sdc1", "MOUNT iso9660 @host/VaporOS.iso"}, []string{"MOUNT exfat sdb4"}},
		{"never ext4", live + "T_sdb4=ext4", ventoy,
			[]string{"ERR: live medium not found", "REBOOT"}, []string{"MOUNT ext4", "MOUNT iso9660"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHookEnv(t)
			c.setup(h)
			out := h.run(c.vars, `vos_mount_live "$NEW"`)
			// Mounted files are copies under /run/vos/host; @ stands for the temp tree.
			at := func(s string) string {
				s = strings.ReplaceAll(s, "@host/", h.path("run/vos/host")+"/")
				return strings.ReplaceAll(s, "@", h.root+"/")
			}
			for _, w := range c.want {
				if !strings.Contains(out, at(w)) {
					t.Errorf("want %q in:\n%s", at(w), out)
				}
			}
			for _, n := range c.not {
				if strings.Contains(out, at(n)) {
					t.Errorf("unwanted %q in:\n%s", at(n), out)
				}
			}
		})
	}
}
