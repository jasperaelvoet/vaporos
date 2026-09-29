package install

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/storage"
)

// fakeSys is a machine in a temp dir: sysfs, /dev (plain files stand in
// for device nodes), mountinfo, the live medium and the runtime dirs.
type fakeSys struct {
	t     *testing.T
	root  string
	disks map[string]string // kernel name -> sysfs device dir
	scan  []storage.Disk
	gpu   display.GPUInfo

	mu        sync.Mutex
	entry     entryCall
	entryN    int
	adminPass string

	// onStep, if set, runs for every progress report of f.recorder, in
	// the installer's goroutine: tests use it to act between steps.
	onStep func(step string)
}

type entryCall struct {
	esp, version, slot, srcDir, options string
	tries                               int
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	root := t.TempDir()
	// macOS temp dirs live behind a /var -> /private/var symlink; resolve
	// it so paths compare equal to what EvalSymlinks returns.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSys{t: t, root: root, disks: map[string]string{},
		gpu: display.GPUInfo{Vendor: "amd", Name: "RX 9070 XT", Driver: "amdgpu", Supported: true}}

	oldPaths, oldBlock, oldAllow := paths, isBlockDevice, allowAnywhere
	oldRun, oldLive, oldKeys, oldInfo := config.RunDir, config.LiveMedium, config.KeysDir, config.ImageInfoPath
	oldProbeTimeout := probeImageTimeout
	t.Cleanup(func() {
		paths, isBlockDevice, allowAnywhere = oldPaths, oldBlock, oldAllow
		config.RunDir, config.LiveMedium, config.KeysDir, config.ImageInfoPath = oldRun, oldLive, oldKeys, oldInfo
		probeImageTimeout = oldProbeTimeout
	})

	paths = sysPaths{
		ClassBlock: f.path("sys/class/block"),
		DevBlock:   f.path("sys/dev/block"),
		Dev:        f.path("dev"),
		Mountinfo:  f.path("proc/mountinfo"),
		Swaps:      f.path("proc/swaps"),
		EFI:        f.path("sys/firmware/efi"),
		Zoneinfo:   f.path("usr/share/zoneinfo"),
		Localtime:  f.path("etc/localtime"),
		Serial:     f.path("dev/ttyS0"),
	}
	isBlockDevice = exists
	allowAnywhere = true
	config.RunDir = f.path("run/vos")
	config.LiveMedium = f.path("run/vos/medium/vos")
	config.KeysDir = f.path("keys")
	config.ImageInfoPath = f.path("usr/lib/vos/image.json")

	for _, d := range []string{"sys/class/block", "sys/dev/block", "dev/disk/by-label", "proc", "sys/firmware/efi", "run/vos/medium/vos", "keys", "etc"} {
		f.mkdir(d)
	}
	f.write("keys/test.pub", manifest.EncodePublicKey(testKey.Public().(ed25519.PublicKey)))
	f.write("usr/share/zoneinfo/Europe/Brussels", "TZif")
	f.write("usr/share/zoneinfo/UTC", "TZif")
	f.write("proc/swaps", "Filename\tType\tSize\tUsed\tPriority\n")
	f.write("dev/ttyS0", "")
	f.setMounts()
	return f
}

func (f *fakeSys) path(rel string) string { return filepath.Join(f.root, rel) }

