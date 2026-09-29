package daemon

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

// crockford is Crockford's base32 alphabet: no I, L, O or U, so a code read
// off a TV across the room cannot be mistyped into a different valid code
// (the api also reads O as 0 and I/L as 1).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var codeRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// setupCodePath keeps the code for the rest of the boot. /run is tmpfs, so
// every boot gets a new one, but a vosd restart (Restart=always) does not
// invalidate the code already on screen and in the browser's cookie.
func setupCodePath() string { return filepath.Join(config.RunDir, "setup-code") }

// setupCode returns the code that unlocks Setup routes when one is needed:
// always in installer mode, and on an installed system until an admin
// password exists. "" means none.
func setupCode(live bool) string {
	if !live && auth.HasAdmin() {
		forgetSetupCode()
		return ""
	}
	if b, err := os.ReadFile(setupCodePath()); err == nil {
		if c := string(b); codeRE.MatchString(c) {
			return c
		}
	}
	c, err := newSetupCode()
	if err != nil {
		// Without a code the installer cannot be used, so say so loudly;
		// crypto/rand does not fail on any kernel VaporOS runs.
		log.Printf("setup code: %v", err)
		return ""
	}
	if err := config.WriteFileAtomic(setupCodePath(), []byte(c), 0o600); err != nil {
		log.Printf("setup code: saving: %v", err)
	}
	return c
}

// newSetupCode returns 40 random bits as eight Crockford base32 characters,
// formatted XXXX-XXXX.
func newSetupCode() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	v := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	out := make([]byte, 0, 9)
	for i := 7; i >= 0; i-- {
		out = append(out, crockford[(v>>(5*uint(i)))&31])
		if i == 4 {
			out = append(out, '-')
		}
	}
	return string(out), nil
}

// forgetSetupCode removes the saved code once setup is over.
func forgetSetupCode() {
	if err := os.Remove(setupCodePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("setup code: removing: %v", err)
	}
}
