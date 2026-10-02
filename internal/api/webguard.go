package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// GuardWeb admits one request to an extension's web UI, which vosd
// proxies on a port of its own (docs/CONTRACTS.md "HTTP API", Extension web
// UIs). The source must be on the local network whatever web.allow_public
// says, the Host one of vosd's names, a write same-origin, and the request
// must carry a live admin session; a signed-out GET or HEAD goes to vosd's
// sign-in page with the address to come back to. A navigation or a write
// counts as web UI activity, a page's own polling does not. GuardWeb adds
// none of vosd's headers. When it returns false it has answered.
func (s *Server) GuardWeb(w http.ResponseWriter, r *http.Request) bool {
	if ip, ok := remoteIP(r); !ok || !localNetwork(ip) {
		http.Error(w, "VaporOS only answers the local network", http.StatusForbidden)
		return false
	}
	if !s.hostAllowed(r) {
		http.Error(w, "unknown host name; use the machine's .local name or IP address", http.StatusMisdirectedRequest)
		return false
	}
	if !safeMethod(r.Method) && !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return false
	}
	if _, ok := s.session(w, r); !ok {
		w.Header().Set("Cache-Control", "no-store")
		if safeMethod(r.Method) {
			http.Redirect(w, r, s.webLoginURL(r), http.StatusSeeOther)
			return false
		}
		http.Error(w, "login required", http.StatusUnauthorized)
		return false
	}
	if !safeMethod(r.Method) || navigation(r) {
		s.touch()
	}
	return true
}

// webLoginURL is vosd's sign-in page on the host the request named, with
// the request's own address as next. The host passed the allowlist and the
// port is the listener's, so next never leads off the box.
func (s *Server) webLoginURL(r *http.Request) string {
	host := normalizeHost(r.Host)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	login := host
	if _, port, err := net.SplitHostPort(s.opts.Addr); err == nil && port != "" && port != "80" {
		login += ":" + port
	}
	next := "http://" + host
	if port := listenerPort(r); port != "" {
		next += ":" + port
	}
	next += r.URL.RequestURI()
	return "http://" + login + "/login?next=" + url.QueryEscape(next)
}

// listenerPort is the port the request came in on: the listener's, or the
// Host header's when the connection does not say (tests).
func listenerPort(r *http.Request) string {
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, port, err := net.SplitHostPort(la.String()); err == nil {
			return port
		}
	}
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" && strings.Trim(port, "0123456789") == "" {
		return port
	}
	return ""
}

// navigation reports whether a request loads a page rather than data for
// one. Browsers send Sec-Fetch-Mode only to secure origins, which a LAN
// address over http is not, so Accept decides without it.
func navigation(r *http.Request) bool {
	if !safeMethod(r.Method) {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
