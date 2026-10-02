package manifest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validManifest() *Manifest {
	return &Manifest{
		Schema: 1, Product: "vaporos", Version: "20260929.123456", RollbackIndex: 1790690000,
		Channel: "main", Git: "abc", Created: "2026-09-29T12:34:56Z", Kernel: "7.2.8-1-cachyos",
		Cmdline: "quiet loglevel=3", MinUpdater: 1,
		Artifacts: map[string]Artifact{
			"root":   {Name: "root.erofs", Size: 123, SHA256: sum},
			"kernel": {Name: "vmlinuz", Size: 12, SHA256: sum},
			"initrd": {Name: "initramfs.img", Size: 34, SHA256: sum},
		},
	}
}

func encode(t *testing.T, m *Manifest) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseValid(t *testing.T) {
	m, err := Parse(encode(t, validManifest()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "20260929.123456" || m.Artifact(Root).Name != "root.erofs" || m.Has(Index) {
		t.Fatalf("parsed %+v", m)
	}
	withIndex := validManifest()
	withIndex.Artifacts[Index] = Artifact{Name: "root.erofs.idx", Size: 64, SHA256: sum}
	if m, err := Parse(encode(t, withIndex)); err != nil || !m.Has(Index) || m.Artifact(Index).Size != 64 {
		t.Fatalf("with an index: %+v, %v", m, err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]func(m *Manifest){
		"schema":         func(m *Manifest) { m.Schema = 2 },
		"product":        func(m *Manifest) { m.Product = "other" },
		"min_updater":    func(m *Manifest) { m.MinUpdater = UpdaterVersion + 1 },
		"empty version":  func(m *Manifest) { m.Version = "" },
		"slash version":  func(m *Manifest) { m.Version = "../etc" },
		"plus version":   func(m *Manifest) { m.Version = "1+3" },
		"dot version":    func(m *Manifest) { m.Version = ".hidden" },
		"long version":   func(m *Manifest) { m.Version = strings.Repeat("1", 65) },
		"rollback index": func(m *Manifest) { m.RollbackIndex = -1 },
		"cmdline break":  func(m *Manifest) { m.Cmdline = "quiet\nlinux /evil" },
		"no root":        func(m *Manifest) { delete(m.Artifacts, "root") },
		"no kernel":      func(m *Manifest) { delete(m.Artifacts, "kernel") },
		"no initrd":      func(m *Manifest) { delete(m.Artifacts, "initrd") },
		"bad name": func(m *Manifest) {
			a := m.Artifacts["kernel"]
			a.Name = "../vmlinuz"
			m.Artifacts["kernel"] = a
		},
		"zero size": func(m *Manifest) {
			a := m.Artifacts["root"]
			a.Size = 0
			m.Artifacts["root"] = a
		},
		"bad sha": func(m *Manifest) {
			a := m.Artifacts["initrd"]
			a.SHA256 = strings.ToUpper(sum)
			m.Artifacts["initrd"] = a
		},
		"bad index name": func(m *Manifest) { m.Artifacts["index"] = Artifact{Name: "/root.erofs.idx", Size: 64, SHA256: sum} },
		"empty index":    func(m *Manifest) { m.Artifacts["index"] = Artifact{Name: "root.erofs.idx", SHA256: sum} },
		"index sha":      func(m *Manifest) { m.Artifacts["index"] = Artifact{Name: "root.erofs.idx", Size: 64} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			mutate(m)
			if _, err := Parse(encode(t, m)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := Parse([]byte(`{"schema":1} {}`)); err == nil {
		t.Fatal("accepted trailing data")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("accepted garbage")
	}
}

func writeKey(t *testing.T, dir, name string, pub ed25519.PublicKey) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(EncodePublicKey(pub)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	// A stray malformed key must not stop the good one from verifying.
	os.WriteFile(filepath.Join(dir, "broken.pub"), []byte("nope"), 0o644)
	os.WriteFile(filepath.Join(dir, "README"), []byte("not a key"), 0o644)
	writeKey(t, dir, "dev.pub", otherPub)
	writeKey(t, dir, "release.pub", pub)

	b := encode(t, validManifest())
	sig := Sign(b, priv)
	if err := Verify(b, sig, dir); err != nil {
		t.Fatalf("valid signature: %v", err)
	}
	if err := Verify(b, Sign(b, otherPriv), dir); err != nil {
		t.Fatalf("second trusted key: %v", err)
	}

	tampered := append([]byte{}, b...)
	tampered[len(tampered)-2] ^= 1
	if err := Verify(tampered, sig, dir); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered manifest: %v", err)
	}
	_, strangerPriv, _ := ed25519.GenerateKey(rand.Reader)
	if err := Verify(b, Sign(b, strangerPriv), dir); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("untrusted key: %v", err)
	}
	if err := Verify(b, []byte("garbage"), dir); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	// Unpadded base64 is accepted too.
	raw := []byte(base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, b)))
	if err := Verify(b, raw, dir); err != nil {
		t.Fatalf("unpadded signature: %v", err)
	}
}

func TestVerifyNoKeys(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	b := []byte("{}")
	if err := Verify(b, Sign(b, priv), t.TempDir()); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("empty keys dir: %v", err)
	}
	if err := Verify(b, Sign(b, priv), filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("missing keys dir: %v", err)
	}
}

func TestLoadPrivateKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	path := filepath.Join(t.TempDir(), "k.key")
	os.WriteFile(path, []byte(EncodePrivateKey(priv)), 0o600)

	got, err := LoadPrivateKey(path)
	if err != nil || !got.Equal(priv) {
		t.Fatalf("file: %v", err)
	}
	t.Setenv("VOS_TEST_KEY", EncodePrivateKey(priv))
	got, err = LoadPrivateKey("env:VOS_TEST_KEY")
	if err != nil || !got.Equal(priv) {
		t.Fatalf("env: %v", err)
	}

	t.Setenv("VOS_EMPTY_KEY", "")
	if _, err := LoadPrivateKey("env:VOS_EMPTY_KEY"); err == nil {
		t.Fatal("empty env accepted")
	}
	if _, err := LoadPrivateKey(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	// A public key is not a private key.
	os.WriteFile(path, []byte(EncodePublicKey(priv.Public().(ed25519.PublicKey))), 0o600)
	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("public key accepted as private")
	}
	// Halves that do not belong together.
	bad := append(ed25519.PrivateKey{}, priv...)
	bad[40] ^= 1
	os.WriteFile(path, []byte(EncodePrivateKey(bad)), 0o600)
	_, err = LoadPrivateKey(path)
	if err == nil {
		t.Fatal("inconsistent key accepted")
	}
	if strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(bad)[:20]) {
		t.Fatal("error leaks key material")
	}
}

func TestValidVersion(t *testing.T) {
	for _, v := range []string{"20260929.123456", "dev-1", "1"} {
		if !ValidVersion(v) {
			t.Errorf("%q rejected", v)
		}
	}
	for _, v := range []string{"", "a/b", "a+1", "-x", "a b", "..", "é"} {
		if ValidVersion(v) {
			t.Errorf("%q accepted", v)
		}
	}
}