func (f *fakeSys) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(f.path(rel), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) write(rel, content string) {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) symlink(target, rel string) {
	f.t.Helper()
	p := f.path(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.Remove(p)
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
}

// addDisk adds a whole disk. bus is part of its sysfs path ("ata1",
// "usb1", "nvme") and decides the transport sysTransport reports.
func (f *fakeSys) addDisk(name, majmin, bus string, size int64, model string) {
	dir := filepath.Join("sys/devices/pci0000:00", bus, "block", name)
	f.mkdir(filepath.Join(dir, "device"))
	f.mkdir(filepath.Join(dir, "holders"))
	f.write(filepath.Join(dir, "size"), strconv.FormatInt(size/512, 10))
	f.write(filepath.Join(dir, "ro"), "0")
	f.write(filepath.Join(dir, "removable"), "0")
	f.write(filepath.Join(dir, "device/model"), model+"  \n")
	f.symlink(f.path(dir), "sys/class/block/"+name)
	f.symlink(f.path(dir), "sys/dev/block/"+majmin)
	f.write("dev/"+name, "")
	f.disks[name] = dir
}

// addVirtual adds a block device without hardware (loop, dm, sr).
func (f *fakeSys) addVirtual(name, majmin string) string {
	dir := filepath.Join("sys/devices/virtual/block", name)
	f.mkdir(dir)
	f.symlink(f.path(dir), "sys/class/block/"+name)
	f.symlink(f.path(dir), "sys/dev/block/"+majmin)
	f.write("dev/"+name, "")
	return dir
}

func (f *fakeSys) addPart(disk, name, majmin string, n int, label string, size int64) {
	dir := filepath.Join(f.disks[disk], name)
	f.mkdir(filepath.Join(dir, "holders"))
	f.write(filepath.Join(dir, "partition"), strconv.Itoa(n))
	f.write(filepath.Join(dir, "size"), strconv.FormatInt(size/512, 10))
	f.write(filepath.Join(dir, "uevent"), fmt.Sprintf("DEVNAME=%s\nDEVTYPE=partition\nPARTN=%d\nPARTNAME=%s\n", name, n, label))
	f.symlink(f.path(dir), "sys/class/block/"+name)
	f.symlink(f.path(dir), "sys/dev/block/"+majmin)
	// The node has a capacity, like a real partition (update.WriteRoot
	// refuses an image larger than its slot); sparse, and capped so a
	// test never holds gigabytes.
	f.write("dev/"+name, "")
	if err := os.Truncate(f.path("dev/"+name), min(size, fakePartCap)); err != nil {
		f.t.Fatal(err)
	}
}

// fakePartCap caps the size of a fake partition node.
const fakePartCap = 64 << 20

// slotHas reports whether the partition node at p starts with want.
func slotHas(t *testing.T, p string, want []byte) bool {
	t.Helper()
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(fh, got); err != nil {
		return false
	}
	return bytes.Equal(got, want)
}

// setImageInfo writes the running (live) image's image.json.
func (f *fakeSys) setImageInfo(version, channel string, debug bool) {
	f.t.Helper()
	b, _ := json.Marshal(config.ImageInfo{Version: version, Channel: channel, Debug: debug})
	f.write("usr/lib/vos/image.json", string(b))
}

func (f *fakeSys) removeParts(disk string) {
	for _, p := range partitions(disk) {
		os.RemoveAll(filepath.Join(f.path(f.disks[disk]), p.Name))
		os.Remove(f.path("sys/class/block/" + p.Name))
		os.Remove(f.path("dev/" + p.Name))
	}
}

// vosParts gives disk the four VaporOS partitions.
func (f *fakeSys) vosParts(disk string, major int, slotMiB int64) {
	sizes := []int64{espMiB * mib, slotMiB * mib, slotMiB * mib, 20 * gib}
	for i, label := range partNames {
		f.addPart(disk, fmt.Sprintf("%s%d", disk, i+1), fmt.Sprintf("%d:%d", major, i+1), i+1, label, sizes[i])
	}
}

// setMounts writes mountinfo; each entry is "majmin mountpoint fstype source".
func (f *fakeSys) setMounts(entries ...string) {
	var b strings.Builder
	b.WriteString("22 1 0:21 / / rw,relatime shared:1 - overlay overlay rw,lowerdir=/run/vos/lower\n")
	for i, e := range entries {
		fl := strings.Fields(e)
		fmt.Fprintf(&b, "%d 22 %s / %s rw,relatime shared:%d - %s %s rw\n", 30+i, fl[0], fl[1], i+2, fl[2], fl[3])
	}
	f.write("proc/mountinfo", b.String())
}

// mediumMount is the mountpoint of the live medium.
func (f *fakeSys) mediumMount() string { return filepath.Dir(config.LiveMedium) }

// image is a signed test image.
type image struct {
	dir  string
	man  manifest.Manifest
	root []byte
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// testKey signs test images; newFakeSys trusts its public half.
var testKey = func() ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}()

// writeImage writes root.erofs, vmlinuz, initramfs.img and a signed
// manifest to dir.
func writeImage(t *testing.T, dir string, rootSize int) *image {
	t.Helper()
	img := &image{dir: dir, root: randomBytes(t, rootSize)}
	kernel, initrd := randomBytes(t, 70_001), randomBytes(t, 150_003)
	files := map[string][]byte{"root.erofs": img.root, "vmlinuz": kernel, "initramfs.img": initrd}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	art := func(name string) manifest.Artifact {
		return manifest.Artifact{Name: name, Size: int64(len(files[name])), SHA256: sha(files[name])}
	}
	img.man = manifest.Manifest{
		Schema: 1, Product: "vaporos", Version: "20260929.123456", RollbackIndex: 1790690000,
		Channel: "testing", Kernel: "7.2.8-1-cachyos", MinUpdater: 1,
		Cmdline: "quiet loglevel=3 console=ttyS0,115200",
		Artifacts: map[string]manifest.Artifact{
			"root": art("root.erofs"), "kernel": art("vmlinuz"), "initrd": art("initramfs.img"),
		},
	}
	img.sign(t)
	return img
}

// sign (re)writes manifest.json and its signature from img.man.
func (img *image) sign(t *testing.T) {
	t.Helper()
	b, _ := json.Marshal(img.man)
	os.WriteFile(filepath.Join(img.dir, "manifest.json"), b, 0o644)
	os.WriteFile(filepath.Join(img.dir, "manifest.json.sig"), manifest.Sign(b, testKey), 0o644)
}

// exitErr is a Runner error with an exit status, like *exec.ExitError.
type exitErr struct{ code int }

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitErr) ExitCode() int { return e.code }

