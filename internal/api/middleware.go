package api

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Headers and cookies of the access-control protocol (docs/CONTRACTS.md).
const (
	csrfHeader    = "X-VOS-CSRF"
	setupHeader   = "X-VOS-Setup"
	sessionCookie = "vos_session"
	setupCookie   = "vos_setup"
)

// setupHost is the mDNS name the live ISO announces.
const setupHost = "vaporos-setup.local"

const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'"

// wrap applies the request-wide middleware, in contract order: source IP,
// Host allowlist, security headers, and a cross-origin check for anything
// that is not a plain read.
func (s *Server) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sourceAllowed(r) {
			Error(w, http.StatusForbidden, "VaporOS only answers the local network")
			return
		}
		if !s.hostAllowed(r) {
			// 421 Misdirected Request: the name the browser used is not one
			// of ours, which is what a DNS-rebinding page looks like. The
			// body names nothing: such a page can read it.
			Error(w, http.StatusMisdirectedRequest, "unknown host name; use the machine's .local name or IP address")
			return
		}
		hdr := w.Header()
		hdr.Set("Content-Security-Policy", contentSecurityPolicy)
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "same-origin")
		hdr.Set("X-Frame-Options", "DENY")
		if !safeMethod(r.Method) && !sameOrigin(r) {
			Error(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// allow enforces access for one request, writing the error response itself
// when it returns false. On success it returns r, carrying the session for
// Authed routes.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, access Access, raw bool) (*http.Request, bool) {
	switch access {
	case Public:
		return r, true
	case Local:
		if ip, ok := remoteIP(r); ok && ip.IsLoopback() {
			return r, true
		}
		deny(w, raw, http.StatusForbidden, "only available on this machine")
		return nil, false
	case Authed:
		return s.requireSession(w, r, raw)
	case Setup:
		return s.requireSetup(w, r, raw)
	case authedOrSetup:
		if info, ok := s.session(w, r); ok {
			s.touchUnlessPassive(r)
			return withSession(r, info), true
		}
		return s.requireSetup(w, r, raw)
	}
	deny(w, raw, http.StatusForbidden, "forbidden")
	return nil, false
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request, raw bool) (*http.Request, bool) {
	info, ok := s.session(w, r)
	if !ok {
		if raw && safeMethod(r.Method) {
			http.Redirect(w, r, loginURL(r), http.StatusSeeOther)
			return nil, false
		}
		deny(w, raw, http.StatusUnauthorized, "login required")
		return nil, false
	}
	if !safeMethod(r.Method) {
		got := r.Header.Get(csrfHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(info.csrf)) != 1 {
			deny(w, raw, http.StatusForbidden, "missing or invalid CSRF token")
			return nil, false
		}
	}
	s.touchUnlessPassive(r)
	return withSession(r, info), true
}

func (s *Server) requireSetup(w http.ResponseWriter, r *http.Request, raw bool) (*http.Request, bool) {
	res, retry, fromCookie := s.checkSetup(r)
	switch res {
	case setupOK:
		s.touchUnlessPassive(r)
		return r, true
	case setupLimited:
		tooMany(w, retry)
		return nil, false
	case setupWrong:
		if fromCookie {
			// A stale cookie (the code changes every boot) should cost the
			// browser one failure, not one per request.
			clearCookie(w, r, setupCookie)
		}
		deny(w, raw, http.StatusForbidden, "wrong or expired setup code")
		return nil, false
	}
	deny(w, raw, http.StatusForbidden, "setup code required")
	return nil, false
}

// passiveHeader marks a request nobody asked for, such as a page refreshing
// itself after an event (docs/CONTRACTS.md, Middleware).
const passiveHeader = "X-VOS-Passive"

// touchUnlessPassive counts r as web UI activity, which keeps the machine
// awake, unless it is passive. EventSource cannot send headers, hence the
// query form.
func (s *Server) touchUnlessPassive(r *http.Request) {
	if r.Header.Get(passiveHeader) == "1" || r.URL.Query().Get("passive") == "1" {
		return
	}
	s.touch()
}

// deny writes an error as JSON for API routes and as text for pages.
func deny(w http.ResponseWriter, raw bool, status int, msg string) {
	if raw {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, msg, status)
		return
	}
	Error(w, status, "%s", msg)
}

