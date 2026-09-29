// Package manifest is the signed update manifest (manifest.json +
// manifest.json.sig, ed25519). See docs/CONTRACTS.md "Update format".
package manifest

import (
	"crypto/ed25519"
	"errors"
)

var ErrNotImplemented = errors.New("not implemented")

const Schema = 1
const UpdaterVersion = 1 // compared against min_updater

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
	Artifacts     map[string]Artifact `json:"artifacts"` // "root", "kernel", "initrd"
}

// Parse decodes and sanity-checks a manifest (schema, required artifacts).
func Parse(b []byte) (*Manifest, error) { return nil, ErrNotImplemented }

// Verify checks sig (base64 ed25519 signature file contents) over b against
// every *.pub in keysDir; nil if any key verifies.
func Verify(b, sig []byte, keysDir string) error { return ErrNotImplemented }

// Sign returns the base64 signature file contents for b.
func Sign(b []byte, key ed25519.PrivateKey) []byte { return nil }

// LoadPrivateKey reads a base64 64-byte key from a file path or "env:VAR".
func LoadPrivateKey(spec string) (ed25519.PrivateKey, error) { return nil, ErrNotImplemented }
