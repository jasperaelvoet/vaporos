package update

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// runCLI runs a vos subcommand and captures its output.
func runCLI(t *testing.T, cmd string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &out, &errOut
	defer func() { stdout, stderr = oldOut, oldErr }()
	code := CLI(cmd, args)
	return code, out.String(), errOut.String()
}

func TestKeygenSignVerify(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	prefix := filepath.Join(dir, "release")
	if code, out, errOut := runCLI(t, "keygen", "--out", prefix); code != 0 {
		t.Fatalf("keygen: %d %s %s", code, out, errOut)
	}
	if fi, err := os.Stat(prefix + ".key"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	// Never overwrite a key.
	if code, _, _ := runCLI(t, "keygen", "--out", prefix); code != 1 {
		t.Fatalf("keygen over an existing key: %d", code)
	}

	img := e.makeImage(newVersion, 200, 1000, nil)
	mf := filepath.Join(dir, "manifest.json")
	os.WriteFile(mf, img.files["manifest.json"], 0o644)
	// Flags after the file work too.
	if code, out, errOut := runCLI(t, "sign", mf, "--key", prefix+".key"); code != 0 {
		t.Fatalf("sign: %d %s %s", code, out, errOut)
	}
	sig, _ := os.ReadFile(mf + ".sig")
	keys := t.TempDir()
	pub, _ := os.ReadFile(prefix + ".pub")
	os.WriteFile(filepath.Join(keys, "release.pub"), pub, 0o644)
	if err := manifest.Verify(img.files["manifest.json"], sig, keys); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// From the environment, as CI does.
	key, _ := os.ReadFile(prefix + ".key")
	t.Setenv("VOS_SIGNING_KEY", string(key))
	os.Remove(mf + ".sig")
	if code, _, errOut := runCLI(t, "sign", "--key", "env:VOS_SIGNING_KEY", mf); code != 0 {
		t.Fatalf("sign from env: %s", errOut)
	}
	if again, _ := os.ReadFile(mf + ".sig"); !bytes.Equal(again, sig) {
		t.Fatal("ed25519 signatures should be deterministic")
	}

	// It refuses to sign a manifest `vos update` would reject.
	os.WriteFile(mf, []byte(`{"schema":1}`), 0o644)
	if code, _, _ := runCLI(t, "sign", "--key", prefix+".key", mf); code != 1 {
		t.Fatalf("signed an invalid manifest: %d", code)
	}
	if code, _, _ := runCLI(t, "sign", mf); code != 2 {
		t.Fatalf("sign without --key: %d", code)
	}
}

func TestStatus(t *testing.T) {
	e := setup(t)
	e.setState(&State{Failed: []string{"20260815.000000"}})
	code, out, errOut := runCLI(t, "status")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{
		"VaporOS " + bootedVersion,
		"slot a:  " + bootedVersion + "  <- running",
		"slot b:  " + oldIdleVersion,
		"failed:  20260815.000000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	code, out, _ = runCLI(t, "status", "--json")
	var s Status
	if code != 0 || json.Unmarshal([]byte(out), &s) != nil {
		t.Fatalf("%d %s", code, out)
	}
	if s.Version != bootedVersion || s.BootedSlot != "a" || s.Slots["a"] == nil || !s.Slots["a"].Running ||
		s.Slots["b"].Version != oldIdleVersion || s.Mode != "os" || len(s.Failed) != 1 {
		t.Fatalf("status %+v", s)
	}

	e.write(config.ProcCmdline, "vos.mode=live\n")
	if _, out, _ := runCLI(t, "status"); !strings.Contains(out, "live") {
		t.Fatalf("live status:\n%s", out)
	}
}

func TestUpdateCheckCLI(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	src := e.srcDir(img)
	code, out, errOut := runCLI(t, "update", "--check", "--from", src)
	if code != 0 || !strings.Contains(out, "Update available: "+newVersion) {
		t.Fatalf("%d\n%s\n%s", code, out, errOut)
	}
	code, out, _ = runCLI(t, "update", "--check", "--json-progress", "--from", src)
	var res map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || res["offered"] != newVersion {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, _ := runCLI(t, "update", "--stage-only", "--reboot"); code != 2 {
		t.Fatalf("conflicting flags: %d", code)
	}
	if code, _, _ := runCLI(t, "update", "extra"); code != 2 {
		t.Fatalf("stray argument: %d", code)
	}
	if code, _, _ := runCLI(t, "frobnicate"); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}
}