// loginURL is where an unauthenticated page view goes, remembering the page
// when its path cannot be turned into an open redirect.
func loginURL(r *http.Request) string {
	next := r.URL.RequestURI()
	if r.URL.Path == "/" || r.URL.Path == "/login" || !strings.HasPrefix(next, "/") ||
		strings.HasPrefix(next, "//") || strings.ContainsRune(next, '\\') {
		return "/login"
	}
	return "/login?next=" + url.QueryEscape(next)
}

func safeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// ---- source address

// remoteIP is the peer address, IPv4-mapped IPv6 unmapped. vosd is never
// behind a proxy, so forwarding headers are deliberately ignored.
func remoteIP(r *http.Request) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap(), true
	}
	if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return a.Unmap(), true
	}
	return netip.Addr{}, false
}

// localNetwork is loopback, RFC 1918, link-local, or IPv6 ULA (IsPrivate
// covers fc00::/7).
func localNetwork(ip netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func (s *Server) sourceAllowed(r *http.Request) bool {
	if ip, ok := remoteIP(r); ok && localNetwork(ip) {
		return true
	}
	return s.publicAllowed()
}

// ---- Host allowlist

// hostCache keeps the hostname and local addresses for a moment, so the
// Host check does not read /etc/hostname and walk interfaces per request.
type hostCache struct {
	mu       sync.Mutex
	at       time.Time
	hostname string
	ips      []netip.Addr
}

const hostCacheTTL = 2 * time.Second

func (s *Server) hostSnapshot() (string, []netip.Addr) {
	c := &s.hosts
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if c.at.IsZero() || now.Sub(c.at) > hostCacheTTL || now.Before(c.at) {
		c.hostname = strings.ToLower(strings.TrimSuffix(config.Hostname(), "."))
		c.ips = c.ips[:0]
		for _, str := range s.localIPs() {
			if a, err := netip.ParseAddr(str); err == nil {
				c.ips = append(c.ips, a.WithZone("").Unmap())
			}
		}
		c.at = now
	}
	return c.hostname, append([]netip.Addr(nil), c.ips...)
}

// normalizeHost lowercases a Host header value and strips the port, IPv6
// brackets and a trailing root dot.
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return strings.TrimSuffix(h, ".")
}

func firstLabel(h string) string {
	l, _, _ := strings.Cut(h, ".")
	return l
}

func (s *Server) hostAllowed(r *http.Request) bool {
	host := normalizeHost(r.Host)
	if host == "" {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.WithZone("").Unmap()
		if ip.IsLoopback() {
			return true
		}
		// The address the client actually connected to is ours by
		// definition; this also covers link-local addresses, which the
		// interface list leaves out.
		if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			if ap, err := netip.ParseAddrPort(la.String()); err == nil && ap.Addr().WithZone("").Unmap() == ip {
				return true
			}
		}
		_, ips := s.hostSnapshot()
		for _, a := range ips {
			if a == ip {
				return true
			}
		}
		return false
	}
	if host == "localhost" || host == setupHost {
		return true
	}
	hn, _ := s.hostSnapshot()
	if hn == "" {
		return false
	}
	short := firstLabel(hn)
	return host == hn || host == hn+".local" || host == short || host == short+".local"
}

// ---- cross-origin writes

// sameOrigin reports whether a state-changing request comes from our own
// pages. Browsers send Origin on every cross-origin write and on same-origin
// fetch/XHR writes; tools like curl send neither header and fall through to
// the CSRF-token check. This backs up SameSite cookies for Public and Setup
// routes, which have no CSRF token.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // includes the opaque "null" origin
	}
	ours := "http"
	if r.TLS != nil {
		ours = "https"
	}
	return u.Scheme == ours && hostPort(u.Host, u.Scheme) == hostPort(r.Host, ours)
}

// hostPort normalises host[:port] with the scheme's default port made
// explicit, so "vapor.local" and "vapor.local:80" compare equal.
func hostPort(h, scheme string) string {
	h = strings.ToLower(h)
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		port = ""
	}
	if port == "" {
		port = "80"
		if scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(strings.TrimSuffix(host, "."), port)
}

// ---- session context

type ctxKey int

const sessionCtxKey ctxKey = 0

type sessionInfo struct {
	key  string // sha256 of the token, the sessions.json key
	csrf string
}

func withSession(r *http.Request, info sessionInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey, info))
}

func sessionFromContext(ctx context.Context) (sessionInfo, bool) {
	info, ok := ctx.Value(sessionCtxKey).(sessionInfo)
	return info, ok
}
