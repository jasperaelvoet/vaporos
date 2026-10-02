package install

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// withExtensions gives img a core extension in its signed manifest.
func (img *image) withExtensions(t *testing.T) *image {
	t.Helper()
	img.man.Extensions = map[string]manifest.Extension{"proton": {
		Name: "ext-proton.raw", Size: 4096, SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64), Core: true,
	}}
	img.sign(t)
	return img
}

const seedPrefix = "@/run/vos/target/root/usr/bin/vos ext fetch --state-dir @/run/vos/target/root/var/lib/vos"

func TestInstallSeedsExtensions(t *testing.T) {
	f, img := installMachine(t)
	img.withExtensions(t)
	f.seed = func(ctx context.Context, args []string, stdout func(string)) error {
		stdout(`{"bytes":0,"total":4096}`)
		stdout("fetching proton")
		stdout(`{"bytes":4096,"total":4096}`)
		return nil
	}
	r := f.runner()
	var recs []progressRec
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, recorder(&recs)); err != nil {
		t.Fatal(err)
	}
	// The live medium carries no images: the new system's update source,
	// at this version.
	want := seedPrefix + " --from " + config.DefaultUpdateSrc + " --version " + img.man.Version + " --seed"
	if len(f.seeds) != 1 || f.seeds[0] != want {
		t.Fatalf("seeds %q, want %q", f.seeds, want)
	}
	if got := strings.Join(stepsOf(recs), ","); got != "probe,partition,write,verify,bootloader,configure,done" {
		t.Fatalf("steps %s", got)
	}
	seen := false
	for i, rec := range recs {
		if i > 0 && rec.percent < recs[i-1].percent {
			t.Fatalf("progress went backwards: %v -> %v", recs[i-1], rec)
		}
		if rec.step == StepConfigure && strings.HasPrefix(rec.message, "Adding extensions (") && rec.percent == 98 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no extension progress: %+v", recs)
	}
}

// From a directory, http(s) or a registry, the images come from the same
// place as the image.
func TestInstallSeedsFromTheSource(t *testing.T) {
	f, _ := installMachine(t)
	dir := t.TempDir()
	img := writeImage(t, dir, 1<<20).withExtensions(t)
	r := f.runner()
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda", Source: dir}, nil); err != nil {
		t.Fatal(err)
	}
	want := seedPrefix + " --from " + strings.ReplaceAll(dir, f.root, "@") + " --version " + img.man.Version + " --seed"
	if len(f.seeds) != 1 || f.seeds[0] != want {
		t.Fatalf("seeds %q, want %q", f.seeds, want)
	}
}

// Offline, slow or failing: the install goes on without extensions.
func TestInstallSeedFailureIsNotFatal(t *testing.T) {
	for name, seed := range map[string]func(ctx context.Context, args []string, stdout func(string)) error{
		"fails": func(context.Context, []string, func(string)) error { return errors.New("exit status 1") },
		"too slow": func(ctx context.Context, _ []string, _ func(string)) error {
			<-ctx.Done()
			return ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, img := installMachine(t)
			img.withExtensions(t)
			seedTimeout = 20 * time.Millisecond
			f.seed = seed
			r := f.runner()
			var recs []progressRec
			if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda"}, recorder(&recs)); err != nil {
				t.Fatal(err)
			}
			if last := recs[len(recs)-1]; last.step != StepDone {
				t.Fatalf("last progress %+v", last)
			}
		})
	}
}

// Cancelling the install while it seeds cancels the install.
func TestInstallCancelledWhileSeeding(t *testing.T) {
	f, img := installMachine(t)
	img.withExtensions(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.seed = func(sctx context.Context, _ []string, _ func(string)) error {
		cancel()
		<-sctx.Done()
		return sctx.Err()
	}
	r := f.runner()
	err := runInstall(ctx, f.env(r), Options{Disk: "sda"}, nil)
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), StepConfigure+": ") {
		t.Fatalf("err = %v", err)
	}
	if len(r.mounted) != 0 {
		t.Errorf("mounted: %v", r.mounted)
	}
}

// A repair rewrites slot a's catalog and resets the extension state
// (`--repair`); slot b's catalog, the pending set and the failed sets are
// gone before it runs, so an offline repair drops them too.
func TestInstallRepairResetsExtensions(t *testing.T) {
	f := newFakeSys(t)
	f.addDisk("sda", "8:0", "ata1", 64*gib, "SSD")
	f.vosParts("sda", 8, 8192)
	writeImage(t, config.LiveMedium, 1<<20)
	r := f.runner()
	var ext string
	r.hook = func(name string, args []string) (string, error, bool) {
		if name == "mount" && args[0] == "--bind" {
			vos := filepath.Join(args[2], "lib/vos")
			ext = filepath.Join(vos, "ext")
			config.WriteJSONAtomic(filepath.Join(vos, "config.json"),
				map[string]any{"schema": 1, "update": map[string]any{"source": "http://lan:8000/vos/"}}, 0o644)
			for _, p := range []string{"slots/a.json", "slots/b.json", "sets/4/ids", "failed", "wanted"} {
				os.MkdirAll(filepath.Join(ext, filepath.Dir(p)), 0o755)
				os.WriteFile(filepath.Join(ext, p), []byte("x\n"), 0o644)
			}
			os.Symlink("sets/4", filepath.Join(ext, "pending"))
			os.Symlink("sets/4", filepath.Join(ext, "enabled"))
		}
		return "", nil, false
	}
	f.seed = func(ctx context.Context, args []string, stdout func(string)) error {
		for _, p := range []string{"slots/b.json", "pending", "failed"} {
			if _, err := os.Lstat(filepath.Join(ext, p)); !os.IsNotExist(err) {
				t.Errorf("%s still there when seeding starts: %v", p, err)
			}
		}
		return errors.New("offline")
	}
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda", Mode: ModeRepair}, nil); err != nil {
		t.Fatal(err)
	}
	// Even an image without extensions resets them on a repair.
	want := seedPrefix + " --from http://lan:8000/vos/ --version 20260929.123456 --seed --repair"
	if len(f.seeds) != 1 || f.seeds[0] != want {
		t.Fatalf("seeds %q, want %q", f.seeds, want)
	}
	for _, p := range []string{"slots/a.json", "wanted", "enabled", "sets/4/ids"} {
		if _, err := os.Lstat(filepath.Join(ext, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{fn: func(s string) { lines = append(lines, s) }}
	w.Write([]byte(`{"bytes":1,`))
	w.Write([]byte("\"total\":2}\nsecond\nthi"))
	w.Write([]byte("rd"))
	w.flush()
	if got := strings.Join(lines, "|"); got != `{"bytes":1,"total":2}|second|third` {
		t.Fatalf("lines %q", got)
	}
}

func TestRunLines(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	var out, errs []string
	err = runLines(context.Background(), sh, []string{"-c", `echo '{"bytes":1,"total":1}'; echo oops >&2; exit 3`},
		func(s string) { out = append(out, s) }, func(s string) { errs = append(errs, s) })
	if err == nil || len(out) != 1 || out[0] != `{"bytes":1,"total":1}` || len(errs) != 1 || errs[0] != "oops" {
		t.Fatalf("err %v, stdout %q, stderr %q", err, out, errs)
	}
}
