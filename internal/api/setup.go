package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// maxCodeInput bounds what we bother normalising; real codes are 9 chars.
const maxCodeInput = 64

// NormalizeCode canonicalises a setup code the way a person might type it:
// case, dashes and spaces are ignored, and the Crockford base32 look-alikes
// O, I and L read as 0, 1 and 1.
func NormalizeCode(code string) string {
	if len(code) > maxCodeInput {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codesEqual compares two codes after normalisation, in constant time.
func codesEqual(given, want string) bool {
	g, w := NormalizeCode(given), NormalizeCode(want)
	if g == "" || w == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(g), []byte(w)) == 1
}

type setupResult int

const (
	setupNone    setupResult = iota // no code presented
	setupOK                         // correct code
	setupWrong                      // a code, but not the current one
	setupLimited                    // too many wrong codes from this client
)

// checkSetup looks for a setup code in the X-VOS-Setup header, else in the
// vos_setup cookie. Wrong codes count toward the per-client limit however
// they arrive; otherwise the cookie would be an unmetered guessing channel.
func (s *Server) checkSetup(r *http.Request) (res setupResult, retry time.Duration, fromCookie bool) {
	given := r.Header.Get(setupHeader)
	if given == "" {
		if c, err := r.Cookie(setupCookie); err == nil && c.Value != "" {
			given, fromCookie = c.Value, true
		}
	}
	if given == "" {
		return setupNone, 0, false
	}
	code := s.SetupCode()
	if code == "" {
		// Nothing to guess: setup is over. Deny without counting.
		return setupWrong, 0, fromCookie
	}
	key := limitKey(r)
	if retry, ok := s.setupLimit.check(key); !ok {
		return setupLimited, retry, fromCookie
	}
	if codesEqual(given, code) {
		return setupOK, 0, fromCookie
	}
	s.setupLimit.fail(key)
	return setupWrong, 0, fromCookie
}

// SetSetupCookie checks code (from GET /setup?code=…, typically the QR
// code) and, if it is the current setup code, sets the vos_setup cookie that
// unlocks Setup routes for this browser. It returns false for a wrong or
// missing code, when no setup is pending, or while the client is locked out
// for guessing; a wrong code counts toward that lockout.
func (s *Server) SetSetupCookie(w http.ResponseWriter, r *http.Request, code string) bool {
	cur := s.SetupCode()
	if cur == "" || code == "" {
		return false
	}
	key := limitKey(r)
	if _, ok := s.setupLimit.check(key); !ok {
		return false
	}
	if !codesEqual(code, cur) {
		s.setupLimit.fail(key)
		return false
	}
	s.setupLimit.reset(key)
	http.SetCookie(w, &http.Cookie{
		Name:     setupCookie,
		Value:    NormalizeCode(cur),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	s.touch()
	return true
}

// HasSetupAccess reports whether r carries the current setup code (header or
// cookie). Pages use it to pick between the wizard and "enter the code".
// Like the Setup guard, a wrong code counts toward the client's lockout.
func (s *Server) HasSetupAccess(r *http.Request) bool {
	res, _, _ := s.checkSetup(r)
	return res == setupOK
}

// stillAllowed re-checks, without side effects, that the credential a
// long-lived request was admitted with is still good (an event stream ends
// when its session is logged out or setup finishes).
func (s *Server) stillAllowed(r *http.Request) bool {
	if info, ok := sessionFromContext(r.Context()); ok {
		return s.sessions.valid(info.key)
	}
	code := s.SetupCode()
	if code == "" {
		return false
	}
	if h := r.Header.Get(setupHeader); h != "" {
		return codesEqual(h, code)
	}
	c, err := r.Cookie(setupCookie)
	return err == nil && codesEqual(c.Value, code)
}
