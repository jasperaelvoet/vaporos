package boot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// The initramfs half of extensions (docs/CONTRACTS.md "Extensions", Boot):
// which set a boot uses, which images of it mount over /usr, and what
// /run/vos/extensions.json then says. Same harness as hook_test.go.

const (
	storeRel     = "new_root/state/var/lib/vos/ext"
	bootCountVar = "sys/firmware/efi/efivars/LoaderBootCountPath-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
)

func shaOf(id string) string { return digestOf("sha256 " + id) }
func fsvOf(id string) string { return digestOf("fsverity " + id) }

func digestOf(s string) string {
	d := sha256.Sum256([]byte(s))
	return hex.EncodeToString(d[:])
}

// ext lists id in the slot's catalog, with a sealed image in the store whose
// tree has usr/share/<id>/owner.
func (h *hookEnv) ext(id string, requires ...string) {
	content := "erofs image of " + id
	line := fmt.Sprintf("ext %s %s %d %s -", id, shaOf(id), len(content), fsvOf(id))
	for _, r := range requires {
		line += " " + r
	}
	h.catalog = append(h.catalog, line)
	h.write("new_root/usr/lib/vos/extensions.list", "# test catalog\ndispatcher 1\n"+strings.Join(h.catalog, "\n")+"\n")
	h.write(storeRel+"/images/"+shaOf(id)+".raw", content)
	h.write("fsv/"+shaOf(id), fsvOf(id))
	h.write("trees/"+shaOf(id)+"/usr/share/"+id+"/owner", id)
}

// set writes sets/<name>; an empty tries or modprobe leaves that file out.
func (h *hookEnv) set(name, tries, modprobe string, ids ...string) {
	dir := storeRel + "/sets/" + name
	h.write(dir+"/ids", strings.Join(ids, "\n")+"\n")
	if tries != "" {
		h.write(dir+"/tries", tries+"\n")
	}
	if modprobe != "" {
		h.write(dir+"/modprobe.conf", modprobe)
	}
}

func (h *hookEnv) link(name, target string) {
	h.mkdir(storeRel)
	if err := os.Symlink(target, h.path(storeRel, name)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hookEnv) prove(ids ...string) {
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s %s\n", id, fsvOf(id))
	}
	h.write(storeRel+"/proven", b.String())
}

