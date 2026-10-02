package extensions

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/buildcheck"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

const cliDescriptor = `{"schema": 1, "id": "demo", "name": "Demo", "summary": "A test extension.", "category": "app",
  "upstream": {"name": "Demo", "url": "https://example.com/demo", "license": "MIT"}}`

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// unprivileged drops check-tree's warning that it cannot see trusted.*
// attributes, which depends on who runs the tests.
func unprivileged[T any](ws []T) []T {
	var out []T
	for _, w := range ws {
		if !strings.Contains(fmt.Sprint(w), "CAP_SYS_ADMIN") {
			out = append(out, w)
		}
	}
	return out
}

func TestBuildCommandsAreRegistered(t *testing.T) {
	for _, name := range []string{"check-tree", "catalog", "digest", "validate"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("vos ext %s is not registered", name)
		}
	}
}

func TestCheckTreeCommand(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "extension.json")
	tree, base := filepath.Join(dir, "tree"), filepath.Join(dir, "base")
	write(t, dir, map[string]string{
		"extension.json":                           cliDescriptor,
		"tree/usr/share/demo/readme":               "hi",
		"tree/usr/lib/vos/ext/demo/extension.json": cliDescriptor,
		"tree/usr/lib/vos/ext/demo/packages.txt":   "",
		"tree/usr/lib/vos/ext/demo/module-options": "",
		"base/usr/bin/vos":                         "vos",
		"other/usr/share/other/readme":             "other",
	})
	out := filepath.Join(dir, "demo.build.json")
	var stderr bytes.Buffer
	args := []string{"--id", "demo", "--descriptor", desc, "--tree", tree, "--base", base, "--other", filepath.Join(dir, "other"), "--json", out}
	if rc := checkTreeCmd(args, &stderr); rc != 0 {
		t.Fatalf("exit %d: %s", rc, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got["runs_as_root"] != false || len(got["permissions"].([]any)) != 0 || len(unprivileged(got["warnings"].([]any))) != 0 {
		t.Fatalf("--json wrote %s", b)
	}

	write(t, dir, map[string]string{"tree/etc/x": "x", "tree/usr/bin/vos": "mine"})
	os.Remove(out)
	stderr.Reset()
	if rc := checkTreeCmd(args, &stderr); rc != 1 {
		t.Fatalf("exit %d for a bad tree", rc)
	}
	lines := unprivileged(strings.Split(strings.TrimSpace(stderr.String()), "\n"))
	if len(lines) != 2 || lines[0] != "demo: etc: outside usr/" || lines[1] != "demo: usr/bin/vos: the base already ships it" {
		t.Fatalf("stderr %q", lines)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("--json written for a tree that failed")
	}

	for _, bad := range [][]string{nil, {"--id", "demo"}, {"--bogus"}, append(args[:8:8], "extra")} {
		if rc := checkTreeCmd(bad, &bytes.Buffer{}); rc != 2 {
			t.Errorf("%q: exit %d, want 2", bad, rc)
		}
	}
	if rc := checkTreeCmd([]string{"--id", "demo", "--descriptor", filepath.Join(dir, "missing.json"), "--tree", tree, "--base", base}, &bytes.Buffer{}); rc != 1 {
		t.Errorf("a missing descriptor: exit %d", rc)
	}
}

func TestCatalogCommand(t *testing.T) {
	stage, out := t.TempDir(), filepath.Join(t.TempDir(), "out")
	img := make([]byte, 4096)
	binary.LittleEndian.PutUint32(img[1024:], 0xE0F5E1E2)
	write(t, stage, map[string]string{
		"ext-demo.raw":    string(img),
		"demo.json":       cliDescriptor,
		"demo.build.json": `{"permissions":[],"runs_as_root":false,"warnings":[]}`,
	})
	var stderr bytes.Buffer
	if rc := catalogCmd([]string{"--stage", stage, "--out", out}, &stderr); rc != 0 {
		t.Fatalf("exit %d: %s", rc, stderr.String())
	}
	c, err := catalog.Load(filepath.Join(out, buildcheck.CatalogFile))
	if err != nil || len(c.Entries) != 1 || c.Entries[0].ID != "demo" || c.Entries[0].Size != 4096 {
		t.Fatalf("catalog %+v %v", c, err)
	}
	for _, f := range []string{buildcheck.ManifestFile, filepath.Join(buildcheck.DescriptorsDir, "demo.json")} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatal(err)
		}
	}

	write(t, stage, map[string]string{"ghost.json": "{}", "demo.build.json": `{"bogus": 1}`})
	stderr.Reset()
	if rc := catalogCmd([]string{"--stage", stage, "--out", out}, &stderr); rc != 1 || strings.Count(stderr.String(), "catalog: ") < 2 {
		t.Fatalf("exit %d: %q", rc, stderr.String())
	}
	if rc := catalogCmd([]string{"--stage", stage}, &bytes.Buffer{}); rc != 2 {
		t.Fatalf("usage: exit %d", rc)
	}
}

func TestValidateCommand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"good/extension.json":    cliDescriptor,
		"unknown/extension.json": strings.Replace(cliDescriptor, `"schema": 1,`, `"schema": 1, "bogus": true,`, 1),
		"invalid/extension.json": strings.Replace(strings.Replace(cliDescriptor, `"id": "demo"`, `"id": "Demo"`, 1), `"name": "Demo", `, "", 1),
		"built/extension.json":   strings.Replace(cliDescriptor, `"schema": 1,`, `"schema": 1, "build": {"size": 1, "permissions": []},`, 1),
	})
	file := func(name string) string { return filepath.Join(dir, name, "extension.json") }
	var stderr bytes.Buffer
	if rc := validateCmd([]string{file("good"), file("good")}, &stderr); rc != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d: %q", rc, stderr.String())
	}
	missing := filepath.Join(dir, "missing.json")
	if rc := validateCmd([]string{file("unknown"), file("good"), file("invalid"), file("built"), missing}, &stderr); rc != 1 {
		t.Fatalf("exit %d", rc)
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	want := []string{
		file("unknown") + `: json: unknown field "bogus"`,
		file("invalid") + ": ",
		file("invalid") + ": ",
		file("built") + ": build is written by the build, not by hand",
		missing + ": no such file or directory",
	}
	if len(lines) != len(want) {
		t.Fatalf("stderr %q", lines)
	}
	for i := range want {
		if !strings.HasPrefix(lines[i], want[i]) || strings.Count(lines[i], file("invalid")) > 1 {
			t.Errorf("line %d: %q, want %q...", i, lines[i], want[i])
		}
	}
	if rc := validateCmd(nil, &bytes.Buffer{}); rc != 2 {
		t.Fatalf("no files: exit %d", rc)
	}
}

func TestDigestCommand(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	write(t, dir, map[string]string{"a": "a"})
	var stdout, stderr bytes.Buffer
	if rc := digestCmd([]string{a, a}, &stdout, &stderr); rc != 0 {
		t.Fatalf("exit %d: %s", rc, stderr.String())
	}
	line := "bce75948b9e7510293f8f2720412af9697c1479281323f3f220623fb8e94b557  " + a + "\n"
	if stdout.String() != line+line {
		t.Fatalf("stdout %q", stdout.String())
	}
	stdout.Reset()
	if rc := digestCmd([]string{filepath.Join(dir, "missing"), a}, &stdout, &stderr); rc != 1 || stdout.String() != line {
		t.Fatalf("exit %d, stdout %q", rc, stdout.String())
	}
	if rc := digestCmd(nil, &stdout, &stderr); rc != 2 {
		t.Fatalf("no files: exit %d", rc)
	}
}
