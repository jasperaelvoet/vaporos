// Package auth stores the web admin password (argon2id) in
// /var/lib/vos/auth.json. The installer and vosd both use it.
package auth

import "errors"

var ErrNotImplemented = errors.New("not implemented")

// HashPassword returns an encoded argon2id hash ("$argon2id$v=19$m=…").
func HashPassword(password string) (string, error) { return "", ErrNotImplemented }

// CheckPassword reports whether password matches the encoded hash.
func CheckPassword(encoded, password string) bool { return false }

// SetAdminPassword writes auth.json (mode 0600) under config.StateDir, or
// under root+StateDir when root != "" (installer writing the target disk).
func SetAdminPassword(root, password string) error { return ErrNotImplemented }

// HasAdmin reports whether auth.json exists.
func HasAdmin() bool { return false }
