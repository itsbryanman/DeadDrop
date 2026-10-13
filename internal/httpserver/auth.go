package httpserver

import (
	"net/http"
	"net/url"
	"strings"
)

// authorized checks the bearer token. This is the security boundary for
// every private endpoint.
func (s *Server) authorized(r *http.Request) bool {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	return s.cfg.Session.CheckToken(strings.TrimSpace(h[len(prefix):]))
}

// sameOrigin rejects browser writes initiated by other sites. Requests
// without browser provenance headers (curl, scripts) pass; they still
// need the bearer token.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.origins[u.Scheme+"://"+strings.ToLower(u.Host)]
}
