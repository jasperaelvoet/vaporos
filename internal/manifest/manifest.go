// Package manifest is the signed update manifest (manifest.json +
// manifest.json.sig, ed25519). See docs/CONTRACTS.md "Update format".
//
// The signature covers the exact bytes of manifest.json, so callers verify
// the bytes first and parse them second: nothing in an unverified manifest
// is ever acted on.
package manifest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Schema is the manifest format this updater reads.
const Schema = 1

// UpdaterVersion is compared against a manifest's min_updater: an image that
// needs a newer updater than this one is refused rather than half-applied.
const UpdaterVersion = 1

// Product is the only product a manifest may describe.
const Product = "vaporos"

// The artifacts a manifest carries, by key in Manifest.Artifacts.
const (
	Root   = "root"   // root.erofs, written into a slot partition
	Kernel = "kernel" // vmlinuz, copied to the ESP
	Initrd = "initrd" // initramfs.img, copied to the ESP
	Index  = "index"  // root.erofs.idx, root's block index (optional)
)

// RequiredArtifacts lists the keys Parse insists on.
var RequiredArtifacts = []string{Root, Kernel, Initrd}

// OptionalArtifacts lists the keys Parse checks when they are there.
var OptionalArtifacts = []string{Index}

var (
	// ErrBadSignature means no trusted key verifies the signature.
	ErrBadSignature = errors.New("manifest signature does not verify against any trusted key")
	// ErrNoKeys means the keys directory holds no usable public key.
	ErrNoKeys = errors.New("no trusted update keys")
)

type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema        int                 `json:"schema"`
	Product       string              `json:"product"`
	Version       string              `json:"version"`
	RollbackIndex int64               `json:"rollback_index"`
	Channel       string              `json:"channel"`
	Git           string              `json:"git"`
	Created       string              `json:"created"`
	Kernel        string              `json:"kernel"`
	Cmdline       string              `json:"cmdline"`
	MinUpdater    int                 `json:"min_updater"`
	Artifacts     map[string]Artifact `json:"artifacts"` // "root", "kernel", "initrd", "index"
	// Extensions are the sealed extension images built with this image
	// (docs/CONTRACTS.md "Extensions"), by id. Optional: older manifests
	// have none, and older updaters ignore the field.
	Extensions map[string]Extension `json:"extensions,omitempty"`
}

// Extension is one sealed extension image: an erofs file whose fs-verity
// digest (sha256, 4096-byte blocks, no salt) the image's catalog records.
type Extension struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	FSVerity string   `json:"fsverity"`
	Core     bool     `json:"core,omitempty"`
	Requires []string `json:"requires,omitempty"`
	Key      string   `json:"key,omitempty"` // the build's input key, for reuse
}

// Artifact is e as a download: what Source.Fetch verifies.
func (e Extension) Artifact() Artifact { return Artifact{Name: e.Name, Size: e.Size, SHA256: e.SHA256} }

var (
	// A version becomes a directory on the ESP (/vos/<ver>/) and part of a
	// loader entry's file name, where '+' starts the boot counter. So: no
	// path separators, no '+', no leading dot, and short.
	versionRe  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	fileNameRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	extIDRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	extKeyRe   = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
)

// reservedExtensionIDs are the words vosd's routes use where an extension
// id goes (POST /extensions/skip-once), so no extension can take them.
var reservedExtensionIDs = []string{"skip-once"}

// ValidExtensionID reports whether id can name an extension: it becomes a
// file name, a mount point, a word in the initramfs's catalog and a path
// segment of vosd's routes.
func ValidExtensionID(id string) bool {
	return extIDRe.MatchString(id) && !slices.Contains(reservedExtensionIDs, id)
}

// ExtensionFile is the published name of extension id's image.
func ExtensionFile(id string) string { return "ext-" + id + ".raw" }

// ValidVersion reports whether v is safe to use as an image version: as an
// ESP directory name and inside a loader entry's file name.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Parse decodes and sanity-checks a manifest (schema, product, required
// artifacts, a sane version). It does not check the signature: call Verify
// on the same bytes first.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if dec.More() {
		return nil, errors.New("manifest: trailing data after the JSON object")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks everything Parse checks on an already decoded manifest.
func (m *Manifest) Validate() error {
	if m.Schema != Schema {
		return fmt.Errorf("manifest: unsupported schema %d (this updater reads %d)", m.Schema, Schema)
	}
	if m.Product != Product {
		return fmt.Errorf("manifest: product is %q, not %q", m.Product, Product)
	}
	if m.MinUpdater > UpdaterVersion {
		return fmt.Errorf("manifest: needs updater version %d, this is %d; update in two steps", m.MinUpdater, UpdaterVersion)
	}
	if !ValidVersion(m.Version) {
		return fmt.Errorf("manifest: invalid version %q", m.Version)
	}
	if m.RollbackIndex < 0 {
		return fmt.Errorf("manifest: negative rollback_index %d", m.RollbackIndex)
	}
	// The cmdline ends up as one line of a loader entry; a line break would
	// let it smuggle in other keys.
	if strings.ContainsFunc(m.Cmdline, isControl) || len(m.Cmdline) > 4096 {
		return errors.New("manifest: cmdline contains control characters or is too long")
	}
	for _, key := range slices.Concat(RequiredArtifacts, OptionalArtifacts) {
		a, ok := m.Artifacts[key]
		if !ok {
			if slices.Contains(OptionalArtifacts, key) {
				continue
			}
			return fmt.Errorf("manifest: missing artifact %q", key)
		}
		if !fileNameRe.MatchString(a.Name) {
			return fmt.Errorf("manifest: artifact %q has an invalid name %q", key, a.Name)
		}
		if a.Size <= 0 {
			return fmt.Errorf("manifest: artifact %q has size %d", key, a.Size)
		}
		if !sha256Re.MatchString(a.SHA256) {
			return fmt.Errorf("manifest: artifact %q has an invalid sha256 %q", key, a.SHA256)
		}
	}
	return validateExtensions(m.Extensions)
}