// fakeRunner records commands and simulates the ones whose effects the
// installer observes: sgdisk (new partitions in sysfs), mount of the
// erofs slot (an image tree) and mount/umount bookkeeping.
type fakeRunner struct {
	f       *fakeSys
	mu      sync.Mutex
	calls   []string
	mounted []string
	// hook runs first; handled=true skips the default behaviour.
	hook func(name string, args []string) (out string, err error, handled bool)
}

func (f *fakeSys) runner() *fakeRunner { return &fakeRunner{f: f} }

func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	r.mu.Unlock()
	if r.hook != nil {
		if out, err, handled := r.hook(name, args); handled {
			return out, err
		}
	}
	switch name {
	case "sgdisk":
		if strings.HasPrefix(args[0], "-n1:") {
			r.partition(args)
		}
	case "mount":
		target := args[len(args)-1]
		if len(args) > 1 && args[0] == "-t" && args[1] == "erofs" {
			r.f.populateImage(target)
		}
		r.mu.Lock()
		r.mounted = append(r.mounted, target)
		r.mu.Unlock()
	case "umount":
		target := args[len(args)-1]
		r.mu.Lock()
		defer r.mu.Unlock()
		for i := len(r.mounted) - 1; i >= 0; i-- {
			if r.mounted[i] == target {
				r.mounted = append(r.mounted[:i], r.mounted[i+1:]...)
				return "", nil
			}
		}
		return "", fmt.Errorf("umount: %s: not mounted", target)
	}
	return "", nil
}

// partition simulates sgdisk creating the VaporOS table.
func (r *fakeRunner) partition(args []string) {
	disk := filepath.Base(args[len(args)-1])
	var slot int64
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "-n2:0:+"); ok {
			slot, _ = strconv.ParseInt(strings.TrimSuffix(v, "M"), 10, 64)
		}
	}
	r.f.removeParts(disk)
	r.f.vosParts(disk, 8, slot)
}

