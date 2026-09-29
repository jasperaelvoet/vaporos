package api

import "net/http"

// allow enforces access for one request, writing the error response itself
// when it returns false. SKELETON: replace with the real policy.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, access Access) bool {
	return true
}

// wrap applies the request-wide middleware (source IP, Host allowlist,
// security headers). SKELETON: replace with the real policy.
func (s *Server) wrap(h http.Handler) http.Handler { return h }