// ValidateExtensions checks a manifest's extensions map as Validate does;
// the build checks the entries it writes with it.
func ValidateExtensions(exts map[string]Extension) error { return validateExtensions(exts) }

// validateExtensions checks every entry and that requires names extensions
// of the same manifest, without cycles.
func validateExtensions(exts map[string]Extension) error {
	for id, e := range exts {
		if !ValidExtensionID(id) {
			return fmt.Errorf("manifest: invalid extension id %q", id)
		}
		if e.Name != ExtensionFile(id) {
			return fmt.Errorf("manifest: extension %q has name %q, not %q", id, e.Name, ExtensionFile(id))
		}
		if e.Size <= 0 {
			return fmt.Errorf("manifest: extension %q has size %d", id, e.Size)
		}
		if !sha256Re.MatchString(e.SHA256) {
			return fmt.Errorf("manifest: extension %q has an invalid sha256 %q", id, e.SHA256)
		}
		if !sha256Re.MatchString(e.FSVerity) {
			return fmt.Errorf("manifest: extension %q has an invalid fsverity digest %q", id, e.FSVerity)
		}
		if e.Key != "" && !extKeyRe.MatchString(e.Key) {
			return fmt.Errorf("manifest: extension %q has an invalid key %q", id, e.Key)
		}
		for _, r := range e.Requires {
			if r == id {
				return fmt.Errorf("manifest: extension %q requires itself", id)
			}
			if _, ok := exts[r]; !ok {
				return fmt.Errorf("manifest: extension %q requires %q, which the manifest does not carry", id, r)
			}
		}
	}
	// Depth-first search for a cycle; ids are visited in sorted order so
	// the error names the same extension every time.
	ids := make([]string, 0, len(exts))
	for id := range exts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	const (
		unseen = iota
		active
		done
	)
	state := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case active:
			return fmt.Errorf("manifest: extension %q is part of a requires cycle", id)
		case done:
			return nil
		}
		state[id] = active
		for _, r := range exts[id].Requires {
			if err := visit(r); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// Artifact returns the artifact stored under key ("root", "kernel", "initrd").
func (m *Manifest) Artifact(key string) Artifact { return m.Artifacts[key] }

// Has reports whether the manifest carries an artifact under key.
func (m *Manifest) Has(key string) bool {
	_, ok := m.Artifacts[key]
	return ok
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Verify checks sig (base64 ed25519 signature file contents) over b against
// every *.pub in keysDir; nil if any key verifies.
func Verify(b, sig []byte, keysDir string) error {
	s, err := decodeBase64(string(sig))
	if err != nil || len(s) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed signature file", ErrBadSignature)
	}
	keys, err := LoadPublicKeys(keysDir)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if ed25519.Verify(k, b, s) {
			return nil
		}
	}
	return ErrBadSignature
}

// LoadPublicKeys reads every *.pub in dir (base64 of a raw 32-byte ed25519
// public key, one line). Malformed files are skipped so one stray file
// cannot lock out the others; no usable key at all is ErrNoKeys.
func LoadPublicKeys(dir string) ([]ed25519.PublicKey, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.pub"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var keys []ed25519.PublicKey
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if k, err := ParsePublicKey(string(b)); err == nil {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoKeys, dir)
	}
	return keys, nil
}

// ParsePublicKey decodes the contents of a .pub file.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := decodeBase64(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not a base64 ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// Sign returns the base64 signature file contents for b.
func Sign(b []byte, key ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, b)) + "\n")
}

// EncodePublicKey returns the contents of a .pub file for k.
func EncodePublicKey(k ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(k) + "\n"
}

// EncodePrivateKey returns the contents of a private key file for k.
func EncodePrivateKey(k ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(k) + "\n"
}

// LoadPrivateKey reads a base64 64-byte key from a file path or "env:VAR".
// Errors never include the key material.
func LoadPrivateKey(spec string) (ed25519.PrivateKey, error) {
	var text string
	if name, ok := strings.CutPrefix(spec, "env:"); ok {
		v, set := os.LookupEnv(name)
		if !set || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("signing key: environment variable %s is empty", name)
		}
		text = v
	} else {
		b, err := os.ReadFile(spec)
		if err != nil {
			return nil, fmt.Errorf("signing key: %w", err)
		}
		text = string(b)
	}
	b, err := decodeBase64(text)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key %s: not base64 of a 64-byte ed25519 private key", spec)
	}
	key := ed25519.PrivateKey(b)
	// The second half is the public key; a key whose halves disagree would
	// produce signatures that nothing verifies.
	if !bytes.Equal(ed25519.NewKeyFromSeed(key.Seed()), key) {
		return nil, fmt.Errorf("signing key %s: public half does not match the seed", spec)
	}
	return key, nil
}

// decodeBase64 accepts standard base64 with or without padding, ignoring
// surrounding whitespace (keys and signatures are one-line files).
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
