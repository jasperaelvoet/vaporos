package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

const (
	sessionTTL = 30 * 24 * time.Hour
	// sessionRefresh is how stale an expiry may get before a request slides
	// it forward. Sliding at most hourly keeps sessions.json from being
	// rewritten on every click.
	sessionRefresh = time.Hour
	// maxSessions bounds sessions.json; the session closest to expiry goes
	// first. One admin with a few browsers never gets near it.
	maxSessions = 32
	tokenBytes  = 32
)

// sessionRec is one sessions.json value. The key is the hex sha256 of the
// cookie's token bytes, so the file alone cannot be used to log in.
type sessionRec struct {
	CSRF    string    `json:"csrf"`
	Expires time.Time `json:"expires"`
}

type sessionStore struct {
	mu   sync.Mutex
	path string
	m    map[string]sessionRec
	now  func() time.Time
}

func newSessionStore(path string, now func() time.Time) *sessionStore {
	st := &sessionStore{path: path, m: map[string]sessionRec{}, now: now}
	var onDisk map[string]sessionRec
	err := config.ReadJSON(path, &onDisk)
	switch {
	case err == nil:
		t := now()
		for k, rec := range onDisk {
			if b, err := hex.DecodeString(k); err == nil && len(b) == sha256.Size && rec.CSRF != "" && rec.Expires.After(t) {
				st.m[k] = rec
			}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		// A damaged file only costs everyone a fresh login.
		log.Printf("api: ignoring sessions: %v", err)
	}
	return st
}

// randomToken returns n random bytes, base64url without padding.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenKey maps a cookie value to its sessions.json key; ok is false for
// anything that is not a well-formed token.
func tokenKey(token string) (string, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != tokenBytes {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

// create starts a session and returns its cookie token and CSRF token.
func (st *sessionStore) create() (token, csrf string, err error) {
	if token, err = randomToken(tokenBytes); err != nil {
		return "", "", err
	}
	if csrf, err = randomToken(tokenBytes); err != nil {
		return "", "", err
	}
	key, _ := tokenKey(token)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked()
	for len(st.m) >= maxSessions {
		st.evictOldestLocked()
	}
	st.m[key] = sessionRec{CSRF: csrf, Expires: st.now().Add(sessionTTL)}
	st.saveLocked()
	return token, csrf, nil
}

// lookup validates a cookie token. refreshed reports that the expiry slid
// forward, in which case the caller re-sends the cookie.
func (st *sessionStore) lookup(token string) (key string, rec sessionRec, refreshed, ok bool) {
	key, ok = tokenKey(token)
	if !ok {
		return "", sessionRec{}, false, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok = st.m[key]
	if !ok {
		return "", sessionRec{}, false, false
	}
	now := st.now()
	if !rec.Expires.After(now) {
		delete(st.m, key)
		st.saveLocked()
		return "", sessionRec{}, false, false
	}
	if rec.Expires.Sub(now) < sessionTTL-sessionRefresh {
		rec.Expires = now.Add(sessionTTL)
		st.m[key] = rec
		st.saveLocked()
		refreshed = true
	}
	return key, rec, refreshed, true
}

// valid reports whether the session key is still live, without sliding it.
func (st *sessionStore) valid(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.m[key]
	return ok && rec.Expires.After(st.now())
}

func (st *sessionStore) delete(key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.m[key]; ok {
		delete(st.m, key)
		st.saveLocked()
	}
}

// deleteAllExcept ends every session but keep (after a password change).
func (st *sessionStore) deleteAllExcept(keep string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for k := range st.m {
		if k != keep {
			delete(st.m, k)
		}
	}
	st.saveLocked()
}

func (st *sessionStore) pruneLocked() {
	now := st.now()
	for k, rec := range st.m {
		if !rec.Expires.After(now) {
			delete(st.m, k)
		}
	}
}

func (st *sessionStore) evictOldestLocked() {
	var oldest string
	var at time.Time
	for k, rec := range st.m {
		if oldest == "" || rec.Expires.Before(at) {
			oldest, at = k, rec.Expires
		}
	}
	delete(st.m, oldest)
}

// saveLocked persists the sessions atomically. A failure is logged, not
// returned: sessions keep working in memory until vosd restarts, which beats
// refusing logins because the data partition is full.
func (st *sessionStore) saveLocked() {
	if err := config.WriteJSONAtomic(st.path, st.m, 0o600); err != nil {
		log.Printf("api: saving sessions: %v", err)
	}
}

// ---- cookies and request plumbing

// session returns the request's live session, sliding its expiry (and
// re-sending the cookie) when due. w may be nil for read-only checks.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (sessionInfo, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return sessionInfo{}, false
	}
	key, rec, refreshed, ok := s.sessions.lookup(c.Value)
	if !ok {
		if w != nil {
			clearCookie(w, r, sessionCookie)
		}
		return sessionInfo{}, false
	}
	if refreshed && w != nil {
		setSessionCookie(w, r, c.Value)
	}
	return sessionInfo{key: key, csrf: rec.CSRF}, true
}

// startSession creates a session, sets its cookie and returns the CSRF token.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) (string, error) {
	token, csrf, err := s.sessions.create()
	if err != nil {
		return "", err
	}
	setSessionCookie(w, r, token)
	return csrf, nil
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

// Authenticated reports whether r carries a valid admin session. Pages use
// it to choose between a view and a redirect to /login.
func (s *Server) Authenticated(r *http.Request) bool {
	_, ok := s.session(nil, r)
	return ok
}
