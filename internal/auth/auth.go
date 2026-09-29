// Package auth stores the web admin password (argon2id) in
// /var/lib/vos/auth.json. The installer and vosd both use it.
//
// Hashes use the PHC string format that libargon2 and most other
// implementations read and write:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
//
// with unpadded standard base64 for salt and hash.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"golang.org/x/crypto/argon2"
)

// AdminUser is the only web account. It is not a Unix account.
const AdminUser = "admin"

// Password length limits. The minimum keeps a LAN-reachable admin page from
// being guarded by a trivially guessable password; the maximum only bounds
// request handling, argon2 itself does not care.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 1024
)

// params are the argon2id cost parameters for new hashes. They are
// variables so tests can use cheap ones; production code never changes them.
var params = hashParams{memory: 64 * 1024, time: 3, threads: 4, keyLen: 32, saltLen: 16}

type hashParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

// Upper bounds for parameters read back from a stored hash. A hash is only
// ever written by us, but bounding what we accept keeps a damaged or
// hand-edited auth.json from making every login allocate gigabytes.
const (
	maxMemory  = 1 << 20 // 1 GiB in KiB
	maxTime    = 16
	maxKeyLen  = 128
	minKeyLen  = 16
	minSaltLen = 8
	maxSaltLen = 64
)

// hashSlots caps concurrent argon2 computations. Each one allocates
// params.memory (64 MiB), so a burst of logins must queue rather than run
// the appliance out of memory.
var hashSlots = make(chan struct{}, 2)

var (
	ErrNoAdmin      = errors.New("no admin password is set")
	ErrBadHash      = errors.New("malformed password hash")
	ErrPasswordLen  = fmt.Errorf("the password must be %d to %d characters long", MinPasswordLen, MaxPasswordLen)
	ErrPasswordUTF8 = errors.New("the password must be valid UTF-8")
)

// File is the auth.json document.
type File struct {
	User string `json:"user"`
	Hash string `json:"hash"`
}

// ValidatePassword checks a new admin password against the length rules.
// Every place that sets a password (web setup, password change, installer)
// should call it so the rules agree.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) {
		return ErrPasswordUTF8
	}
	if n := utf8.RuneCountInString(password); n < MinPasswordLen || len(password) > MaxPasswordLen {
		return ErrPasswordLen
	}
	return nil
}

// HashPassword returns an encoded argon2id hash ("$argon2id$v=19$m=…").
func HashPassword(password string) (string, error) {
	p := params
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("reading random salt: %w", err)
	}
	key := derive(password, salt, p)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the encoded hash. A
// malformed hash never matches.
func CheckPassword(encoded, password string) bool {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return false
	}
	got := derive(password, salt, p)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func derive(password string, salt []byte, p hashParams) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
}

// parseHash decodes a PHC argon2id string into its parameters, salt and key.
func parseHash(encoded string) (hashParams, []byte, []byte, error) {
	var p hashParams
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrBadHash
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, fmt.Errorf("%w: unsupported version %q", ErrBadHash, parts[2])
	}
	seen := map[string]bool{}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || seen[k] {
			return p, nil, nil, ErrBadHash
		}
		seen[k] = true
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return p, nil, nil, ErrBadHash
		}
		switch k {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.time = uint32(n)
		case "p":
			if n > 255 {
				return p, nil, nil, ErrBadHash
			}
			p.threads = uint8(n)
		default:
			return p, nil, nil, ErrBadHash
		}
	}
	if !seen["m"] || !seen["t"] || !seen["p"] {
		return p, nil, nil, ErrBadHash
	}
	if p.time < 1 || p.time > maxTime || p.threads < 1 || p.memory < 8*uint32(p.threads) || p.memory > maxMemory {
		return p, nil, nil, fmt.Errorf("%w: parameters out of range", ErrBadHash)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen || len(salt) > maxSaltLen {
		return p, nil, nil, ErrBadHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < minKeyLen || len(key) > maxKeyLen {
		return p, nil, nil, ErrBadHash
	}
	p.keyLen = uint32(len(key))
	p.saltLen = uint32(len(salt))
	return p, salt, key, nil
}

// path returns auth.json, optionally below root (the installer's target).
func path(root string) string {
	if root == "" {
		return config.AuthPath()
	}
	return filepath.Join(root, config.AuthPath())
}

// SetAdminPassword writes auth.json (mode 0600) under config.StateDir, or
// under root+StateDir when root != "" (installer writing the target disk).
// It hashes password as given; callers that take a password from a user
// validate it first with ValidatePassword.
func SetAdminPassword(root, password string) error {
	if password == "" {
		return ErrPasswordLen
	}
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	return config.WriteJSONAtomic(path(root), File{User: AdminUser, Hash: h}, 0o600)
}

// Load reads auth.json. A file without a well-formed argon2id hash is an
// error, so a damaged file behaves like a missing one (see HasAdmin).
func Load() (*File, error) {
	f := &File{}
	if err := config.ReadJSON(config.AuthPath(), f); err != nil {
		return nil, err
	}
	if _, _, _, err := parseHash(f.Hash); err != nil {
		return nil, fmt.Errorf("%s: %w", config.AuthPath(), err)
	}
	return f, nil
}

// HasAdmin reports whether auth.json exists and holds a usable hash. A
// damaged file counts as missing: that re-enables first-run setup instead
// of locking the owner out of their own machine for good.
func HasAdmin() bool {
	_, err := Load()
	return err == nil
}

// VerifyAdmin checks password against auth.json. It returns ErrNoAdmin
// when no usable admin password exists.
func VerifyAdmin(password string) (bool, error) {
	f, err := Load()
	if err != nil {
		return false, ErrNoAdmin
	}
	return CheckPassword(f.Hash, password), nil
}
