package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"golang.org/x/crypto/argon2"
)

// cheap swaps in fast argon2 parameters for the duration of a test.
func cheap(t *testing.T) {
	t.Helper()
	old := params
	params = hashParams{memory: 64, time: 1, threads: 1, keyLen: 32, saltLen: 16}
	t.Cleanup(func() { params = old })
}

func stateDir(t *testing.T) string {
	t.Helper()
	old := config.StateDir
	config.StateDir = t.TempDir()
	t.Cleanup(func() { config.StateDir = old })
	return config.StateDir
}

func TestHashFormatUsesProductionParams(t *testing.T) {
	// One real-cost hash, to pin the documented parameters.
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected hash prefix: %s", h)
	}
	parts := strings.Split(h, "$")
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		t.Fatalf("salt: %v len %d", err, len(salt))
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) != 32 {
		t.Fatalf("key: %v len %d", err, len(key))
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "correct horsE") {
		t.Fatal("check mismatch")
	}
}

func TestHashRoundTripAndSalting(t *testing.T) {
	cheap(t)
	a, _ := HashPassword("hunter22")
	b, _ := HashPassword("hunter22")
	if a == b {
		t.Fatal("two hashes of the same password are identical: salt not random")
	}
	for _, h := range []string{a, b} {
		if !CheckPassword(h, "hunter22") {
			t.Fatalf("CheckPassword(%s) = false", h)
		}
		if CheckPassword(h, "hunter23") || CheckPassword(h, "") {
			t.Fatal("wrong password accepted")
		}
	}
}

func TestCheckPasswordKnownVector(t *testing.T) {
	// Built independently of HashPassword so the encoder and parser can't
	// share a bug that cancels out.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("password"), salt, 2, 256, 2, 32)
	b64 := base64.RawStdEncoding
	h := "$argon2id$v=19$m=256,t=2,p=2$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key)
	if !CheckPassword(h, "password") {
		t.Fatal("known vector rejected")
	}
}

func TestCheckPasswordRejectsMalformed(t *testing.T) {
	cheap(t)
	good, _ := HashPassword("pw-pw-pw-pw")
	parts := strings.Split(good, "$")
	b64salt, b64key := parts[4], parts[5]
	cases := map[string]string{
		"empty":         "",
		"argon2i":       "$argon2i$v=19$m=64,t=1,p=1$" + b64salt + "$" + b64key,
		"old version":   "$argon2id$v=16$m=64,t=1,p=1$" + b64salt + "$" + b64key,
		"missing param": "$argon2id$v=19$m=64,t=1$" + b64salt + "$" + b64key,
		"dup param":     "$argon2id$v=19$m=64,m=64,t=1,p=1$" + b64salt + "$" + b64key,
		"unknown param": "$argon2id$v=19$m=64,t=1,p=1,x=2$" + b64salt + "$" + b64key,
		"huge memory":   "$argon2id$v=19$m=4194304,t=1,p=1$" + b64salt + "$" + b64key,
		"zero time":     "$argon2id$v=19$m=64,t=0,p=1$" + b64salt + "$" + b64key,
		"many threads":  "$argon2id$v=19$m=64,t=1,p=300$" + b64salt + "$" + b64key,
		"bad salt b64":  "$argon2id$v=19$m=64,t=1,p=1$!!!$" + b64key,
		"short key":     "$argon2id$v=19$m=64,t=1,p=1$" + b64salt + "$AAAA",
		"extra field":   good + "$x",
		"padded b64":    "$argon2id$v=19$m=64,t=1,p=1$" + b64salt + "==$" + b64key,
	}
	for name, h := range cases {
		if CheckPassword(h, "pw-pw-pw-pw") {
			t.Errorf("%s: malformed hash accepted", name)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for pw, ok := range map[string]bool{
		"":                              false,
		"short":                         false,
		"1234567":                       false,
		"12345678":                      true,
		"ünïcødé!":                      true, // 8 runes, more bytes
		strings.Repeat("x", 1024):       true,
		strings.Repeat("x", 1025):       false,
		string([]byte{0xff, 0xfe, 'a'}): false,
	} {
		if err := ValidatePassword(pw); (err == nil) != ok {
			t.Errorf("ValidatePassword(%q) = %v, want ok=%v", pw, err, ok)
		}
	}
}

func TestSetAdminPasswordAndVerify(t *testing.T) {
	cheap(t)
	dir := stateDir(t)
	if HasAdmin() {
		t.Fatal("HasAdmin with no auth.json")
	}
	if _, err := VerifyAdmin("x"); err != ErrNoAdmin {
		t.Fatalf("VerifyAdmin without file: %v", err)
	}
	if err := SetAdminPassword("", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode %v, want 0600", st.Mode().Perm())
	}
	if !HasAdmin() {
		t.Fatal("HasAdmin false after SetAdminPassword")
	}
	f, err := Load()
	if err != nil || f.User != "admin" {
		t.Fatalf("Load: %+v %v", f, err)
	}
	if ok, err := VerifyAdmin("s3cret-pass"); !ok || err != nil {
		t.Fatalf("VerifyAdmin(right) = %v, %v", ok, err)
	}
	if ok, _ := VerifyAdmin("wrong-pass"); ok {
		t.Fatal("VerifyAdmin(wrong) = true")
	}
	if err := SetAdminPassword("", ""); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestSetAdminPasswordUnderRoot(t *testing.T) {
	cheap(t)
	stateDir(t)
	root := t.TempDir()
	if err := SetAdminPassword(root, "target-pass"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, config.AuthPath())
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("auth.json not written under root: %v", err)
	}
	if HasAdmin() {
		t.Fatal("writing under root must not touch the running system's auth.json")
	}
}

func TestDamagedAuthFileIsNoAdmin(t *testing.T) {
	dir := stateDir(t)
	for _, body := range []string{"{", `{"user":"admin","hash":""}`, `{"user":"admin","hash":"plaintext"}`} {
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if HasAdmin() {
			t.Errorf("HasAdmin true for %q", body)
		}
	}
}

func TestVerifyLeavesTheQueueWhenTheClientGoesAway(t *testing.T) {
	cheap(t)
	stateDir(t)
	if err := SetAdminPassword("", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	// Occupy every hash slot, as a burst of other logins would.
	for range cap(hashSlots) {
		hashSlots <- struct{}{}
	}
	released := false
	release := func() {
		if !released {
			released = true
			for range cap(hashSlots) {
				<-hashSlots
			}
		}
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := VerifyAdminContext(ctx, "s3cret-pass")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("returned %v while every slot was taken", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled check kept waiting for a hash slot")
	}
	release()
	if ok, err := VerifyAdminContext(context.Background(), "s3cret-pass"); !ok || err != nil {
		t.Fatalf("after the queue drained: %v, %v", ok, err)
	}
	// An already-cancelled check never hashes, even with a free slot.
	if _, err := VerifyAdminContext(ctx, "s3cret-pass"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: err = %v", err)
	}
}