// populateImage lays out the parts of the erofs root the installer uses.
func (f *fakeSys) populateImage(root string) {
	for _, d := range []string{"etc", "var", "state", "usr/lib/vos"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "etc/hostname"), []byte("vapor\n"), 0o644)
	os.WriteFile(filepath.Join(root, "usr/lib/vos/cmdline"), []byte("quiet from-image\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "usr/share/zoneinfo/Europe"), 0o755)
	os.WriteFile(filepath.Join(root, "usr/share/zoneinfo/Europe/Brussels"), []byte("TZif"), 0o644)
}

func (r *fakeRunner) callList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.calls...)
}

func (r *fakeRunner) called(prefix string) bool {
	for _, c := range r.callList() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// relCalls shows calls with the fake root replaced by "@".
func (r *fakeRunner) relCalls() []string {
	var out []string
	for _, c := range r.callList() {
		out = append(out, strings.ReplaceAll(c, r.f.root, "@"))
	}
	return out
}

// env wires the fake machine into an installer environment.
func (f *fakeSys) env(r Runner) *env {
	return &env{
		run:               r,
		scanDisks:         func(context.Context) ([]storage.Disk, error) { return f.scan, nil },
		gpu:               func() display.GPUInfo { return f.gpu },
		chooseConnector:   func() (string, error) { return "DP-1", nil },
		machineCmdlineFor: func(c string) string { return "video=" + c + ":e drm.edid_firmware=" + c + ":edid/vaporos.bin" },
		bootCmdline: func(slot, image, machine string) string {
			return strings.Join(strings.Fields("vos.slot="+slot+" "+image+" "+machine), " ")
		},
		writeLoaderConf: func(esp string) error {
			os.MkdirAll(filepath.Join(esp, "loader"), 0o755)
			return os.WriteFile(filepath.Join(esp, "loader/loader.conf"), []byte("timeout 0\n"), 0o644)
		},
		installEntry: func(esp, version, slot, srcDir, options string, tries int) error {
			f.mu.Lock()
			f.entry = entryCall{esp, version, slot, srcDir, options, tries}
			f.entryN++
			f.mu.Unlock()
			dst := filepath.Join(esp, "vos", version)
			os.MkdirAll(dst, 0o755)
			for _, n := range []string{"vmlinuz", "initramfs.img"} {
				b, err := os.ReadFile(filepath.Join(srcDir, n))
				if err != nil {
					return err
				}
				os.WriteFile(filepath.Join(dst, n), b, 0o644)
			}
			os.MkdirAll(filepath.Join(esp, "loader/entries"), 0o755)
			return os.WriteFile(filepath.Join(esp, "loader/entries/vos-"+version+".conf"), []byte("options "+options+"\n"), 0o644)
		},
		setMachineCmdline: func(root, cmdline string) error {
			return config.WriteFileAtomic(filepath.Join(root, config.MachineCmdlinePath()), []byte(cmdline+"\n"), 0o644)
		},
		setAdminPassword: func(root, password string) error {
			f.mu.Lock()
			f.adminPass = password
			f.mu.Unlock()
			return config.WriteJSONAtomic(filepath.Join(root, config.AuthPath()), map[string]string{"user": "admin"}, 0o600)
		},
		sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		logf:  f.t.Logf,
	}
}

type progressRec struct {
	step    string
	percent int
	message string
}

func recorder(recs *[]progressRec) Progress {
	var mu sync.Mutex
	return func(step string, percent int, message string) {
		mu.Lock()
		*recs = append(*recs, progressRec{step, percent, message})
		mu.Unlock()
	}
}

// recorder is the package recorder that also runs f.onStep.
func (f *fakeSys) recorder(recs *[]progressRec) Progress {
	rec := recorder(recs)
	return func(step string, percent int, message string) {
		if f.onStep != nil {
			f.onStep(step)
		}
		rec(step, percent, message)
	}
}
