package buildcheck

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// fakeImage is enough of an erofs for the catalog: its magic.
func fakeImage(id string) []byte {
	b := make([]byte, 3*4096+17)
	binary.LittleEndian.PutUint32(b[erofsMagicAt:], erofsMagic)
	copy(b[8192:], "image of "+id)
	return b
}

// protonDescriptorText is the repository's own Proton descriptor.
var protonDescriptorText = func() string {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "extensions", "proton", "extension.json"))
	if err != nil {
		panic(err)
	}
	return string(b)
}()

var demoRequiresProton = strings.Replace(demoDescriptor, `"permissions"`, `"requires": ["proton"], "permissions"`, 1)

// newStage stages proton (the repository's descriptor) and demo, which
// requires it.
func newStage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"ext-proton.raw":      string(fakeImage("proton")),
		"proton.json":         protonDescriptorText,
		"proton.build.json":   `{"permissions":["modules","compat-tool"],"runs_as_root":false,"warnings":[]}`,
		"proton.key":          "0123456789abcdef0123456789abcdef\n",
		"proton.packages.txt": "proton-cachyos-slr 1:10.0-20250906-1\n\numu-launcher  1.2.6-1\n",
		"ext-demo.raw":        string(fakeImage("demo")),
		"demo.json":           demoRequiresProton,
		"demo.build.json":     `{"permissions":["service"],"runs_as_root":true,"warnings":["a warning"]}`,
		"notes.txt":           "ignored",
	})
	return dir
}

// The fake images' digests, from sha256sum and an independent fs-verity
// implementation.
const (
	protonSHA = "34247581cd2c385ac4537470a107351f0749dfecbe6d0e17ec4db30b52e42aa9"
	protonFSV = "ffb6ed4a40902de2ce1a5e85ea7f7bbc8452322c45c8993d9bf2b3196666e334"
	demoSHA   = "812cbfb3a03b80d8b7682aa5dbc390a278c42bc6f5be8dedb646fe579df3b4e6"
	demoFSV   = "394b83980d20d1869e507affa2abcd757adb1694be8470f934c8944968724c37"
)

