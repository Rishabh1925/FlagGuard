package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func (s *Server) allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	allowed := s.AllowedHosts
	if len(allowed) == 0 {
		allowed = []string{"localhost", "127.0.0.1", "::1"}
	}
	for _, v := range allowed {
		if strings.EqualFold(host, strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

func (s *Server) authorized(r *http.Request) bool {
	if s.AuthToken == "" {
		return true
	}
	// Basic authentication supports native browser prompts; APIs may use Bearer.
	// Remote deployments MUST terminate HTTPS before this loopback/private listener.
	supplied := ""
	if user, password, ok := r.BasicAuth(); ok && user == "operator" {
		supplied = password
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		supplied = strings.TrimPrefix(h, "Bearer ")
	}
	got, want := sha256.Sum256([]byte(supplied)), sha256.Sum256([]byte(s.AuthToken))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