func (h *hookEnv) read(rel string) string {
	b, err := os.ReadFile(h.path(rel))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

type extReport struct {
	Mode      string `json:"mode"`
	Set       string `json:"set"`
	TriesLeft int    `json:"tries_left"`
	Reason    string `json:"reason"`
	Mounted   []struct {
		ID       string `json:"id"`
		SHA256   string `json:"sha256"`
		FSVerity string `json:"fsverity"`
	} `json:"mounted"`
	Skipped []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// report checks /run/vos/extensions.json against the contract (compact JSON,
// exactly its fields, the catalog's digests) and sums it up in one line.
func (h *hookEnv) report() string {
	h.t.Helper()
	raw, err := os.ReadFile(h.path("run/vos/extensions.json"))
	if err != nil {
		h.t.Fatalf("no extensions.json: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || compact.String()+"\n" != string(raw) {
		h.t.Fatalf("extensions.json is not one line of compact JSON (%v):\n%s", err, raw)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if strings.Join(keys, " ") != "mode mounted reason set skipped tries_left" ||
		fields["mounted"][0] != '[' || fields["skipped"][0] != '[' {
		h.t.Fatalf("extensions.json fields:\n%s", raw)
	}
	var r extReport
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		h.t.Fatalf("extensions.json: %v\n%s", err, raw)
	}
	var mounted, skipped []string
	for _, m := range r.Mounted {
		if m.SHA256 != shaOf(m.ID) || m.FSVerity != fsvOf(m.ID) {
			h.t.Errorf("%s mounted with digests %s %s", m.ID, m.SHA256, m.FSVerity)
		}
		mounted = append(mounted, m.ID)
	}
	for _, s := range r.Skipped {
		skipped = append(skipped, s.ID+":"+s.Reason)
	}
	return fmt.Sprintf("%s set=%s tries=%d reason=%q mounted=%s skipped=%s",
		r.Mode, r.Set, r.TriesLeft, r.Reason, strings.Join(mounted, ","), strings.Join(skipped, ","))
}

func TestHookExtensionSets(t *testing.T) {
	// Every case starts from the catalog a, b (requires a), c, all sealed in
	// the store; setup adds the sets. enabled: set 1 with all three, proven.
	enabled := func(h *hookEnv) {
		h.set("1", "", "", "a", "b", "c")
		h.link("enabled", "sets/1")
		h.prove("a", "b", "c")
	}
	pending := func(tries string) func(h *hookEnv) {
		return func(h *hookEnv) {
			h.set("1", "", "", "a")
			h.link("enabled", "sets/1")
			h.prove("a")
			h.set("2", tries, "", "a", "b", "c")
			h.link("pending", "sets/2")
		}
	}
	cases := []struct {
		name, vars, script string
		setup              func(h *hookEnv)
		want               string
		check              func(t *testing.T, h *hookEnv, out string)
	}{
		{name: "vos.ext=0", vars: "A_ext=0", setup: pending("2"),
			want: `off set= tries=0 reason="cmdline" mounted= skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if got := h.read(storeRel + "/sets/2/tries"); got != "2\n" {
					t.Errorf("tries %q", got)
				}
			}},
		{name: "skip-once", setup: func(h *hookEnv) {
			pending("2")(h)
			h.write(storeRel+"/skip-once", "")
		}, want: `off set= tries=0 reason="skip-once" mounted= skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if _, err := os.Lstat(h.path(storeRel, "skip-once")); err == nil {
					t.Error("skip-once is still there")
				}
				if got := h.read(storeRel + "/sets/2/tries"); got != "2\n" {
					t.Errorf("tries %q", got)
				}
			}},
		{name: "vos.ext=0 uses skip-once up too", vars: "A_ext=0", setup: func(h *hookEnv) {
			pending("2")(h)
			h.write(storeRel+"/skip-once", "")
		}, want: `off set= tries=0 reason="cmdline skip-once" mounted= skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if _, err := os.Lstat(h.path(storeRel, "skip-once")); err == nil {
					t.Error("skip-once is still there")
				}
			}},
		{name: "os trial: enabled at the catalog's digests, pending waits", setup: func(h *hookEnv) {
			h.set("1", "", "", "a", "c")
			h.link("enabled", "sets/1")
			h.set("2", "2", "", "a", "b", "c")
			h.link("pending", "sets/2")
			h.write(bootCountVar, "")
		}, want: `os-trial set=1 tries=0 reason="" mounted=a,c skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if got := h.read(storeRel + "/sets/2/tries"); got != "2\n" {
					t.Errorf("pending's tries %q", got)
				}
			}},
		{name: "os trial without a set", setup: func(h *hookEnv) { h.write(bootCountVar, "") },
			want: `os-trial set= tries=0 reason="no-set" mounted= skipped=`},
		{name: "pending trial", setup: pending("2"),
			want: `pending set=2 tries=1 reason="" mounted=a,b,c skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if got := h.read(storeRel + "/sets/2/tries"); got != "1\n" {
					t.Errorf("tries %q", got)
				}
				if _, err := os.Stat(h.path(storeRel, "sets/2/tries.new")); err == nil {
					t.Error("tries.new left behind")
				}
			}},
		{name: "pending's last try", setup: pending("1"),
			want: `pending set=2 tries=0 reason="" mounted=a,b,c skipped=`},
		{name: "pending's tries used up", setup: pending("0"),
			want: `enabled set=1 tries=0 reason="tries-used" mounted=a skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if got := h.read(storeRel + "/sets/2/tries"); got != "0\n" {
					t.Errorf("tries %q", got)
				}
			}},
		{name: "pending's tries unreadable", setup: pending("12"),
			want: `enabled set=1 tries=0 reason="tries-used" mounted=a skipped=`},
		{name: "pending without tries", setup: pending(""),
			want: `enabled set=1 tries=0 reason="tries-used" mounted=a skipped=`},
		{name: "pending's tries cannot be written", setup: pending("2"),
			script: `mv() { case "$2" in */tries.new) return 1 ;; esac; command mv "$@"; }; vos_mount_extensions "$NEW"`,
			want:   `enabled set=1 tries=0 reason="tries-write" mounted=a skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if got := h.read(storeRel + "/sets/2/tries"); got != "2\n" {
					t.Errorf("tries %q", got)
				}
				if _, err := os.Stat(h.path(storeRel, "sets/2/tries.new")); err == nil {
					t.Error("tries.new left behind")
				}
			}},
		{name: "enabled mounts only proven images", setup: func(h *hookEnv) {
			enabled(h)
			h.prove("a", "c")
		}, want: `enabled set=1 tries=0 reason="" mounted=a,c skipped=b:unproven`},
		{name: "proven at another digest", setup: func(h *hookEnv) {
			enabled(h)
			h.write(storeRel+"/proven", "a "+fsvOf("a")+"\nb "+fsvOf("old b")+"\nc "+fsvOf("c")+"\n")
		}, want: `enabled set=1 tries=0 reason="" mounted=a,c skipped=b:unproven`},
		{name: "no set", want: `enabled set= tries=0 reason="no-set" mounted= skipped=`},
		{name: "a link out of the store", setup: func(h *hookEnv) {
			enabled(h)
			os.Remove(h.path(storeRel, "enabled"))
			h.link("enabled", h.path(storeRel, "sets/1"))
		}, want: `enabled set= tries=0 reason="no-set" mounted= skipped=`},
		{name: "a link up and out", setup: func(h *hookEnv) {
			enabled(h)
			os.Remove(h.path(storeRel, "enabled"))
			h.link("enabled", "sets/../sets/1")
		}, want: `enabled set= tries=0 reason="no-set" mounted= skipped=`},
		{name: "missing image", setup: func(h *hookEnv) {
			enabled(h)
			os.Remove(h.path(storeRel, "images", shaOf("c")+".raw"))
		}, want: `enabled set=1 tries=0 reason="" mounted=a,b skipped=c:missing`},
		{name: "wrong size", setup: func(h *hookEnv) {
			enabled(h)
			h.write(storeRel+"/images/"+shaOf("c")+".raw", "a longer erofs image of c")
		}, want: `enabled set=1 tries=0 reason="" mounted=a,b skipped=c:size`},
		{name: "another fs-verity digest", setup: func(h *hookEnv) {
			enabled(h)
			h.write("fsv/"+shaOf("c"), fsvOf("tampered c"))
		}, want: `enabled set=1 tries=0 reason="" mounted=a,b skipped=c:fsverity`},
		{name: "not sealed", setup: func(h *hookEnv) {
			enabled(h)
			os.Remove(h.path("fsv", shaOf("c")))
		}, want: `enabled set=1 tries=0 reason="" mounted=a,b skipped=c:fsverity`},
		{name: "a skipped requirement skips its dependents", setup: func(h *hookEnv) {
			h.ext("d", "b")
			h.set("1", "", "", "a", "b", "c", "d")
			h.link("enabled", "sets/1")
			h.prove("a", "b", "c", "d")
			os.Remove(h.path(storeRel, "images", shaOf("a")+".raw"))
		}, want: `enabled set=1 tries=0 reason="" mounted=c skipped=a:missing,b:requires,d:requires`},
		{name: "a requirement outside the set", setup: func(h *hookEnv) {
			enabled(h)
			h.write(storeRel+"/sets/1/ids", "b\nc\n")
		}, want: `enabled set=1 tries=0 reason="" mounted=c skipped=b:requires`},
		{name: "not in this catalog", setup: func(h *hookEnv) {
			enabled(h)
			h.write(storeRel+"/sets/1/ids", "ghost\nc\na\n")
		}, want: `enabled set=1 tries=0 reason="" mounted=a,c skipped=ghost:not-in-catalog`},
		{name: "ids that are not ids", setup: func(h *hookEnv) {
			enabled(h)
			h.write(storeRel+"/sets/1/ids", "a\n../c\nB\nx\"y\n*\nb c\n"+strings.Repeat("z", 33)+"\na\n  c  \nx")
		}, want: `enabled set=1 tries=0 reason="" mounted=a,c skipped=x:not-in-catalog`},
		{name: "mount fails", vars: "FAIL_IMAGE=" + shaOf("c"), setup: enabled,
			want: `enabled set=1 tries=0 reason="" mounted=a,b skipped=c:mount`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if !strings.Contains(out, "DETACH "+h.path(storeRel, "images", shaOf("c")+".raw")) {
					t.Errorf("loop device not detached:\n%s", out)
				}
			}},
		{name: "file-backed mount fails, losetup works", vars: "FAIL_FILE_MOUNT=1", setup: enabled,
			want: `enabled set=1 tries=0 reason="" mounted=a,b,c skipped=`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if h.read("dev/loop3.backing") != h.path(storeRel, "images", shaOf("c")+".raw")+"\n" || strings.Contains(out, "DETACH") {
					t.Errorf("loop devices:\n%s", out)
				}
			}},
		{name: "no usr", setup: func(h *hookEnv) {
			enabled(h)
			os.RemoveAll(h.path("trees", shaOf("b"), "usr"))
			h.write("trees/"+shaOf("b")+"/etc/b.conf", "")
		}, want: `enabled set=1 tries=0 reason="" mounted=a,c skipped=b:no-usr`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if !strings.Contains(out, "UMOUNT "+h.path("run/vos/x/2")+"\n") ||
					!strings.Contains(out, "lowerdir="+h.path("run/vos/x/3/usr")+":"+h.path("run/vos/x/1/usr")+":") {
					t.Errorf("no-usr image:\n%s", out)
				}
			}},
		{name: "overlay fails", vars: "FAIL_OVERLAY=1", setup: enabled,
			want: `enabled set=1 tries=0 reason="" mounted= skipped=a:overlay,b:overlay,c:overlay`,
			check: func(t *testing.T, h *hookEnv, out string) {
				for _, n := range []string{"1", "2", "3"} {
					if !strings.Contains(out, "UMOUNT "+h.path("run/vos/x", n)+"\n") {
						t.Errorf("x/%s still mounted:\n%s", n, out)
					}
				}
			}},
		{name: "overlay fails on loop devices", vars: "FAIL_OVERLAY=1 FAIL_FILE_MOUNT=1", setup: enabled,
			want: `enabled set=1 tries=0 reason="" mounted= skipped=a:overlay,b:overlay,c:overlay`,
			check: func(t *testing.T, h *hookEnv, out string) {
				if strings.Count(out, "DETACH ") != 3 {
					t.Errorf("loop devices not all detached:\n%s", out)
				}
			}},
		{name: "live", vars: "A_mode=live", setup: enabled,
			check: func(t *testing.T, h *hookEnv, out string) {
				if _, err := os.Stat(h.path("run/vos/extensions.json")); err == nil {
					t.Error("live mode wrote extensions.json")
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHookEnv(t)
			h.ext("a")
			h.ext("b", "a")
			h.ext("c")
			if c.setup != nil {
				c.setup(h)
			}
			script := c.script
			if script == "" {
				script = `vos_mount_extensions "$NEW"`
			}
			out := h.run(c.vars, script)
			if c.want != "" {
				if got := h.report(); got != c.want {
					t.Errorf("got  %s\nwant %s\n%s", got, c.want, out)
				}
				// The stub prints only overlays that mount.
				overlay := strings.Contains(out, "-t overlay overlay -o ro,lowerdir=")
				if overlay != !strings.Contains(c.want, "mounted= ") {
					t.Errorf("/usr overlay mounted: %v\n%s", overlay, out)
				}
			}
			if c.check != nil {
				c.check(t, h, out)
			}
		})
	}
}

func TestHookExtensionOverlay(t *testing.T) {
	h := newHookEnv(t)
	h.ext("a")
	h.ext("b", "a")
	h.ext("c")
	h.set("1", "", "", "c", "b", "a")
	h.link("enabled", "sets/1")
	h.prove("a", "b", "c")
	out := h.run("", `vos_mount_extensions "$NEW"`)

	// Catalog order: a at x/1, b at x/2, c at x/3; the last one is on top.
	x := func(n string) string { return h.path("run/vos/x", n, "usr") }
	want := "MOUNT -t overlay overlay -o ro,lowerdir=" + x("3") + ":" + x("2") + ":" + x("1") + ":" + h.path("new_root/usr") +
		" " + h.path("new_root/usr") + " FORCE=always\n"
	if !strings.Contains(out, want) {
		t.Errorf("want %q in:\n%s", want, out)
	}
	for n, id := range map[string]string{"1": "a", "2": "b", "3": "c"} {
		if got := h.read("run/vos/x/" + n + "/usr/share/" + id + "/owner"); got != id {
			t.Errorf("x/%s holds %q, want %s", n, got, id)
		}
	}
	if _, err := os.Stat(h.path("run/modprobe.d")); err == nil {
		t.Error("module options without any in the set")
	}
}

func TestHookExtensionModuleOptions(t *testing.T) {
	const conf = `options amdgpu ppfeaturemask=0xfff7ffff
options it87 ignore_resource_conflict=1
options i915 enable_guc=3
install amdgpu /bin/sh -c reboot
options amdgpu ppfeaturemask=$(reboot)
options amdgpu ppfeaturemask=1;reboot
options amdgpu ppfeaturemask=1 dc=0
options  amdgpu ppfeaturemask=2
options amdgpu  ppfeaturemask=2
options amdgpu ppfeaturemask=a=b
options amdgpu ppfeaturemask=
options amdgpu dc=0
options amdgpu
softdep amdgpu pre: it87
# options amdgpu ppfeaturemask=3
blacklist amdgpu
options it87 force_id=0x8628
` + "options amdgpu ppfeaturemask=2 \n" + "options amdgpu ppfeaturemask=4\r\n" + "\toptions amdgpu ppfeaturemask=5"
	setup := func(h *hookEnv) {
		h.ext("a")
		h.ext("b", "a")
		h.ext("c")
		h.write("trees/"+shaOf("a")+"/usr/lib/vos/ext/a/module-options", "amdgpu ppfeaturemask\n")
		h.write("trees/"+shaOf("b")+"/usr/lib/vos/ext/b/module-options", "it87 ignore_resource_conflict\nit87 force_id")
		// c is not proven, so its list never counts; nor does a list b
		// keeps for another id.
		h.write("trees/"+shaOf("c")+"/usr/lib/vos/ext/c/module-options", "i915 enable_guc\n")
		h.write("trees/"+shaOf("b")+"/usr/lib/vos/ext/a/module-options", "amdgpu dc\n")
		h.set("1", "", conf, "a", "b", "c")
		h.link("enabled", "sets/1")
		h.prove("a", "b")
	}

	h := newHookEnv(t)
	setup(h)
	out := h.run("", `vos_mount_extensions "$NEW"`)
	want := "# Written by the VaporOS initramfs: module options of extension set 1.\n" +
		"options amdgpu ppfeaturemask=0xfff7ffff\n" +
		"options it87 ignore_resource_conflict=1\n" +
		"options it87 force_id=0x8628\n"
	if got := h.read("run/modprobe.d/vos-ext.conf"); got != want {
		t.Errorf("vos-ext.conf:\n%s\nwant:\n%s\n%s", got, want, out)
	}
	if got := h.report(); got != `enabled set=1 tries=0 reason="" mounted=a,b skipped=c:unproven` {
		t.Errorf("report %s", got)
	}

	// Nothing mounted, no options.
	for _, vars := range []string{"FAIL_OVERLAY=1", "A_ext=0"} {
		h = newHookEnv(t)
		setup(h)
		out = h.run(vars, `vos_mount_extensions "$NEW"`)
		if _, err := os.Stat(h.path("run/modprobe.d/vos-ext.conf")); err == nil {
			t.Errorf("%s: module options written:\n%s", vars, out)
		}
	}
}

func TestHookExtensionVerityFeature(t *testing.T) {
	disk := "A_slot=a A_disk=aaaa-1 " + guids
	setup := func(h *hookEnv) {
		h.ext("a")
		h.set("1", "", "", "a")
		h.link("enabled", "sets/1")
		h.prove("a")
	}
	trace := func(h *hookEnv) string { return strings.TrimSpace(h.read("trace")) }

	// Missing: turned on between the check and the mount of vos_data, and
	// the extensions are laid over /usr after /etc is assembled.
	h := newHookEnv(t)
	setup(h)
	out := h.run(disk, `vos_mount_disk "$NEW"`)
	dev := h.path("dev/sda4")
	if got, want := trace(h), "E2FSCK -p "+dev+"\nTUNE2FS -O verity "+dev+"\nMOUNT ext4"; got != want {
		t.Errorf("trace:\n%s\nwant:\n%s", got, want)
	}
	etc, usr := strings.Index(out, "lowerdir="+h.path("new_root/etc")), strings.Index(out, "ro,lowerdir=")
	if etc < 0 || usr < etc {
		t.Errorf("/usr overlay not after /etc:\n%s", out)
	}
	if got := h.report(); got != `enabled set=1 tries=0 reason="" mounted=a skipped=` {
		t.Errorf("report %s", got)
	}

	// Already there: left alone.
	h = newHookEnv(t)
	setup(h)
	h.write("verity", "")
	h.run(disk, `vos_mount_disk "$NEW"`)
	if got := trace(h); strings.Contains(got, "TUNE2FS") {
		t.Errorf("tune2fs ran on a filesystem with verity:\n%s", got)
	}

	// tune2fs fails: the boot goes on, and extensions.json says why.
	h = newHookEnv(t)
	setup(h)
	out = h.run(disk+" FAIL_TUNE2FS=1", `vos_mount_disk "$NEW"`)
	if strings.Contains(out, "REBOOT") || !strings.Contains(out, "MOUNT -t ext4") {
		t.Errorf("tune2fs failure stopped the boot:\n%s", out)
	}
	if got := h.report(); !strings.Contains(got, `reason="no-verity"`) {
		t.Errorf("report %s", got)
	}
	h = newHookEnv(t)
	setup(h)
	h.write(storeRel+"/skip-once", "")
	h.run(disk+" FAIL_TUNE2FS=1", `vos_mount_disk "$NEW"`)
	if got := h.report(); !strings.Contains(got, `reason="skip-once no-verity"`) {
		t.Errorf("report %s", got)
	}
}