func TestCatalogFromStage(t *testing.T) {
	out := t.TempDir()
	writeTree(t, out, map[string]string{"descriptors/old.json": "{}", "descriptors/keep.txt": "x"})
	s, err := ReadStage(newStage(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(out); err != nil {
		t.Fatal(err)
	}

	list, err := os.ReadFile(filepath.Join(out, CatalogFile))
	if err != nil {
		t.Fatal(err)
	}
	const size = 12305
	want := "# VaporOS extension catalog (docs/CONTRACTS.md \"Extensions\"). Written by the build.\n" +
		"dispatcher 1\n" +
		"ext proton " + protonSHA + " 12305 " + protonFSV + " core\n" +
		"ext demo " + demoSHA + " 12305 " + demoFSV + " - proton\n"
	if string(list) != want {
		t.Fatalf("extensions.list:\n%s\nwant:\n%s", list, want)
	}
	if c, err := catalog.Load(filepath.Join(out, CatalogFile)); err != nil || c.Dispatcher != catalog.Dispatcher {
		t.Fatalf("catalog does not load: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(out, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var exts map[string]manifest.Extension
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&exts); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		Schema: manifest.Schema, Product: manifest.Product, Version: "20261002.120000", MinUpdater: 1,
		Artifacts: map[string]manifest.Artifact{
			manifest.Root:   {Name: "root.erofs", Size: 1, SHA256: protonSHA},
			manifest.Kernel: {Name: "vmlinuz", Size: 1, SHA256: protonSHA},
			manifest.Initrd: {Name: "initramfs.img", Size: 1, SHA256: protonSHA},
		},
		Extensions: exts,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("the manifest entries do not validate: %v", err)
	}
	wantExts := map[string]manifest.Extension{
		"proton": {Name: "ext-proton.raw", Size: int64(size), SHA256: protonSHA, FSVerity: protonFSV, Core: true, Key: "0123456789abcdef0123456789abcdef"},
		"demo":   {Name: "ext-demo.raw", Size: int64(size), SHA256: demoSHA, FSVerity: demoFSV, Requires: []string{"proton"}},
	}
	if !reflect.DeepEqual(exts, wantExts) {
		t.Fatalf("extensions.json:\n%+v\nwant\n%+v", exts, wantExts)
	}
	if bytes.Contains(raw, []byte(`"core": false`)) || bytes.Contains(raw, []byte(`"key": ""`)) {
		t.Fatalf("empty optional fields written:\n%s", raw)
	}

	p, err := descriptor.Load(filepath.Join(out, DescriptorsDir, "proton.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantBuild := &descriptor.Build{
		Size:        int64(size),
		Packages:    []string{"proton-cachyos-slr 1:10.0-20250906-1", "umu-launcher 1.2.6-1"},
		Permissions: []string{"modules", "compat-tool"},
	}
	if !reflect.DeepEqual(p.Build, wantBuild) || p.ID != "proton" || !p.Core || p.Steam.DefaultCompatTool != "proton-cachyos-slr" {
		t.Fatalf("proton descriptor: %+v build %+v", p, p.Build)
	}
	d, err := descriptor.Load(filepath.Join(out, DescriptorsDir, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Build == nil || !d.Build.RunsAsRoot || d.Build.Packages != nil || !reflect.DeepEqual(d.Build.Permissions, []string{"service"}) {
		t.Fatalf("demo build %+v", d.Build)
	}
	if _, err := os.Stat(filepath.Join(out, DescriptorsDir, "old.json")); !os.IsNotExist(err) {
		t.Fatal("a stale descriptor stayed")
	}
	if _, err := os.Stat(filepath.Join(out, DescriptorsDir, "keep.txt")); err != nil {
		t.Fatal("removed a file that is not a descriptor")
	}
}

func TestEmptyStage(t *testing.T) {
	out := t.TempDir()
	s, err := ReadStage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, ManifestFile)); string(b) != "{}\n" {
		t.Fatalf("extensions.json %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, CatalogFile)); !strings.HasSuffix(string(b), "dispatcher 1\n") {
		t.Fatalf("extensions.list %q", b)
	}
}

func TestStageRejects(t *testing.T) {
	for name, c := range map[string]struct {
		files  map[string]string
		remove string
		want   string
	}{
		"no build result":     {remove: "demo.build.json", want: "demo.build.json"},
		"no descriptor":       {remove: "demo.json", want: "demo.json"},
		"orphan descriptor":   {files: map[string]string{"ghost.json": "{}"}, want: "ghost.json: no ext-ghost.raw"},
		"orphan build result": {files: map[string]string{"ghost.build.json": "{}"}, want: "ghost.build.json: no ext-ghost.raw"},
		"bad id":              {files: map[string]string{"ext-Bad.raw": "x"}, want: `invalid extension id "Bad"`},
		"permissions differ":  {files: map[string]string{"demo.build.json": `{"permissions":["service","udev"],"runs_as_root":false,"warnings":[]}`}, want: "the build verified permissions"},
		"unknown permission":  {files: map[string]string{"demo.build.json": `{"permissions":["service","root"],"runs_as_root":false,"warnings":[]}`}, want: `unknown permission "root"`},
		"foreign build json":  {files: map[string]string{"demo.build.json": `{"permissions":["service"],"ok":true}`}, want: "unknown field"},
		"not erofs":           {files: map[string]string{"ext-demo.raw": "just bytes"}, want: "not an erofs image"},
		"wrong descriptor":    {files: map[string]string{"demo.json": strings.Replace(demoDescriptor, `"id": "demo"`, `"id": "other"`, 1)}, want: `is the descriptor of "other"`},
		"source with build":   {files: map[string]string{"demo.json": strings.Replace(demoRequiresProton, `"schema": 1,`, `"schema": 1, "build": {"size": 1, "permissions": []},`, 1)}, want: "written by the build"},
		"missing requirement": {files: map[string]string{"demo.json": strings.Replace(demoRequiresProton, `["proton"]`, `["proton", "ghost"]`, 1)}, want: `requires "ghost"`},
		"cycle": {files: map[string]string{
			"proton.json": strings.Replace(protonDescriptorText, `"packages"`, `"requires": ["demo"], "packages"`, 1),
		}, want: "cycle"},
		"bad key": {files: map[string]string{"demo.key": "not-hex\n"}, want: "invalid key"},
		"two defaults": {files: map[string]string{
			"demo.json": strings.Replace(demoRequiresProton, `"schema": 1,`, `"schema": 1, "core": true, "steam": {"default_compat_tool": "other"},`, 1),
		}, want: "both set Steam's default compatibility tool"},
		"one app forced twice": {files: map[string]string{
			"demo.json":   strings.Replace(demoRequiresProton, `"schema": 1,`, `"schema": 1, "steam": {"compat_tool": "x", "force_compat_tool": [227300]},`, 1),
			"proton.json": strings.Replace(protonDescriptorText, `"default_compat_tool": "proton-cachyos-slr"`, `"default_compat_tool": "proton-cachyos-slr", "compat_tool": "y", "force_compat_tool": [227300]`, 1),
		}, want: "both force a compatibility tool on app 227300"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := newStage(t)
			if c.remove != "" {
				os.Remove(filepath.Join(dir, c.remove))
			}
			writeTree(t, dir, c.files)
			_, err := ReadStage(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want %q", err, c.want)
			}
		})
	}
	if _, err := ReadStage(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing stage read")
	}
}

// The repository's Proton descriptor against a tree shaped like its
// packages: the compatibility tool (whose programs run in Steam's runtime,
// so it is elf_exempt), ntsync's modules-load.d and umu-launcher.
func TestProtonShapedTree(t *testing.T) {
	d := parseDescriptor(t, protonDescriptorText)
	tree := t.TempDir()
	writeTree(t, tree, map[string]string{
		"usr/share/steam/compatibilitytools.d/proton-cachyos-slr/compatibilitytool.vdf": `"compatibilitytools" {}`,
		"usr/share/steam/compatibilitytools.d/proton-cachyos-slr/files/bin/wine64":      lib64("libsteamruntimeonly.so.1"),
		"usr/lib/modules-load.d/ntsync.conf":                                            "ntsync\n",
		"usr/bin/umu-run":                                                               "#!/usr/bin/python3\n",
		"usr/lib/vos/ext/proton/extension.json":                                         protonDescriptorText,
		"usr/lib/vos/ext/proton/packages.txt":                                           "proton-cachyos-slr 1:10.0-1\n",
		"usr/lib/vos/ext/proton/module-options":                                         "",
	})
	r := run(t, tree, newBase(t), d)
	wantClean(t, r)
	if !reflect.DeepEqual(r.Permissions, []string{"modules", "compat-tool"}) || r.RunsAsRoot {
		t.Fatalf("result %+v", r.Result)
	}
}

func TestLowerdirLen(t *testing.T) {
	if n := lowerdirLen(nil); n != len("/new_root/usr") {
		t.Fatal(n)
	}
	if n := lowerdirLen([]string{"a", "proton"}); n != len("/run/vos/x/1/usr:/run/vos/x/proton/usr:/new_root/usr") {
		t.Fatal(n)
	}
	var ids []string
	for i := 0; i < 200; i++ {
		ids = append(ids, strings.Repeat("x", 32))
	}
	if lowerdirLen(ids) <= maxLowerdir {
		t.Fatal("200 long ids fit")
	}
}
